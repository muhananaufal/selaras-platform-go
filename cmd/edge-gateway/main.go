// Command edge-gateway serves the public REST contract and forwards it to
// the services behind it over gRPC.
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
	dashboardv1 "github.com/muhananaufal/selaras-platform-go/gen/dashboard/v1"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/handler"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/oauth"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/revocation"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	rd "github.com/muhananaufal/selaras-platform-go/internal/platform/redis"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/rpc"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

const shutdownGrace = 15 * time.Second

func main() {
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, nil)))
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

	// Telemetry is started before any other dependency is opened, so even the
	// first connection is recorded. Its failure stops the start: a gateway
	// that cannot be observed is more dangerous than a gateway that does not
	// start, because the latter is visible.
	tel, err := telemetry.Start(ctx, "edge-gateway", log)
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

	// The source of truth for revocation is identity-svc, reached over gRPC.
	// It is NOT a database connection: schema-per-service isolation is
	// enforced by the database itself, and the gateway has no rights in the
	// identity schema.
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
		// Without assessment-svc, the assessment routes are not mounted and
		// risk_region is sent as null. Both are honest: the first is a 404, the
		// second a value that cannot be computed yet.
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
		// Without coaching-svc, its routes are not mounted. 404 is far more
		// honest than 500 from a client connected to nothing.
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

	var nutritionHandler *handler.Nutrition
	if cfg.NutritionAddr != "" {
		conn, err := dial(cfg.NutritionAddr)
		if err != nil {
			return fmt.Errorf("nutrition-svc: %w", err)
		}
		defer closeConn(conn, "nutrition-svc", log)

		nutritionHandler = handler.NewNutrition(nutritionv1.NewNutritionClient(conn))
	} else {
		log.Warn("nutrition-svc is not configured; its routes are not mounted",
			"variable", "NUTRITION_GRPC_TARGET")
	}

	var dashboardHandler *handler.Dashboard
	if cfg.DashboardAddr != "" {
		conn, err := dial(cfg.DashboardAddr)
		if err != nil {
			return fmt.Errorf("dashboard-svc: %w", err)
		}
		defer closeConn(conn, "dashboard-svc", log)

		dashboardHandler = handler.NewDashboard(dashboardv1.NewDashboardClient(conn))
	} else {
		log.Warn("dashboard-svc is not configured; its route is not mounted",
			"variable", "DASHBOARD_GRPC_TARGET")
	}

	socialHandler, err := buildSocial(cfg, identityClient, redisClient, log)

	if err != nil {
		return err
	}

	probes := httpx.NewHealth()

	// Rate limiting uses the same Redis as the revocation cache.
	//
	// An authentication path without a limit is an unlimited place to guess
	// passwords, and an LLM path without a limit is an unlimited bill.
	limiter, err := middleware.NewLimiter(redisClient, log)
	if err != nil {
		return err
	}

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
		Nutrition:   nutritionHandler,
		Dashboards:  dashboardHandler,
		Regions:     regions,
		Limiter:     limiter,
	})

	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: router,
		// Timeouts are set explicitly. A Go HTTP server without timeouts holds
		// hanging connections forever, and that is the cheapest way to exhaust a
		// gateway's resources.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	admin := adminEndpoint(cfg.AdminAddr, probes, tel.Handler())

	probes.SetReady(true)

	errs := make(chan error, 2)
	go func() {
		log.Info("serving http", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	go func() {
		log.Info("serving metrics and probes", "addr", cfg.AdminAddr)
		if err := admin.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Not-ready is declared first, then the requests in flight are given time
	// to finish. The order matters: the load balancer stops sending new ones
	// before the old ones are cut off.
	probes.SetReady(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := admin.Shutdown(shutdownCtx); err != nil {
		log.Error("shutting down the admin endpoint", "error", err)
	}
	return server.Shutdown(shutdownCtx)
}

// adminEndpoint serves metrics and probes on a port that is not public.
//
// The probes remain on the public port too - the load balancer checks them
// there - and are repeated here so the admin port can be used on its own by
// Prometheus and the orchestrator without touching the API port.
func adminEndpoint(addr string, probes *httpx.Health, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", probes.Live)
	mux.HandleFunc("GET /readyz", probes.Ready)
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

func dial(target string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		telemetry.GRPCDialOption(),
		// The user's token is passed downstream (ADR-026).
		grpc.WithChainUnaryInterceptor(authn.UnaryClientInterceptor()),
		// A per-call deadline (chaos F9-13): without it, a service that has just
		// died makes its callers hang instead of fail.
		rpc.WithUpstreamDeadline(rpc.DefaultUpstreamTimeout))
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

// generationOverGRPC fetches the current token generation from identity-svc.
//
// Called ONLY when the revocation cache does not know, not on every request -
// that is what sets this design apart from the opaque tokens ADR-012 rejected.
type generationOverGRPC struct {
	identity identityv1.IdentityClient
}

func (g generationOverGRPC) CurrentGeneration(ctx context.Context, userID domain.UserID) (int64, error) {
	// A short deadline: this check sits on the path of every authenticated
	// request that misses the cache, and a slow identity-svc must not hold the
	// whole gateway.
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

// buildSocial assembles the social sign-in flow, or returns nil when this
// environment is not configured for it.
//
// nil means the routes are not mounted at all, so the answer is 404 - not an
// endpoint that exists but always fails. A PARTIALLY filled configuration is
// a mistake, not a deployment mode, and therefore fails start-up: a client
// id without a secret would mount the routes and then fail at the exchange,
// long after whoever mistyped it has left.
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
		return nil, nil //nolint:nilnil // nil here means "not mounted", and that is a valid state
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
