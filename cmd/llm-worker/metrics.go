package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/muhananaufal/selaras-platform-go/internal/llmworker"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// startMetrics starts telemetry: the metrics endpoint (F3-15) and traces
// (F9-05).
//
// It returns a stopper and NEVER returns an error: telemetry that fails to set
// up must not kill the worker. A queue nobody is working on is far more
// expensive than an empty graph - and the failure is logged, not hidden.
func startMetrics(
	ctx context.Context, log *slog.Logger,
	consumer *llmworker.Consumer, client *kgo.Client,
) func() {
	addr := os.Getenv("METRICS_ADDR")
	if addr == "" {
		addr = ":9402"
	}

	tel, err := telemetry.Start(ctx, "llm-worker", log)
	if err != nil {
		log.Error("metrics and traces are disabled; telemetry could not be set up", "error", err)
		return func() {}
	}

	metrics, err := llmworker.NewMetrics(tel.Meter())
	if err != nil {
		log.Error("job metrics are disabled", "error", err)
	} else {
		consumer.WithMetrics(metrics)
	}

	if _, err := llmworker.NewLagReporter(tel.Meter(), client, ConsumerGroup); err != nil {
		// Lag is the metric most often asked about when something is wrong, so
		// losing it is mentioned separately - not lumped in with the rest.
		log.Error("consumer lag will not be reported", "error", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", tel.Handler())
	ready := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			// A client leaving in the middle of an answer is not breakage, but a
			// probe that always disconnects is a symptom - so it is logged.
			log.Warn("writing the health response", "error", err)
		}
	}
	mux.HandleFunc("/healthz", ready)
	// /readyz is the same as /healthz: startMetrics is called after the broker
	// is pinged and the consumer is assembled, so the existence of this
	// endpoint means the worker is ready to consume. The chart (F9-03) checks
	// /readyz on every unit; without this, llm-worker's startup probe fails
	// with 404 and KEDA wakes pods that are never declared alive - that
	// happened on k3d.
	mux.HandleFunc("/readyz", ready)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("serving metrics", "addr", addr)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Error("the metrics endpoint stopped", "error", err)
		}
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error("shutting down the metrics endpoint", "error", err)
		}
		if err := tel.Shutdown(shutdownCtx); err != nil {
			log.Error("shutting down telemetry", "error", err)
		}
	}
}
