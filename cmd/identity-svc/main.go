// Command identity-svc melayani kontrak identity.v1 di atas gRPC.
package main

import (
	"context"
	"crypto/ed25519"
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

	"github.com/jackc/pgx/v5/pgxpool"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/crypto"
	identitygrpc "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/grpc"
	identitymail "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/mail"
	identitypg "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/profileclient"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/revocation"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/social"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/mail"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	rd "github.com/muhananaufal/selaras-platform-go/internal/platform/redis"
)

// shutdownGrace membatasi berapa lama permintaan yang sedang berjalan boleh
// diselesaikan setelah sinyal berhenti diterima.
const shutdownGrace = 15 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("identity-svc stopped", "error", err)
		os.Exit(1)
	}
	log.Info("identity-svc stopped cleanly")
}

func run(log *slog.Logger) error {
	// Sinyal ditangkap SEBELUM apa pun dibuka, supaya Ctrl-C selama start-up
	// yang lambat tetap ditangani alih-alih membunuh proses di tengah
	// pembukaan koneksi.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := identity.LoadConfig()
	if err != nil {
		return err
	}

	pool, err := pg.Open(ctx, pg.DefaultConfig(cfg.DatabaseDSN))
	if err != nil {
		return err
	}
	defer pool.Close()

	redisClient, err := rd.Open(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Error("closing redis", "error", err)
		}
	}()

	issuer, err := token.NewIssuer(cfg.SigningKey, cfg.TokenIssuer, cfg.AccessTTL)
	if err != nil {
		return err
	}

	// Kunci publik diturunkan dari kunci privat yang sama, jadi identity-svc
	// bisa memverifikasi tokennya sendiri tanpa konfigurasi tambahan - dan
	// tanpa mempercayai siapa pun soal siapa pemilik token yang dikirim.
	publicKey, ok := cfg.SigningKey.Public().(ed25519.PublicKey)
	if !ok {
		// Tidak bisa terjadi dari konfigurasi yang lolos LoadConfig, tetapi
		// type assertion yang gagal tanpa diperiksa akan panik - dan panik
		// saat start-up jauh lebih sulit dibaca daripada satu kalimat.
		return errors.New("the signing key did not yield an ed25519 public key")
	}
	verifier, err := token.NewVerifier(publicKey, cfg.TokenIssuer)
	if err != nil {
		return err
	}

	revocations, err := revocation.NewRedisStore(
		redisClient,
		localGenerationSource{users: identitypg.NewUserRepository(pool)},
		cfg.RevocationTTL,
	)
	if err != nil {
		return err
	}

	profiles, closeProfiles, err := dialProfiles(cfg.ProfileAddr, log)
	if err != nil {
		return err
	}
	defer closeProfiles()

	server, deleteAccount, err := buildServer(cfg, pool, issuer, verifier, revocations, profiles, log)
	if err != nil {
		return err
	}

	// Relay outbox dan konsumen konfirmasi: yang satu mengeluarkan permintaan
	// penghapusan, yang lain menerima jawabannya. Keduanya bisa gagal
	// sendiri-sendiri, jadi keduanya dinyalakan terpisah.
	stopRelay, err := startRelay(ctx, log, pool, os.Getenv("KAFKA_BROKERS"))
	if err != nil {
		return err
	}
	defer stopRelay()

	stopConfirmations, err := startConfirmationConsumer(
		ctx, log, deleteAccount, os.Getenv("KAFKA_BROKERS"))
	if err != nil {
		return err
	}
	defer stopConfirmations()

	// Saga yang menggantung dari proses sebelumnya tidak akan menyelesaikan
	// dirinya sendiri: unitnya sudah dihubungi, dan yang belum menjawab tidak
	// akan ditanya lagi. Satu-satunya cara ia terlihat adalah kalau seseorang
	// diberi tahu saat start-up.
	deleteAccount.LogOutstandingSagas(ctx, log)

	probes := httpx.NewHealth()

	grpcServer := grpc.NewServer()
	identityv1.RegisterIdentityServer(grpcServer, server)

	// Health check dan reflection keduanya dinyalakan. Reflection membuat
	// grpcurl bisa dipakai tanpa membawa berkas proto - itu satu-satunya cara
	// memeriksa service ini dari luar tanpa menulis klien lebih dulu.
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	reflection.Register(grpcServer)
	healthServer.SetServingStatus("identity.v1.Identity", healthpb.HealthCheckResponse_SERVING)

	// Siap dinyatakan setelah seluruh dependensi terbuka dan terbukti
	// terjangkau - kolam Postgres dan Redis keduanya sudah di-ping di atas.
	// Menyatakannya lebih awal berarti trafik datang sebelum ada yang bisa
	// melayaninya.
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

	// Kubernetes menandai pod tidak siap lebih dulu, tetapi sinyal bisa
	// sampai sebelum load balancer sempat berhenti mengirim. Statusnya
	// diturunkan di sini juga supaya probe yang datang saat itu jujur.
	healthServer.SetServingStatus("identity.v1.Identity", healthpb.HealthCheckResponse_NOT_SERVING)
	probes.SetReady(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("shutting down health endpoint", "error", err)
	}

	// GracefulStop menunggu permintaan yang sedang berjalan selesai. Ia
	// dibatasi waktu: satu permintaan yang menggantung tidak boleh menahan
	// pod selamanya dan menghabiskan grace period milik orkestratornya.
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

