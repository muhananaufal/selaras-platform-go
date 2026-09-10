// Command dashboard-svc serves the dashboard.v1 contract over gRPC.
package main

import (
	"context"
	"errors"
	"fmt"
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

	dashboardv1 "github.com/muhananaufal/selaras-platform-go/gen/dashboard/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard"

	dashboardgrpc "github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/grpc"
	dashboardpg "github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

const (
	shutdownGrace = 15 * time.Second
	serviceName   = "dashboard.v1.Dashboard"
)

func main() {
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("dashboard-svc stopped", "error", err)
		os.Exit(1)
	}
	log.Info("dashboard-svc stopped cleanly")
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := dashboard.LoadConfig()
	if err != nil {
		return err
	}

	// Telemetry is started before any other dependency is opened, so even the
	// first connection is recorded. Its failure stops the start: a process
	// that cannot be observed is more dangerous than a process that does not
	// start, because the latter is visible.
	tel, err := telemetry.Start(ctx, "dashboard-svc", log)
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

	// The read replica (F9-32), if configured. Its pool is opened and pinged
	// like the primary: a replica unreachable at start had better refuse to
	// start than silently serve from the primary.
	reader := pg.Querier(pool)
	if cfg.ReadDSN != "" {
		readPool, err := pg.Open(ctx, pg.DefaultConfig(cfg.ReadDSN))
		if err != nil {
			return fmt.Errorf("connecting to the read replica: %w", err)
		}
		defer readPool.Close()
		reader = readPool
		log.Info("dashboard reads are served from a replica")
	}

	uow, err := dashboardpg.NewUnitOfWork(pool)
	if err != nil {
		return err
	}

	svc, err := app.NewService(
		dashboardpg.NewRepositoryWithReader(pool, reader),
		dashboardpg.NewStateRepository(pool),
		uow, time.Now)
	if err != nil {
		return err
	}

	// The outbox relay is REQUIRED, even though the dashboard owns not a
	// single fact.
	//
	// The first version skipped it on the grounds that "this service only
	// reads". That was wrong: the account deletion saga demands that every
	// unit confirm once its data is gone, and that confirmation is an event.
	// Without a relay, the confirmation is written to the outbox and then
	// never leaves - and the saga hangs forever waiting for a unit that has
	// actually finished.
	stopRelay, err := startRelay(ctx, log, pool, cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer stopRelay()

	stopProjector, err := startProjector(ctx, log, svc, cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer stopProjector()

	stopDeletion, err := startDeletionConsumer(ctx, log, pool, cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer stopDeletion()

	server, err := dashboardgrpc.NewServer(svc, time.Now)
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
	dashboardv1.RegisterDashboardServer(grpcServer, server)

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
