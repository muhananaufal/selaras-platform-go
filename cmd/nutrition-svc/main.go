// Command nutrition-svc serves the nutrition.v1 contract over gRPC.
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

	// The distroless/static base carries no time zone database, so
	// LoadLocation always fails there. It is embedded into the binary: one
	// file that comes along, in exchange for correct meal times.
	_ "time/tzdata"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/cache"
	nutritiongrpc "github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/grpc"
	nutritionpg "github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

const (
	shutdownGrace = 15 * time.Second
	serviceName   = "nutrition.v1.Nutrition"
)

func main() {
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("nutrition-svc stopped", "error", err)
		os.Exit(1)
	}
	log.Info("nutrition-svc stopped cleanly")
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := nutrition.LoadConfig()
	if err != nil {
		return err
	}

	// Telemetry is started before any other dependency is opened, so even the
	// first connection is recorded. Its failure stops the start: a process
	// that cannot be observed is more dangerous than a process that does not
	// start, because the latter is visible.
	tel, err := telemetry.Start(ctx, "nutrition-svc", log)
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

	// The event writer is built FROM the transaction the unit of work provides,
	// not from the connection pool. The latter would commit on its own, and its
	// event would survive even when the change that triggered it was rolled
	// back.
	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	uow, err := nutritionpg.NewUnitOfWork(pool, events)
	if err != nil {
		return err
	}

	// The users' clock, not the server's.
	//
	// Containers run in UTC. Computing the meal time there records 13:00 WIB as
	// breakfast - seven hours off from what rule D10 means. The zone is loaded
	// NOW and its failure stops start-up: a mistyped zone name would silently
	// fall back to UTC, and every guide afterwards would be wrong without a
	// single visible error.
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("loading timezone %q: %w", cfg.Timezone, err)
	}
	log.Info("meal times will be computed in this zone", "timezone", location.String())

	clock := func() time.Time { return time.Now().In(location) }

	// The language is read from the cache filled by profile.updated events,
	// not by calling profile-svc on every request (ADR-007). Producing a menu
	// guide must not die just because profile-svc is down.
	svc, err := app.NewService(
		nutritionpg.NewPreferencesRepository(pool),
		nutritionpg.NewGuideRepository(pool),
		cache.NewLanguages(pool),
		uow, clock)
	if err != nil {
		return err
	}

	stopRelay, err := startRelay(ctx, log, pool, cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer stopRelay()

	stopResults, err := startResultConsumer(ctx, log, svc, pool, cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer stopResults()

	stopDeletion, err := startDeletionConsumer(ctx, log, pool, cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer stopDeletion()

	server, err := nutritiongrpc.NewServer(svc)
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
	nutritionv1.RegisterNutritionServer(grpcServer, server)

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
