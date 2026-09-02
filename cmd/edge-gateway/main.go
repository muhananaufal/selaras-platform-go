// Command edge-gateway adalah satu-satunya unit yang menghadap publik.
// Pada tahap ini ia baru menyediakan probe; rute REST menyusul di F1.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("shutting down after failure", "error", err)
		os.Exit(1)
	}
}

func run() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	health := httpx.NewHealth()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Live)
	mux.HandleFunc("GET /readyz", health.Ready)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Sinyal ditangkap sebelum server menyala, supaya SIGTERM yang datang
	// saat masih menyiapkan dependensi tidak terlewat.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Belum ada dependensi yang perlu ditunggu. Begitu ada, SetReady
	// dipanggil setelah semuanya terhubung, bukan di sini.
	health.SetReady(true)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("signal received, draining")
	}

	// Beri waktu request yang sedang berjalan untuk selesai. Tanpa ini,
	// setiap deploy memutus koneksi yang sedang dilayani.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	slog.Info("stopped cleanly")
	return nil
}
