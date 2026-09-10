// Command profile-svc serves the profile.v1 contract over gRPC.
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
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
	"github.com/muhananaufal/selaras-platform-go/internal/profile"
	profilegrpc "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/grpc"
	profilepg "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/app"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

const (
	shutdownGrace = 15 * time.Second
	serviceName   = "profile.v1.Profile"
)

func main() {
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, nil)))
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

	// Telemetry is started before any other dependency is opened, so even the
	// first connection is recorded. Its failure stops the start: a process
	// that cannot be observed is more dangerous than a process that does not
	// start, because the latter is visible.
	tel, err := telemetry.Start(ctx, "profile-svc", log)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := tel.Shutdown(flushCtx); err != nil {
			log.Error("shutting down telemetry", "error", err)
		}
	}()

	pool, err := pg.Open(ctx, pg.DefaultConfig(cfg.DatabaseDSN))
	if err != nil {
		return err
	}
	defer pool.Close()

	svc, err := app.NewService(profilepg.NewProfileRepository(pool), time.Now)
	if err != nil {
		return err
	}
	// Event publishing is installed when the broker is present. All three at
	// once or not at all: partially installed would mean profile changes
	// stored without being announced, and the cache on the other side going
	// stale without anyone knowing.
	svc = svc.WithEvents(
		profilepg.NewUnitOfWork(pool),
		func(q pg.Querier) domain.ProfileRepository { return profilepg.NewProfileRepository(q) },
		func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) },
	)

	stopRelay, err := startRelay(ctx, log, pool)
	if err != nil {
		return err
	}
	defer stopRelay()

	log.Info("profile events", "published", svc.PublishesEvents())

	stopDeletion, err := startDeletionConsumer(ctx, log, pool, os.Getenv("KAFKA_BROKERS"))
	if err != nil {
		return err
	}
	defer stopDeletion()

	server, err := profilegrpc.NewServer(svc)
	if err != nil {
		return err
	}

	probes := httpx.NewHealth()

	// Every user-scoped RPC has to carry a token whose sub matches the
	// request's user_id (ADR-026); the public key comes from JWT_VERIFY_KEY.
	verifier, err := authn.VerifierFromEnv()
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(telemetry.GRPCServerOption(),
		grpc.ChainUnaryInterceptor(authn.UnaryServerInterceptor(verifier)))
	profilev1.RegisterProfileServer(grpcServer, server)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	reflection.Register(grpcServer)
	healthServer.SetServingStatus(serviceName, healthpb.HealthCheckResponse_SERVING)

	// Ready is declared once the connection pool has proven reachable, not
	// when the process starts.
	probes.SetReady(true)

	listener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	httpServer := healthEndpoint(cfg.HealthAddr, probes, tel.Handler())
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

// healthEndpoint receives probes from outside rather than creating them
// itself - see the reason in cmd/identity-svc: the shape that creates them
// itself makes SetReady impossible to call, and readyz answers 503 forever.
func healthEndpoint(addr string, probes *httpx.Health, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", probes.Live)
	mux.HandleFunc("GET /readyz", probes.Ready)

	// Metrics are served on the probe port, not the gRPC port: neither is for
	// users, and Prometheus already knows this address.
	mux.Handle("GET /metrics", metrics)

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