func buildServer(
	cfg identity.Config,
	pool *pgxpool.Pool,
	issuer *token.Issuer,
	verifier *token.Verifier,
	revocations domain.RevocationPublisher,
	profiles profileClient,
	log *slog.Logger,
) (*identitygrpc.Server, *app.DeleteAccount, error) {
	uow := identitypg.NewUnitOfWork(pool)
	users := identitypg.NewUserRepository(pool)
	sagas := identitypg.NewSagaRepository(pool)
	hasher := crypto.NewArgon2idHasher(crypto.DefaultParams())
	now := time.Now

	links, err := buildResetLinkSender(cfg.Mail, log)
	if err != nil {
		return nil, nil, err
	}

	// Verifier sosial dipilih di sini, sekali, berdasarkan konfigurasi.
	// Lingkungan tanpa kredensial penyedia tetap menyala dengan satu jalur
	// masuk; yang DILARANG hanya berpura-pura berhasil.
	var socialVerifier identitygrpc.SocialIdentityVerifier = social.Unconfigured{}
	if cfg.GoogleClientID == "" {
		log.Warn("social sign-in is not configured; GOOGLE_CLIENT_ID is unset")
	} else {
		google, err := social.NewGoogleVerifier(cfg.GoogleClientID, "", nil, time.Hour)
		if err != nil {
			return nil, nil, err
		}
		socialVerifier = google
		log.Info("social sign-in is configured", "provider", "google")
	}

	register, err := app.NewRegister(uow, hasher, issuer, profiles, now)
	if err != nil {
		return nil, nil, err
	}
	login, err := app.NewLogin(uow, hasher, issuer, profiles, revocations, now)
	if err != nil {
		return nil, nil, err
	}
	logout, err := app.NewLogout(uow, revocations, now)
	if err != nil {
		return nil, nil, err
	}
	requestReset, err := app.NewRequestPasswordReset(uow, links, now)
	if err != nil {
		return nil, nil, err
	}
	confirmReset, err := app.NewConfirmPasswordReset(uow, hasher, revocations, now)
	if err != nil {
		return nil, nil, err
	}
	exchange, err := app.NewExchangeSocialToken(uow, issuer, profiles, revocations, now)
	if err != nil {
		return nil, nil, err
	}

	// Penghapusan akun butuh pembanding kata sandi DAN penyimpanan saga.
	//
	// Ia dikembalikan terpisah karena konsumen konfirmasi memakainya juga, dan
	// keduanya harus use case yang SAMA - aturan penutupan saga hanya boleh
	// hidup di satu tempat, kalau tidak akun bisa dihapus lewat jalur yang
	// menghitung konfirmasinya secara berbeda.
	deleteAccount, err := app.NewDeleteAccount(users, sagas, hasher, profiles, revocations, uow, now, log)
	if err != nil {
		return nil, nil, err
	}

	server, err := identitygrpc.NewServer(identitygrpc.UseCases{
		Register:              register,
		Login:                 login,
		Logout:                logout,
		RequestReset:          requestReset,
		ConfirmReset:          confirmReset,
		ExchangeSocial:        exchange,
		Deletion:              deleteAccount,
		Users:                 users,
		Tokens:                verifier,
		Social:                socialVerifier,
		AccessTokenTTLSeconds: int64(cfg.AccessTTL.Seconds()),
	})
	if err != nil {
		return nil, nil, err
	}
	return server, deleteAccount, nil
}

