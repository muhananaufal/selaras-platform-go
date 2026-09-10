// Package telemetry provides metrics, traces, and logs that are linked to
// each other.
//
// Three things are guarded here:
//
//   - Metrics are served in Prometheus format from every process, so they
//     can be read with curl without any infrastructure at all.
//   - Traces are sent over OTLP ONLY when a collector address is configured,
//     and their context crosses the broker through the Envelope (see
//     propagate.go) - not just gRPC and HTTP.
//   - Logs written with a context carry trace_id and span_id (see
//     logging.go), so one log line can be followed to its trace.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Meters is the instrument factory together with the handler that serves
// them.
type Meters struct {
	provider *sdkmetric.MeterProvider
	registry *prometheus.Registry
	meter    metric.Meter
}

// New sets up a meter exported in Prometheus format.
//
// Prometheus, not OTLP, deliberately for now: OTLP needs a running collector,
// and a metric that cannot be read without extra infrastructure helps nobody
// when something breaks at three in the morning. An endpoint that can be curled
// can be read by anyone.
func New(serviceName string) (*Meters, error) {
	if serviceName == "" {
		return nil, errors.New("a service without a name produces metrics nobody can attribute")
	}

	// Its own registry, not prometheus.DefaultRegisterer: the global registry
	// carries metrics from whatever library happens to be linked in, and name
	// collisions only surface when the process fails to start.
	registry := prometheus.NewRegistry()

	// Go process and runtime metrics are served too: CPU seconds, RSS,
	// goroutines, and GC. Without them, "what do a thousand requests cost"
	// (F9-16) and "how many requests does the HPA need" (F9-21) could only be
	// answered from docker stats - which does not exist in the cluster.
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("building the prometheus exporter: %w", err)
	}

	res, err := describe(serviceName)
	if err != nil {
		return nil, err
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
		sdkmetric.WithResource(res),
	)

	return &Meters{
		provider: provider,
		registry: registry,
		meter:    provider.Meter(serviceName),
	}, nil
}

// Meter returns a meter for creating instruments.
func (m *Meters) Meter() metric.Meter { return m.meter }

// Handler serves the metrics.
func (m *Meters) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		// An error while collecting metrics is reported as a 500, not hidden
		// behind a response that looks fine. A wrong metric is more dangerous
		// than a missing one: the latter is visible.
		ErrorHandling: promhttp.HTTPErrorOnError,
	})
}

// Shutdown closes the metrics provider.
//
// It returns the error instead of swallowing it: a failed shutdown means the
// last reading may not have made it out, and that needs to show up in the
// shutdown log - not vanish.
func (m *Meters) Shutdown(ctx context.Context) error {
	if err := m.provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutting down the meter provider: %w", err)
	}
	return nil
}
