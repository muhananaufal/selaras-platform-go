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

// startMetrics menyalakan endpoint metrik (F3-15).
//
// Ia mengembalikan penghenti dan TIDAK pernah mengembalikan galat: telemetri
// yang gagal disiapkan tidak boleh mematikan worker. Antrean yang tidak ada
// yang mengerjakan jauh lebih mahal daripada grafik yang kosong - dan
// kegagalannya dicatat, bukan disembunyikan.
func startMetrics(
	ctx context.Context, log *slog.Logger,
	consumer *llmworker.Consumer, client *kgo.Client,
) func() {
	addr := os.Getenv("METRICS_ADDR")
	if addr == "" {
		addr = ":9402"
	}

	meters, err := telemetry.New("llm-worker")
	if err != nil {
		log.Error("metrics are disabled; telemetry could not be set up", "error", err)
		return func() {}
	}

	metrics, err := llmworker.NewMetrics(meters.Meter())
	if err != nil {
		log.Error("job metrics are disabled", "error", err)
	} else {
		consumer.WithMetrics(metrics)
	}

	if _, err := llmworker.NewLagReporter(meters.Meter(), client, ConsumerGroup); err != nil {
		// Lag adalah metrik yang paling sering ditanya saat ada masalah, jadi
		// kehilangannya disebutkan terpisah - bukan digabung dengan yang lain.
		log.Error("consumer lag will not be reported", "error", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", meters.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			// Klien yang pergi di tengah jawaban bukan kerusakan, tetapi
			// probe yang selalu putus adalah gejala - jadi ia dicatat.
			log.Warn("writing the health response", "error", err)
		}
	})

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
		if err := meters.Shutdown(shutdownCtx); err != nil {
			log.Error("shutting down telemetry", "error", err)
		}
	}
}
