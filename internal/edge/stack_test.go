package edge_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	goredis "github.com/redis/go-redis/v9"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/oauth"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/service"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/crypto"
	identitygrpc "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/grpc"
	identitypg "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/social"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	identityapp "github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	identitydomain "github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/redis/redistest"
	profilegrpc "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/grpc"
	profilepg "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/postgres"
	profileapp "github.com/muhananaufal/selaras-platform-go/internal/profile/app"
)

// The whole stack runs: the Connect gateway, both gRPC services, and a real
// Postgres. Only the revocation checker is faked - it is already tested
// separately against a real Redis, and bringing it in here would add one
// dependency without adding anything proven.
type stack struct {
	server      *httptest.Server
	links       *capturedLinks
	revocations *stubRevocations
	google      *fakeGoogle
}

type capturedLinks struct{ sent []identitydomain.ResetToken }

func (c *capturedLinks) SendResetLink(_ context.Context, _ identitydomain.Email, t identitydomain.ResetToken) error {
	c.sent = append(c.sent, t)
	return nil
}

// stubRevocations keeps generations in memory and fails closed, just like
// the real one.
type stubRevocations struct {
	generations map[string]int64
	fail        bool
}

func newStubRevocations() *stubRevocations {
	return &stubRevocations{generations: map[string]int64{}}
}

func (s *stubRevocations) IsCurrent(_ context.Context, userID identitydomain.UserID, gen int64) (bool, error) {
	if s.fail {
		return false, errNoAnswer
	}
	current, ok := s.generations[userID.String()]
	if !ok {
		// Never seen means the first generation, like a user who just registered.
		return gen == 1, nil
	}
	return current == gen, nil
}

func (s *stubRevocations) PublishGeneration(_ context.Context, userID identitydomain.UserID, gen int64) error {
	s.generations[userID.String()] = gen
	return nil
}

var errNoAnswer = &noAnswerError{}

type noAnswerError struct{}

func (*noAnswerError) Error() string { return "cannot confirm the token generation" }

type stubProfilesForIdentity struct {
	profiles profilev1.ProfileClient
}

func (s stubProfilesForIdentity) CreateEmptyProfile(ctx context.Context, userID identitydomain.UserID) (string, error) {
	resp, err := s.profiles.CreateEmptyProfile(ctx, &profilev1.CreateEmptyProfileRequest{UserId: userID.String()})
	if err != nil {
		return "", err
	}
	return resp.GetProfile().GetId(), nil
}

func (s stubProfilesForIdentity) FindProfileID(ctx context.Context, userID identitydomain.UserID) (string, error) {
	resp, err := s.profiles.ResolveProfileId(ctx, &profilev1.ResolveProfileIdRequest{UserId: userID.String()})
	if err != nil {
		return "", err
	}
	return resp.GetUserProfileId(), nil
}

type stubSocial struct{}

func (stubSocial) Verify(context.Context, string, string) (identityapp.SocialIdentity, error) {
	return identityapp.SocialIdentity{}, errNoAnswer
}

func newStack(t *testing.T) *stack {
	t.Helper()
	return build(t, nil)
}

// newStackWithGoogle adds the fake provider to the same stack, so the social
// flow is tested through exactly the same path as every other flow.
func newStackWithGoogle(t *testing.T) *stack {
	t.Helper()
	return build(t, newFakeGoogle(t))
}

