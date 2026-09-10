package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// EndpointVariable is the variable that switches trace export on.
//
// The name belongs to the OTel specification, not to this project, and that
// is deliberate: the exporter reads it itself, so there is no translation
// from a project name to a library name that could go wrong.
const EndpointVariable = "OTEL_EXPORTER_OTLP_ENDPOINT"

// Telemetry is the whole instrumentation of one process: metrics, traces,
// and context propagation (F9-05).
type Telemetry struct {
	meters *Meters
	traces *sdktrace.TracerProvider
}

// Start sets up telemetry and installs it as the global providers.
//
// Global, on purpose. Library instrumentation - gRPC, Gin - takes the
// provider from otel.GetTracerProvider() when given nothing, and passing
// the provider explicitly to every place means one missed place produces
// spans that are silently dropped.
//
// Traces are sent ONLY when OTEL_EXPORTER_OTLP_ENDPOINT is set. Without it,
// the process still comes up with a no-op tracer, and that state is
// announced in the log - not hidden behind an empty graph. The W3C
// propagator is installed in both states, so a traceparent arriving from
// outside is still forwarded even when this process itself does not record.
func Start(ctx context.Context, serviceName string, log *slog.Logger) (*Telemetry, error) {
	if log == nil {
		return nil, errors.New("nil logger")
	}

	meters, err := New(serviceName)
	if err != nil {
		return nil, err
	}
	otel.SetMeterProvider(meters.provider)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	// Exporter errors - collector down, queue full - are reported through the
	// process log, not to raw stderr where nobody reads them.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Error("telemetry export failed", "error", err)
	}))

	t := &Telemetry{meters: meters}

	endpoint := os.Getenv(EndpointVariable)
	if endpoint == "" {
		log.Info("tracing is off; spans are not exported", "variable", EndpointVariable)
		return t, nil
	}

	// Address, TLS, and headers are read by the exporter itself from
	// OTEL_EXPORTER_OTLP_*. Nothing is read twice here, so there are not two
	// sources of truth for one address.
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("building the trace exporter: %w", err)
	}

	res, err := describe(serviceName)
	if err != nil {
		return nil, err
	}

	// The sampler is read from OTEL_TRACES_SAMPLER; the default is
	// parentbased_always_on [otel/sdk@v1.46.0/trace/sampler_env.go]. Not
	// overridden here: the sampling ratio is a per-environment decision, not a
	// per-code one.
	t.traces = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(t.traces)

	log.Info("tracing is on", "endpoint", endpoint)
	return t, nil
}

// Meter returns a meter for creating instruments.
func (t *Telemetry) Meter() metric.Meter { return t.meters.Meter() }

// Handler menyajikan metriknya dalam format Prometheus.
func (t *Telemetry) Handler() http.Handler { return t.meters.Handler() }

// Shutdown drains the span queue and then closes both providers.
//
// Traces are closed first: spans not yet exported are lost if the process
// exits, whereas metrics are read by whoever scrapes them and have no queue
// to drain.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	if t.traces != nil {
		if err := t.traces.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutting down the tracer provider: %w", err))
		}
	}
	if err := t.meters.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// describe assembles the resource attached to every span and metric.
//
// The semconv version MUST match the one used by resource.Default()
// [otel/sdk@v1.46.0/resource/builtin.go:16], otherwise Merge refuses with
// "conflicting Schema URL" - and the telemetry silently does not exist.
// This really happened: v1.26.0 vs v1.43.0.
func describe(serviceName string) (*resource.Resource, error) {
	res, err := resource.Merge(resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, fmt.Errorf("describing this service: %w", err)
	}
	return res, nil
}