// healthEndpoint menerima probes dari luar, bukan membuatnya sendiri.
//
// Versi pertama membuatnya di dalam dan tidak mengembalikannya, sehingga
// SetReady tidak mungkin dipanggil dan readyz menjawab 503 selamanya - pod
// yang tidak pernah menerima trafik. Bentuk itu membuat kekeliruannya tak
// terhindarkan; bentuk ini membuatnya mustahil.
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

// dialProfiles membuka koneksi ke profile-svc, atau mengembalikan penopang
// yang menolak bila alamatnya tidak dikonfigurasi.
//
// Koneksi gRPC dibuka malas, jadi tidak ada yang gagal di sini kalau
// profile-svc sedang mati - dan itu justru yang diinginkan: identity-svc
// TIDAK boleh menolak menyala karena tetangganya belum siap. Kegagalannya
// muncul per panggilan, dan setiap pemanggilnya sudah dirancang menghadapi
// kegagalan itu (ADR-002 aturan 1 dan 2).
func dialProfiles(target string, log *slog.Logger) (profileClient, func(), error) {
	if target == "" {
		log.Warn("profile-svc is not configured; profiles will not be created",
			"variable", "PROFILE_GRPC_TARGET", "task", "F1-31")
		return unavailableProfiles{}, func() {}, nil
	}

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("creating the profile-svc client: %w", err)
	}

	client, err := profileclient.New(conn)
	if err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, nil, err
	}

	return client, func() {
		if err := conn.Close(); err != nil {
			log.Error("closing the profile-svc connection", "error", err)
		}
	}, nil
}

// buildResetLinkSender merakit pengirim tautan reset, atau mengembalikan
// penopang yang menolak bila lingkungan ini tidak punya server surel.
//
// Terisi SEBAGIAN menggagalkan start-up: host tanpa alamat pengirim akan
// menyalakan service lalu gagal di permintaan reset pertama, jauh setelah
// orang yang salah mengetiknya pergi.
func buildResetLinkSender(cfg identity.MailConfig, log *slog.Logger) (app.ResetLinkSender, error) {
	if !cfg.Configured() {
		if missing := cfg.Missing(); len(missing) < 4 {
			return nil, fmt.Errorf("mail is partly configured; missing: %v", missing)
		}
		log.Warn("no mail transport is configured; password reset cannot be completed",
			"task", "F1-33")
		return unavailableLinks{}, nil
	}

	sender, err := mail.NewSMTP(mail.Config{
		Host:     cfg.Host,
		Port:     cfg.Port,
		Username: cfg.Username,
		Password: cfg.Password,
		From:     cfg.From,
	})
	if err != nil {
		return nil, err
	}

	links, err := identitymail.NewResetLinkSender(sender, cfg.FrontendURL)
	if err != nil {
		return nil, err
	}

	log.Info("mail transport is configured", "host", cfg.Host, "port", cfg.Port)
	return links, nil
}