func build(t *testing.T, google *fakeGoogle) *stack {
	t.Helper()

	identityPool := pgtest.Open(t, "identity")
	profilePool := pgtest.Open(t, "profile")
	pgtest.Truncate(t, identityPool, "users", "password_reset_tokens")
	pgtest.Truncate(t, profilePool, "user_profiles")

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	issuer, err := token.NewIssuer(priv, "identity-svc", time.Hour)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	verifier, err := token.NewVerifier(pub, "identity-svc")
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// profile-svc first, because identity-svc calls it.
	profileSvc, err := profileapp.NewService(profilepg.NewProfileRepository(profilePool), time.Now)
	if err != nil {
		t.Fatalf("profile NewService: %v", err)
	}
	profileServer, err := profilegrpc.NewServer(profileSvc)
	if err != nil {
		t.Fatalf("profile NewServer: %v", err)
	}
	profileClient := profilev1.NewProfileClient(serveGRPC(t, func(s *grpc.Server) {
		profilev1.RegisterProfileServer(s, profileServer)
	}))

	uow := identitypg.NewUnitOfWork(identityPool)
	hasher := crypto.NewArgon2idHasher(crypto.FastParamsForTests())
	links := &capturedLinks{}
	revocations := newStubRevocations()
	profiles := stubProfilesForIdentity{profiles: profileClient}
	now := time.Now

	register, err := identityapp.NewRegister(uow, hasher, issuer, profiles, now)
	if err != nil {
		t.Fatalf("NewRegister: %v", err)
	}
	login, err := identityapp.NewLogin(uow, hasher, issuer, profiles, revocations, now)
	if err != nil {
		t.Fatalf("NewLogin: %v", err)
	}
	logout, err := identityapp.NewLogout(uow, revocations, now)
	if err != nil {
		t.Fatalf("NewLogout: %v", err)
	}
	requestReset, err := identityapp.NewRequestPasswordReset(uow, links, now)
	if err != nil {
		t.Fatalf("NewRequestPasswordReset: %v", err)
	}
	confirmReset, err := identityapp.NewConfirmPasswordReset(uow, hasher, revocations, now)
	if err != nil {
		t.Fatalf("NewConfirmPasswordReset: %v", err)
	}
	exchange, err := identityapp.NewExchangeSocialToken(uow, issuer, profiles, revocations, now)
	if err != nil {
		t.Fatalf("NewExchangeSocialToken: %v", err)
	}

	identityServer, err := identitygrpc.NewServer(identitygrpc.UseCases{
		Register:              register,
		Login:                 login,
		Logout:                logout,
		RequestReset:          requestReset,
		ConfirmReset:          confirmReset,
		ExchangeSocial:        exchange,
		Users:                 identitypg.NewUserRepository(identityPool),
		Tokens:                verifier,
		Social:                socialVerifier(t, google),
		AccessTokenTTLSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("identity NewServer: %v", err)
	}
	identityClient := identityv1.NewIdentityClient(serveGRPC(t, func(s *grpc.Server) {
		identityv1.RegisterIdentityServer(s, identityServer)
	}))

	// A real Redis: the state and handoff codes are stored there, and both
	// depend on GETDEL being atomic - a property an in-memory fake cannot
	// prove.
	redisClient := redistest.Open(t)

	probes := httpx.NewHealth()
	probes.SetReady(true)

	social, handoff := buildSocialHandler(t, google, identityClient, redisClient)
	handler, err := edge.NewHandler(edge.Deps{
		Identity:    identityClient,
		Profiles:    profileClient,
		Tokens:      verifier,
		Revocations: revocations,
		Probes:      probes,
		Now:         time.Now,
		Social:      social,
		Handoff:     handoff,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &stack{server: server, links: links, revocations: revocations, google: google}
}

// socialVerifier uses the REAL Google verifier, pointed at the fake
// provider's JWKS. Only the provider is faked; signature, audience, issuer,
// and email_verified verification all run.
func socialVerifier(t *testing.T, google *fakeGoogle) identitygrpc.SocialIdentityVerifier {
	t.Helper()
	if google == nil {
		return stubSocial{}
	}

	verifier, err := social.NewGoogleVerifier(
		googleClientID, google.server.URL+"/certs", google.server.Client(), time.Hour)
	if err != nil {
		t.Fatalf("NewGoogleVerifier: %v", err)
	}
	return verifier
}

// buildSocialHandler assembles the OAuth flow at the edge, or nil when
// there is no provider - exactly like an environment without credentials.
func buildSocialHandler(
	t *testing.T,
	google *fakeGoogle,
	identity identityv1.IdentityClient,
	redisClient *goredis.Client,
) (*service.Social, service.HandoffCodes) {
	t.Helper()
	if google == nil {
		return nil, nil
	}

	store, err := oauth.NewStore(redisClient, 10*time.Minute, time.Minute)
	if err != nil {
		t.Fatalf("oauth.NewStore: %v", err)
	}
	provider, err := oauth.NewGoogle(oauth.GoogleConfig{
		ClientID:     googleClientID,
		ClientSecret: "not-a-secret-in-tests",
		RedirectURL:  "http://127.0.0.1/auth/google/callback",
		AuthURL:      google.server.URL + "/auth",
		TokenURL:     google.server.URL + "/token",
		Client:       google.server.Client(),
	})
	if err != nil {
		t.Fatalf("oauth.NewGoogle: %v", err)
	}

	return service.NewSocial(
		identity,
		map[string]service.ProviderClient{"google": provider},
		store,
		"http://frontend.test",
	), store
}

func serveGRPC(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	register(server)

	go func() {
		if err := server.Serve(listener); err != nil {
			t.Errorf("serving: %v", err)
		}
	}()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("closing connection: %v", err)
		}
	})
	return conn
}

// rpc calls one procedure over the Connect protocol with a JSON body - the
// wire a browser uses - and decodes the JSON answer, success or error.
func (s *stack) rpc(t *testing.T, procedure, bearer string, body any) (int, map[string]any) {
	t.Helper()

	if body == nil {
		body = map[string]any{}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding body: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		s.server.URL+procedure, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := s.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s: %v", procedure, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body: %v", err)
		}
	}()

	decoded := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decoding %s: %v", procedure, err)
	}
	return resp.StatusCode, decoded
}

func (s *stack) registerUser(t *testing.T, email string) string {
	t.Helper()

	status, body := s.rpc(t, "/edge.v1.Auth/Register", "", map[string]string{
		"email":                email,
		"password":             "a-long-enough-password",
		"passwordConfirmation": "a-long-enough-password",
	})
	if status != http.StatusOK {
		t.Fatalf("register status = %d; want 200 (%v)", status, body)
	}
	return accessToken(t, body)
}

// accessToken takes session.accessToken from a Register/Login answer.
func accessToken(t *testing.T, body map[string]any) string {
	t.Helper()
	session, _ := body["session"].(map[string]any)
	token, _ := session["accessToken"].(string)
	if token == "" {
		t.Fatalf("no session.accessToken in %v", body)
	}
	return token
}

// violations returns the field names of a google.rpc.BadRequest detail,
// read from the "debug" form Connect adds to JSON errors.
func violations(body map[string]any) []string {
	var fields []string
	details, _ := body["details"].([]any)
	for _, d := range details {
		detail, _ := d.(map[string]any)
		debug, _ := detail["debug"].(map[string]any)
		list, _ := debug["fieldViolations"].([]any)
		for _, v := range list {
			fv, _ := v.(map[string]any)
			if f, ok := fv["field"].(string); ok {
				fields = append(fields, f)
			}
		}
	}
	return fields
}

// errNoAnswer deliberately has its own type, not errors.New, so the test
// can tell it apart from any error that may come from another layer.
var _ error = errNoAnswer
