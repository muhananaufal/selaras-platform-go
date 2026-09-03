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

	goredis "github.com/redis/go-redis/v9"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	chatv1 "github.com/muhananaufal/selaras-platform-go/gen/chat/v1"
	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/handler"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/oauth"
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

	var (
		assessmentHandler *handler.Assessment
		regions           assessmentv1.AssessmentClient
	)
	if cfg.AssessmentAddr != "" {
		conn, err := dial(cfg.AssessmentAddr)
		if err != nil {
			return fmt.Errorf("assessment-svc: %w", err)
		}
		defer closeConn(conn, "assessment-svc", log)

		regions = assessmentv1.NewAssessmentClient(conn)
		assessmentHandler = handler.NewAssessment(regions)
	} else {
		// Tanpa assessment-svc, rute penilaian tidak dipasang dan risk_region
		// dikirim null. Keduanya jujur: yang pertama 404, yang kedua nilai
		// yang memang belum bisa dihitung.
		log.Warn("assessment-svc is not configured; its routes are not mounted",
			"variable", "ASSESSMENT_GRPC_TARGET")
	}

	var coachingHandler *handler.Coaching
	if cfg.CoachingAddr != "" {
		conn, err := dial(cfg.CoachingAddr)
		if err != nil {
			return fmt.Errorf("coaching-svc: %w", err)
		}
		defer closeConn(conn, "coaching-svc", log)

		coachingHandler = handler.NewCoaching(coachingv1.NewCoachingClient(conn))
	} else {
		// Tanpa coaching-svc, rutenya tidak dipasang. 404 jauh lebih jujur
		// daripada 500 dari klien yang tidak menyambung ke mana-mana.
		log.Warn("coaching-svc is not configured; its routes are not mounted",
			"variable", "COACHING_GRPC_TARGET")
	}

	var chatHandler *handler.Chat
	if cfg.ChatAddr != "" {
		conn, err := dial(cfg.ChatAddr)
		if err != nil {
			return fmt.Errorf("chat-svc: %w", err)
		}
		defer closeConn(conn, "chat-svc", log)

		chatHandler = handler.NewChat(chatv1.NewChatClient(conn))
	} else {
		log.Warn("chat-svc is not configured; its routes are not mounted",
			"variable", "CHAT_GRPC_TARGET")
	}

	socialHandler, err := buildSocial(cfg, identityClient, redisClient, log)

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
		Social:      socialHandler,
		Assessments: assessmentHandler,
		Coaching:    coachingHandler,
		Chat:        chatHandler,
		Regions:     regions,
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

// buildSocial merakit alur masuk sosial, atau mengembalikan nil bila
// lingkungan ini tidak dikonfigurasi untuknya.
//
// nil berarti rutenya tidak dipasang sama sekali, sehingga jawabannya 404 -
// bukan endpoint yang ada tetapi selalu gagal. Konfigurasi yang terisi
// SEBAGIAN adalah kekeliruan, bukan mode penyebaran, dan karena itu
// menggagalkan start-up: client id tanpa secret akan menyalakan rutenya lalu
// gagal di pertukaran, jauh setelah orang yang salah mengetiknya pergi.
func buildSocial(
	cfg edge.Config,
	identity identityv1.IdentityClient,
	redisClient *goredis.Client,
	log *slog.Logger,
) (*handler.Social, error) {
	social := cfg.Social

	if !social.Configured() {
		if missing := social.Missing(); len(missing) < 4 {
			return nil, fmt.Errorf("social sign-in is partly configured; missing: %v", missing)
		}
		log.Warn("social sign-in is not configured; its routes are not mounted")
		return nil, nil //nolint:nilnil // nil di sini berarti "tidak dipasang", dan itu keadaan yang sah
	}

	store, err := oauth.NewStore(redisClient, 10*time.Minute, time.Minute)
	if err != nil {
		return nil, err
	}

	google, err := oauth.NewGoogle(oauth.GoogleConfig{
		ClientID:     social.GoogleClientID,
		ClientSecret: social.GoogleClientSecret,
		RedirectURL:  social.GoogleRedirectURL,
	})
	if err != nil {
		return nil, err
	}

	log.Info("social sign-in is configured", "provider", "google")
	return handler.NewSocial(
		identity,
		map[string]handler.ProviderClient{"google": google},
		store,
		social.FrontendURL,
	), nil
}
