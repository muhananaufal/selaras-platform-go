package telemetry_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// recordingProvider memasang tracer provider yang merekam ke memori sebagai
// provider global, dan mengembalikannya saat test selesai.
func recordingProvider(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutting down the test provider: %v", err)
		}
	})
	return exporter
}

var traceParentShape = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)

func TestInjectEnvelopeCarriesTheActiveSpan(t *testing.T) {
	recordingProvider(t)

	ctx, span := otel.Tracer("test").Start(context.Background(), "request")
	defer span.End()

	env := &eventsv1.Envelope{EventId: "evt-1"}
	telemetry.InjectEnvelope(ctx, env)

	if got := env.GetTraceParent(); !traceParentShape.MatchString(got) {
		t.Fatalf("trace_parent = %q, want a W3C traceparent", got)
	}
	if got, want := env.GetTraceId(), span.SpanContext().TraceID().String(); got != want {
		t.Errorf("trace_id = %q, want %q", got, want)
	}

	// Yang diekstrak harus menunjuk ke span yang SAMA, sebagai induk jarak
	// jauh - itu yang membuat span konsumen menjadi anaknya.
	extracted := trace.SpanContextFromContext(telemetry.ContextFromEnvelope(context.Background(), env))
	if !extracted.IsValid() {
		t.Fatal("the extracted span context is not valid")
	}
	if extracted.TraceID() != span.SpanContext().TraceID() {
		t.Errorf("extracted trace id = %s, want %s", extracted.TraceID(), span.SpanContext().TraceID())
	}
	if extracted.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("extracted span id = %s, want %s", extracted.SpanID(), span.SpanContext().SpanID())
	}
	if !extracted.IsRemote() {
		t.Error("the extracted span context should be marked remote")
	}
}

func TestInjectEnvelopeLeavesTheEnvelopeAloneWithoutASpan(t *testing.T) {
	env := &eventsv1.Envelope{EventId: "evt-2"}
	telemetry.InjectEnvelope(context.Background(), env)

	if env.TraceParent != nil || env.TraceId != nil {
		t.Errorf("an envelope written outside any span must not carry a trace, got parent=%v id=%v",
			env.TraceParent, env.TraceId)
	}

	// Dan tanpa trace, ctx dikembalikan apa adanya.
	if sc := trace.SpanContextFromContext(telemetry.ContextFromEnvelope(context.Background(), env)); sc.IsValid() {
		t.Errorf("no trace should be extracted from an empty envelope, got %s", sc.TraceID())
	}

	// Nil tidak boleh panic: penulis outbox memvalidasi envelope setelahnya.
	telemetry.InjectEnvelope(context.Background(), nil)
}

func TestStartConsumerSpanIsAChildOfTheEnvelopeTrace(t *testing.T) {
	exporter := recordingProvider(t)

	ctx, producer := otel.Tracer("test").Start(context.Background(), "request")
	env := &eventsv1.Envelope{EventId: "evt-3"}
	telemetry.InjectEnvelope(ctx, env)
	producer.End()

	_, consumer := telemetry.StartConsumerSpan(context.Background(), env,
		"llm.jobs", "llm-worker", "meal_guide_requested")
	telemetry.End(consumer, nil)

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(spans))
	}
	got := spans[1]

	if got.Name != "llm.jobs process" {
		t.Errorf("span name = %q", got.Name)
	}
	if got.SpanKind != trace.SpanKindConsumer {
		t.Errorf("span kind = %s, want consumer", got.SpanKind)
	}
	if got.Parent.SpanID() != producer.SpanContext().SpanID() {
		t.Errorf("parent span = %s, want the producer's %s", got.Parent.SpanID(), producer.SpanContext().SpanID())
	}
	if got.SpanContext.TraceID() != producer.SpanContext().TraceID() {
		t.Errorf("the consumer span is in trace %s, want the producer's %s",
			got.SpanContext.TraceID(), producer.SpanContext().TraceID())
	}
	if got.Status.Code != codes.Unset {
		t.Errorf("status = %s, want unset for a span that ended without error", got.Status.Code)
	}

	want := map[string]string{
		"messaging.destination.name":    "llm.jobs",
		"messaging.consumer.group.name": "llm-worker",
		"selaras.event.type":            "meal_guide_requested",
		"selaras.event.id":              "evt-3",
	}
	for _, attr := range got.Attributes {
		if expected, ok := want[string(attr.Key)]; ok {
			if attr.Value.AsString() != expected {
				t.Errorf("%s = %q, want %q", attr.Key, attr.Value.AsString(), expected)
			}
			delete(want, string(attr.Key))
		}
	}
	for key := range want {
		t.Errorf("attribute %s is missing", key)
	}
}

func TestStartConsumerSpanStartsANewTraceWhenTheEnvelopeHasNone(t *testing.T) {
	exporter := recordingProvider(t)

	_, span := telemetry.StartConsumerSpan(context.Background(),
		&eventsv1.Envelope{EventId: "evt-4"}, "user.deletion", "profile-deletion", "user_deletion_requested")
	telemetry.End(span, nil)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Parent.IsValid() {
		t.Errorf("a span for an untraced event should be a root, got parent %s", spans[0].Parent.SpanID())
	}
}

func TestEndRecordsTheError(t *testing.T) {
	exporter := recordingProvider(t)

	_, span := telemetry.StartSpan(context.Background(), "llm.generate",
		attribute.String("selaras.llm.provider", "fake"))
	telemetry.End(span, errors.New("the provider timed out"))

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	got := spans[0]
	if got.Status.Code != codes.Error {
		t.Errorf("status = %s, want error", got.Status.Code)
	}
	if got.Status.Description != "the provider timed out" {
		t.Errorf("status description = %q", got.Status.Description)
	}
	if len(got.Events) != 1 || got.Events[0].Name != "exception" {
		t.Errorf("expected exactly one exception event, got %+v", got.Events)
	}
}
