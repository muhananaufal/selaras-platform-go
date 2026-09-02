// Command edge-gateway melayani kontrak REST publik dan meneruskannya ke
// service di belakangnya lewat gRPC.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/revocation"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	rd "github.com/muhananaufal/selaras-platform-go/internal/platform/redis"
)

const shutdownGrace = 15 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("edge-gateway stopped", "error", err)
		os.Exit(1)
	}
	log.Info("edge-gateway stopped cleanly")
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := edge.LoadConfig()
	if err != nil {
		return err
	}

	verifier, err := token.NewVerifier(cfg.VerifyKey, cfg.TokenIssuer)
	if err != nil {
		return err
	}

	redisClient, err := rd.Open(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Error("closing redis", "error", err)
		}
	}()

	identityConn, err := dial(cfg.IdentityAddr)
	if err != nil {
		return fmt.Errorf("identity-svc: %w", err)
	}
	defer closeConn(identityConn, "identity-svc", log)

	profileConn, err := dial(cfg.ProfileAddr)
	if err != nil {
		return fmt.Errorf("profile-svc: %w", err)
	}
	defer closeConn(profileConn, "profile-svc", log)

	identityClient := identityv1.NewIdentityClient(identityConn)

	// Sumber kebenaran pencabutan adalah identity-svc, dijangkau lewat gRPC.
	// Ia BUKAN koneksi basis data: isolasi skema-per-service ditegakkan basis
	// datanya sendiri, dan gateway tidak punya hak di skema identity.
	revocations, err := revocation.NewRedisStore(
		redisClient,
		generationOverGRPC{identity: identityClient},
		cfg.RevocationTTL,
	)
	if err != nil {
		return err
	}

	probes := httpx.NewHealth()
	router := edge.NewRouter(edge.Deps{
		Identity:    identityClient,
		Profiles:    profilev1.NewProfileClient(profileConn),
		Tokens:      verifier,
		Revocations: revocations,
		Probes:      probes,
		Now:         time.Now,
	})

	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: router,
		// Batas waktu ditetapkan eksplisit. Server HTTP Go tanpa batas waktu
		// menahan koneksi yang menggantung selamanya, dan itu cara termurah
		// menghabiskan sumber daya sebuah gateway.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	probes.SetReady(true)

	errs := make(chan error, 1)
	go func() {
		log.Info("serving http", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Tidak siap dinyatakan lebih dulu, baru permintaan yang sedang berjalan
	// diberi waktu selesai. Urutannya penting: load balancer berhenti
	// mengirim yang baru sebelum yang lama dihentikan.
	probes.SetReady(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	return server.Shutdown(shutdownCtx)
}

func dial(target string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("creating the client for %s: %w", target, err)
	}
	return conn, nil
}

func closeConn(conn *grpc.ClientConn, name string, log *slog.Logger) {
	if err := conn.Close(); err != nil {
		log.Error("closing connection", "service", name, "error", err)
	}
}

// generationOverGRPC mengambil generasi token yang berlaku dari identity-svc.
//
// Dipanggil HANYA saat cache pencabutan tidak tahu, bukan di setiap request -
// itulah yang membedakan rancangan ini dari token opaque yang ditolak ADR-012.
type generationOverGRPC struct {
	identity identityv1.IdentityClient
}

func (g generationOverGRPC) CurrentGeneration(ctx context.Context, userID domain.UserID) (int64, error) {
	// Batas waktu pendek: pemeriksaan ini duduk di jalur setiap permintaan
	// terautentikasi yang meleset dari cache, dan identity-svc yang lambat
	// tidak boleh menahan seluruh gateway.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	resp, err := g.identity.GetTokenGeneration(ctx, &identityv1.GetTokenGenerationRequest{
		UserId: userID.String(),
	})
	if err != nil {
		return 0, fmt.Errorf("asking identity-svc for the token generation: %w", err)
	}
	return resp.GetGeneration(), nil
}
