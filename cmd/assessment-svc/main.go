// Command assessment-svc melayani kontrak assessment.v1 di atas gRPC.
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
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

const (
	shutdownGrace = 15 * time.Second
	serviceName   = "assessment.v1.Assessment"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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

	// Konstanta klinis dimuat SEBELUM apa pun dibuka.
	//
	// Konstanta yang tidak lengkap berarti service ini akan menghitung angka
	// yang salah, bukan gagal - jadi kegagalannya harus terjadi di sini,
	// saat belum ada yang bisa dirugikan.
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
		grpc.WithTransportCredentials(insecure.NewCredentials()))
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

	// Cache profil di depan klien gRPC-nya (F2-16, ADR-007). Urutannya tidak
	// boleh dibalik: cache lebih dulu, panggilan gRPC hanya sebagai jaring.
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
	// Penulis outbox dibangun DARI transaksi yang diberikan unit of work,
	// bukan dari kolam koneksi. Yang kedua akan commit sendiri, dan eventnya
	// bertahan meski perubahan bisnisnya batal.
	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	statuses := func(q pg.Querier) app.StatusWriter { return assessmentpg.NewRepository(q) }
	svc = svc.WithStatusWriter(statuses)

	// Repository transaksional: penilaian dan event pengumumannya ditulis dalam
	// satu transaksi, sehingga dasbor tidak pernah melewatkan penilaian yang
	// sudah tersimpan (E10).
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

	// Kafka menyusul BILA dikonfigurasi. Tanpa KAFKA_BROKERS, service ini
	// tetap melayani pembacaan dan perhitungan - yang tidak boleh terjadi
	// adalah menerima permintaan personalisasi yang tidak akan pernah keluar
	// dari outbox, dan itu ditolak di RequestPersonalization.
	stopKafka, err := startEventing(ctx, log, pool, svc, statuses, events)
	if err != nil {
		return err
	}
	defer stopKafka()

	probes := httpx.NewHealth()

	grpcServer := grpc.NewServer()
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
