package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// spanContext returns a ctx carrying an active span.
func spanContext(t *testing.T) (context.Context, string) {
	t.Helper()

	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(tracetest.NewNoopExporter()))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutting down the test provider: %v", err)
		}
	})

	ctx, span := provider.Tracer("test").Start(context.Background(), "request")
	t.Cleanup(func() { span.End() })
	return ctx, span.SpanContext().TraceID().String()
}

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("the log line is not JSON: %v\n%s", err, line)
	}
	return record
}

func TestWithTraceContextAddsIdsOnlyWhenASpanIsActive(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(&buf, nil)))
	ctx, traceID := spanContext(t)

	log.InfoContext(ctx, "inside the request")
	log.Info("outside any request")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want 2", len(lines))
	}

	inside := decode(t, lines[0])
	if got := inside["trace_id"]; got != traceID {
		t.Errorf("trace_id = %v, want %s", got, traceID)
	}
	if got, ok := inside["span_id"].(string); !ok || len(got) != 16 {
		t.Errorf("span_id = %v, want a 16-hex-digit id", inside["span_id"])
	}

	outside := decode(t, lines[1])
	if _, has := outside["trace_id"]; has {
		t.Errorf("a record written without a span must not carry a trace_id: %s", lines[1])
	}
}

func TestWithTraceContextSurvivesDerivedLoggers(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(&buf, nil)))
	ctx, traceID := spanContext(t)

	// log.With and WithGroup are the forms almost every unit uses; both must
	// keep carrying the trace fields.
	log.With("service", "identity").WithGroup("saga").InfoContext(ctx, "confirmed", "id", "s-1")

	record := decode(t, strings.TrimSpace(buf.String()))
	if got := record["trace_id"]; got != traceID {
		t.Errorf("trace_id = %v after With/WithGroup, want %s", got, traceID)
	}
	if got := record["service"]; got != "identity" {
		t.Errorf("service = %v, the wrapped attributes were lost", got)
	}
}

func TestWithTraceContextHandlesNilAndDoubleWrapping(t *testing.T) {
	if got := telemetry.WithTraceContext(nil); got != nil {
		t.Errorf("wrapping nil should give nil, got %T", got)
	}

	inner := slog.NewJSONHandler(&bytes.Buffer{}, nil)
	once := telemetry.WithTraceContext(inner)
	if twice := telemetry.WithTraceContext(once); twice != once {
		t.Error("wrapping twice should not stack handlers; every record would carry duplicate ids")
	}
}

func TestStartWithoutAnEndpointKeepsTracingOffButPropagating(t *testing.T) {
	t.Setenv(telemetry.EndpointVariable, "")

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	tel, err := telemetry.Start(context.Background(), "test-svc", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	if !strings.Contains(buf.String(), "tracing is off") {
		t.Errorf("the log must say tracing is off, got:\n%s", buf.String())
	}

	// The W3C propagator stays installed, so an incoming traceparent is still
	// forwarded even though this process does not record.
	fields := otel.GetTextMapPropagator().Fields()
	var hasTraceParent bool
	for _, f := range fields {
		if f == "traceparent" {
			hasTraceParent = true
		}
	}
	if !hasTraceParent {
		t.Errorf("the global propagator does not carry traceparent: %v", fields)
	}

	// Metrics still exist without a collector.
	if tel.Meter() == nil || tel.Handler() == nil {
		t.Error("metrics must be available even when tracing is off")
	}
}

func TestStartWithAnEndpointInstallsARecordingProvider(t *testing.T) {
	// A non-existent address: the exporter connects lazily, so Start still
	// succeeds and the failure only appears on export - exactly the behaviour
	// wanted when the collector is not up yet.
	t.Setenv(telemetry.EndpointVariable, "http://127.0.0.1:1")

	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	tel, err := telemetry.Start(context.Background(), "test-svc", log)
	if err != nil {
		t.Fatal(err)
	}

	if _, isSDK := otel.GetTracerProvider().(*sdktrace.TracerProvider); !isSDK {
		t.Errorf("the global tracer provider is %T, want the SDK provider", otel.GetTracerProvider())
	}
	if !strings.Contains(buf.String(), "tracing is on") {
		t.Errorf("the log must say tracing is on, got:\n%s", buf.String())
	}

	// A span created through the global provider must be valid - that is the
	// proof the provider really records rather than being a no-op.
	_, span := otel.Tracer("test").Start(context.Background(), "probe")
	if !span.SpanContext().IsValid() {
		t.Error("spans from the installed provider are not valid; tracing is effectively off")
	}
	span.End()

	// Shutdown tries to export to a non-existent address and is entitled to
	// fail; all that is guarded is that it honours its deadline and does not
	// hang. Every main wraps it in the shutdown grace period.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := tel.Shutdown(ctx); err != nil {
		t.Logf("shutdown reported (expected with no collector): %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("shutdown took %s; it must give up at the deadline", elapsed)
	}
}

// Compilation guarantees the installed propagator is the W3C composite type;
// this test makes sure Baggage is installed too, because cross-unit
// attributes (a user id for logs, say) depend on it.
func TestStartInstallsBaggagePropagation(t *testing.T) {
	t.Setenv(telemetry.EndpointVariable, "")
	tel, err := telemetry.Start(context.Background(), "test-svc", slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	var hasBaggage bool
	for _, f := range otel.GetTextMapPropagator().Fields() {
		if f == (propagation.Baggage{}).Fields()[0] {
			hasBaggage = true
		}
	}
	if !hasBaggage {
		t.Error("baggage propagation is not installed")
	}
}

// Process and runtime metrics MUST exist on every /metrics: FinOps (F9-16)
// and the HPA thresholds (F9-21) are computed from them, and docker stats
// does not exist in the cluster.
func TestMetricsExposeProcessAndRuntimeCollectors(t *testing.T) {
	t.Setenv(telemetry.EndpointVariable, "")
	tel, err := telemetry.Start(context.Background(), "test-svc", slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	rec := httptest.NewRecorder()
	tel.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, name := range []string{"process_cpu_seconds_total", "process_resident_memory_bytes", "go_goroutines"} {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics does not expose %s", name)
		}
	}
}
