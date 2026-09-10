// Command identity-svc serves the identity.v1 contract over gRPC.
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
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/mail"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	rd "github.com/muhananaufal/selaras-platform-go/internal/platform/redis"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/rpc"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// shutdownGrace bounds how long requests in flight may take to finish after
// the stop signal is received.
const shutdownGrace = 15 * time.Second

func main() {
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("identity-svc stopped", "error", err)
		os.Exit(1)
	}
	log.Info("identity-svc stopped cleanly")
}

func run(log *slog.Logger) error {
	// Signals are caught BEFORE anything is opened, so a Ctrl-C during a slow
	// start-up is still handled instead of killing the process in the middle
	// of opening a connection.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := identity.LoadConfig()
	if err != nil {
		return err
	}

	// Telemetry is started before any other dependency is opened, so even the
	// first connection is recorded. Its failure stops the start: a process
	// that cannot be observed is more dangerous than a process that does not
	// start, because the latter is visible.
	tel, err := telemetry.Start(ctx, "identity-svc", log)
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

	// The public key is derived from the same private key, so identity-svc can
	// verify its own tokens without extra configuration - and without trusting
	// anyone about who owns a token that was sent.
	publicKey, ok := cfg.SigningKey.Public().(ed25519.PublicKey)
	if !ok {
		// Cannot happen with a configuration that passed LoadConfig, but an
		// unchecked type assertion that fails would panic - and a panic at
		// start-up is far harder to read than one sentence.
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

	// A one-time token for the identity -> profile calls that happen BEFORE the
	// user holds a token (registration, login): profile-svc has no special
	// path, it verifies this token like an ordinary user token (ADR-026). Its
	// lifetime is thirty seconds - ten times the call deadline.
	bootstrap, err := token.NewIssuer(cfg.SigningKey, cfg.TokenIssuer, 30*time.Second)
	if err != nil {
		return err
	}
	mint := profileclient.Minter(func(id domain.UserID) (string, error) {
		return bootstrap.Issue(domain.Claims{UserID: id, Role: domain.RoleUser, Generation: 1})
	})
	profiles, closeProfiles, err := dialProfiles(cfg.ProfileAddr, mint, log)
	if err != nil {
		return err
	}
	defer closeProfiles()

	server, deleteAccount, err := buildServer(cfg, pool, issuer, verifier, revocations, profiles, log)
	if err != nil {
		return err
	}

	// The outbox relay and the confirmation consumer: one sends the deletion
	// requests out, the other receives the answers. Each can fail on its own,
	// so the two are started separately.
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

	// A saga left hanging by a previous process will not finish itself: its
	// units have been contacted, and those that did not answer will not be
	// asked again. The only way it becomes visible is if someone is told at
	// start-up.
	deleteAccount.LogOutstandingSagas(ctx, log)

	probes := httpx.NewHealth()

	// ADR-026: user-scoped RPCs (GetMe, DeleteAccount, ...) have to carry a
	// token whose sub matches user_id; the public key is already available
	// above.
	authVerifier, err := authn.NewVerifier(publicKey, cfg.TokenIssuer)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(telemetry.GRPCServerOption(),
		grpc.ChainUnaryInterceptor(authn.UnaryServerInterceptor(authVerifier)))
	identityv1.RegisterIdentityServer(grpcServer, server)

	// Health check and reflection are both enabled. Reflection lets grpcurl be
	// used without carrying the proto files - it is the only way to inspect
	// this service from the outside without writing a client first.
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	reflection.Register(grpcServer)
	healthServer.SetServingStatus("identity.v1.Identity", healthpb.HealthCheckResponse_SERVING)

	// Ready is declared once every dependency is open and proven reachable -
	// the Postgres pool and Redis have both been pinged above. Declaring it
	// earlier means traffic arrives before anything can serve it.
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

	// Kubernetes marks the pod not-ready first, but the signal can arrive
	// before the load balancer has had a chance to stop sending. The status is
	// lowered here as well so probes arriving at that moment are honest.
	healthServer.SetServingStatus("identity.v1.Identity", healthpb.HealthCheckResponse_NOT_SERVING)
	probes.SetReady(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("shutting down health endpoint", "error", err)
	}

	// GracefulStop waits for requests in flight to finish. It is time-bounded:
	// one hanging request must not hold the pod forever and eat up the
	// orchestrator's grace period.
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

	// The social verifier is chosen here, once, from the configuration. An
	// environment without provider credentials still starts with one sign-in
	// path; what is FORBIDDEN is only pretending to succeed.
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

	// Account deletion needs the password comparer AND the saga storage.
	//
	// It is returned separately because the confirmation consumer uses it too,
	// and the two have to be the SAME use case - the saga closing rule may
	// live in only one place, otherwise an account could be deleted through a
	// path that counts its confirmations differently.
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

// healthEndpoint receives probes from outside rather than creating them
// itself.
//
// The first version created them inside and did not return them, so
// SetReady could not possibly be called and readyz answered 503 forever - a
// pod that never received traffic. That shape made the mistake unavoidable;
// this shape makes it impossible.
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

// dialProfiles opens the connection to profile-svc, or returns a refusing
// stand-in when its address is not configured.
//
// gRPC connections are opened lazily, so nothing fails here if profile-svc
// is down - and that is precisely what is wanted: identity-svc must NOT
// refuse to start because its neighbour is not ready. The failure shows up
// per call, and every caller is already designed to face that failure
// (ADR-002 rules 1 and 2).
func dialProfiles(target string, mint profileclient.Minter, log *slog.Logger) (profileClient, func(), error) {
	if target == "" {
		log.Warn("profile-svc is not configured; profiles will not be created",
			"variable", "PROFILE_GRPC_TARGET", "task", "F1-31")
		return unavailableProfiles{}, func() {}, nil
	}

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		telemetry.GRPCDialOption(),
		// The user's token is passed downstream (ADR-026).
		grpc.WithChainUnaryInterceptor(authn.UnaryClientInterceptor()),
		// A per-call deadline (chaos F9-13): without it, a service that has just
		// died makes its callers hang instead of fail.
		rpc.WithUpstreamDeadline(rpc.DefaultUpstreamTimeout))
	if err != nil {
		return nil, nil, fmt.Errorf("creating the profile-svc client: %w", err)
	}

	client, err := profileclient.New(conn, mint)
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

// buildResetLinkSender assembles the reset link sender, or returns a
// refusing stand-in when this environment has no mail server.
//
// PARTIALLY filled fails start-up: a host without a sender address would
// start the service and then fail on the first reset request, long after
// whoever mistyped it has left.
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
