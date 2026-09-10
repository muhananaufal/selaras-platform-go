package telemetry

import (
	"context"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
)

// traceParentKey is the W3C field name carried by the Envelope.
const traceParentKey = "traceparent"

// scopeName marks spans created by this package, as opposed to library
// instrumentation.
const scopeName = "github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"

// InjectEnvelope copies the active trace context into the envelope.
//
// This is the only trace bridge across the broker. gRPC and HTTP carry the
// traceparent in their own headers, but events are written to the outbox
// table and published later by a relay that knows nothing about the request
// that produced them. The only thing that can be carried is what sits inside
// the payload itself.
//
// Without an active span, the envelope is left as it is: an event born from
// scheduled work has no parent, and inventing one would produce a misleading
// single-span trace.
func InjectEnvelope(ctx context.Context, env *eventsv1.Envelope) {
	if env == nil {
		return
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return
	}

	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)

	parent := carrier.Get(traceParentKey)
	if parent == "" {
		return
	}
	traceID := sc.TraceID().String()
	env.TraceParent = &parent
	env.TraceId = &traceID
}

// ContextFromEnvelope returns a ctx whose parent span is the trace inside
// the envelope, or ctx as-is when the envelope carries no trace.
func ContextFromEnvelope(ctx context.Context, env *eventsv1.Envelope) context.Context {
	parent := env.GetTraceParent()
	if parent == "" {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx,
		propagation.MapCarrier{traceParentKey: parent})
}

// StartConsumerSpan opens a span for processing one event from the broker.
//
// The span becomes a CHILD of the request that wrote the event, not a new
// trace with a link. OTel messaging semantics suggest links for
// asynchronous processing, but the question to be answered here is "what
// happened to this user's request" - and the answer has to read as one
// trace from the edge to the worker (F9-07), not as two traces someone has
// to match up by hand.
func StartConsumerSpan(
	ctx context.Context, env *eventsv1.Envelope, rec *kgo.Record,
) (context.Context, trace.Span) {
	ctx = ContextFromEnvelope(ctx, env)

	return otel.Tracer(scopeName).Start(ctx, rec.Topic+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.operation.type", "process"),
			attribute.String("messaging.destination.name", rec.Topic),
			attribute.Int("messaging.destination.partition.id", int(rec.Partition)),
			attribute.Int64("messaging.kafka.offset", rec.Offset),
			attribute.String("selaras.event.type", headerOf(rec, "event_type")),
			attribute.String("selaras.event.id", env.GetEventId()),
		))
}

// headerOf reads one record header; empty when absent.
func headerOf(rec *kgo.Record, key string) string {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// StartSpan opens an ordinary internal span - for work that library
// instrumentation does not cover, such as one call to the LLM provider.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(scopeName).Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...))
}

// End closes the span and records the error, if any.
//
// It exists so that every place that closes a span does it the same way. A
// span that ended with an error but carries status OK is a span that will
// never be found when someone searches for failures.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
