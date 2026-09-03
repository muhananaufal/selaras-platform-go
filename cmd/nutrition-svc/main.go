// Command nutrition-svc melayani kontrak nutrition.v1 di atas gRPC.
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

	// Basis distroless/static tidak memuat basis data zona waktu, sehingga
	// LoadLocation di sana selalu gagal. Ia di-embed ke dalam binernya: satu
	// berkas yang ikut, ditukar dengan waktu makan yang benar.
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
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

const (
	shutdownGrace = 15 * time.Second
	serviceName   = "nutrition.v1.Nutrition"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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

	pool, err := pg.Open(ctx, pg.DefaultConfig(cfg.DatabaseDSN))
	if err != nil {
		return err
	}
	defer pool.Close()

	// Penulis event dibangun DARI transaksi yang diberikan unit of work, bukan
	// dari kolam koneksi. Yang kedua akan commit sendiri, dan eventnya bertahan
	// meski perubahan yang memicunya batal.
	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	uow, err := nutritionpg.NewUnitOfWork(pool, events)
	if err != nil {
		return err
	}

	// Jam pengguna, bukan jam server.
	//
	// Container berjalan di UTC. Menghitung waktu makan di sana membuat pukul
	// 13.00 WIB tercatat sebagai sarapan - meleset tujuh jam dari maksud aturan
	// D10. Zonanya dimuat SEKARANG dan kegagalannya menghentikan start-up: nama
	// zona yang salah ketik akan jatuh ke UTC diam-diam, dan setiap panduan
	// sesudahnya salah tanpa satu pun galat yang terlihat.
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("loading timezone %q: %w", cfg.Timezone, err)
	}
	log.Info("meal times will be computed in this zone", "timezone", location.String())

	clock := func() time.Time { return time.Now().In(location) }

	// Bahasa dibaca dari cache yang diisi event profile.updated, bukan dengan
	// memanggil profile-svc pada setiap permintaan (ADR-007). Membuat panduan
	// menu tidak boleh mati hanya karena profile-svc mati.
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

	server, err := nutritiongrpc.NewServer(svc)
	if err != nil {
		return err
	}

	probes := httpx.NewHealth()

	grpcServer := grpc.NewServer()
	nutritionv1.RegisterNutritionServer(grpcServer, server)

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
