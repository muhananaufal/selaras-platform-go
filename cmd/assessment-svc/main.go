// Command assessment-svc serves the assessment.v1 contract over gRPC.
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
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/cache"
	assessmentgrpc "github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/grpc"
	assessmentpg "github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/profileclient"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/rpc"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

const (
	shutdownGrace = 15 * time.Second
	serviceName   = "assessment.v1.Assessment"
)

func main() {
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("assessment-svc stopped", "error", err)
		os.Exit(1)
	}
	log.Info("assessment-svc stopped cleanly")
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := assessment.LoadConfig()
	if err != nil {
		return err
	}

	// Telemetry is started before any other dependency is opened, so even the
	// first connection is recorded. Its failure stops the start: a process
	// that cannot be observed is more dangerous than a process that does not
	// start, because the latter is visible.
	tel, err := telemetry.Start(ctx, "assessment-svc", log)
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

	// The clinical constants are loaded BEFORE anything is opened.
	//
	// Incomplete constants mean this service would compute wrong numbers, not
	// fail - so its failure has to happen here, while nobody can be harmed
	// yet.
	constants, err := score.Load()
	if err != nil {
		return fmt.Errorf("loading the clinical constants: %w", err)
	}
	log.Info("clinical constants loaded",
		"score_models_sha256", constants.ModelsSHA256,
		"region_mapping_sha256", constants.RegionsSHA256)

	pool, err := pg.Open(ctx, pg.DefaultConfig(cfg.DatabaseDSN))
	if err != nil {
		return err
	}
	defer pool.Close()

	profileConn, err := grpc.NewClient(cfg.ProfileAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		telemetry.GRPCDialOption(),
		// The user's token is passed downstream (ADR-026).
		grpc.WithChainUnaryInterceptor(authn.UnaryClientInterceptor()),
		// A per-call deadline (chaos F9-13): without it, a service that has just
		// died makes its callers hang instead of fail.
		rpc.WithUpstreamDeadline(rpc.DefaultUpstreamTimeout))
	if err != nil {
		return fmt.Errorf("creating the profile-svc client: %w", err)
	}
	defer func() {
		if err := profileConn.Close(); err != nil {
			log.Error("closing the profile-svc connection", "error", err)
		}
	}()

	profiles, err := profileclient.New(profileConn)
	if err != nil {
		return err
	}

	// The profile cache in front of its gRPC client (F2-16, ADR-007). The
	// order must not be reversed: the cache first, the gRPC call only as a
	// safety net.
	cachedProfiles, err := cache.NewSource(pool, profiles, log)
	if err != nil {
		return err
	}

	svc, err := app.NewService(
		assessmentpg.NewRepository(pool),
		cachedProfiles,
		score.NewEngine(constants),
		time.Now,
	)
	if err != nil {
		return err
	}
	// The outbox writer is built FROM the transaction the unit of work
	// provides, not from the connection pool. The latter would commit on its
	// own, and its event would survive even when the business change was
	// rolled back.
	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	statuses := func(q pg.Querier) app.StatusWriter { return assessmentpg.NewRepository(q) }
	svc = svc.WithStatusWriter(statuses)

	// A transactional repository: the assessment and its announcement event are
	// written in one transaction, so the dashboard never misses an assessment
	// that was stored (E10).
	svc = svc.WithRepositoryFor(func(q pg.Querier) domain.Repository {
		return assessmentpg.NewRepository(q)
	})

	stopDeletion, err := startDeletionConsumer(ctx, log, pool, os.Getenv("KAFKA_BROKERS"))
	if err != nil {
		return err
	}
	defer stopDeletion()

	server, err := assessmentgrpc.NewServer(svc, constants,
		assessmentpg.NewUnitOfWork(pool), events)
	if err != nil {
		return err
	}

	// Kafka follows IF configured. Without KAFKA_BROKERS, this service still
	// serves reads and computations - what must not happen is accepting a
	// personalisation request that will never leave the outbox, and that is
	// refused in RequestPersonalization.
	stopKafka, err := startEventing(ctx, log, pool, svc, statuses, events)
	if err != nil {
		return err
	}
	defer stopKafka()

	probes := httpx.NewHealth()

	// Every user-scoped RPC has to carry a token whose sub matches the
	// request's user_id (ADR-026); the public key comes from JWT_VERIFY_KEY.
	verifier, err := authn.VerifierFromEnv()
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(telemetry.GRPCServerOption(),
		grpc.ChainUnaryInterceptor(authn.UnaryServerInterceptor(verifier)))
	assessmentv1.RegisterAssessmentServer(grpcServer, server)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	reflection.Register(grpcServer)
	healthServer.SetServingStatus(serviceName, healthpb.HealthCheckResponse_SERVING)
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
