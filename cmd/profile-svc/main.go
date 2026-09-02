// Command profile-svc melayani kontrak profile.v1 di atas gRPC.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile"
	profilegrpc "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/grpc"
	profilepg "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/app"
)

const (
	shutdownGrace = 15 * time.Second
	serviceName   = "profile.v1.Profile"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("profile-svc stopped", "error", err)
		os.Exit(1)
	}
	log.Info("profile-svc stopped cleanly")
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := profile.LoadConfig()
	if err != nil {
		return err
	}

	pool, err := pg.Open(ctx, pg.DefaultConfig(cfg.DatabaseDSN))
	if err != nil {
		return err
	}
	defer pool.Close()

	svc, err := app.NewService(profilepg.NewProfileRepository(pool), time.Now)
	if err != nil {
		return err
	}
	server, err := profilegrpc.NewServer(svc)
	if err != nil {
		return err
	}

	probes := httpx.NewHealth()

	grpcServer := grpc.NewServer()
	profilev1.RegisterProfileServer(grpcServer, server)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	reflection.Register(grpcServer)
	healthServer.SetServingStatus(serviceName, healthpb.HealthCheckResponse_SERVING)

	// Siap dinyatakan setelah kolam koneksi terbukti terjangkau, bukan saat
	// prosesnya menyala.
	probes.SetReady(true)

	listener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	httpServer := healthEndpoint(cfg.HealthAddr, probes)
	errs := make(chan error, 2)

	go func() {
		log.Info("serving grpc", "addr", cfg.GRPCAddr)
		errs <- grpcServer.Serve(listener)
	}()
	go func() {
		log.Info("serving health probes", "addr", cfg.HealthAddr)
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	healthServer.SetServingStatus(serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
	probes.SetReady(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("shutting down health endpoint", "error", err)
	}

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		log.Warn("grace period expired; dropping in-flight requests")
		grpcServer.Stop()
	}
	return nil
}

// healthEndpoint menerima probes dari luar, bukan membuatnya sendiri - lihat
// alasannya di cmd/identity-svc: bentuk yang membuatnya sendiri membuat
// SetReady mustahil dipanggil, dan readyz menjawab 503 selamanya.
func healthEndpoint(addr string, probes *httpx.Health) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", probes.Live)
	mux.HandleFunc("GET /readyz", probes.Ready)

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
