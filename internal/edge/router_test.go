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

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/crypto"
	identitygrpc "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/grpc"
	identitypg "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	identityapp "github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	identitydomain "github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
	profilegrpc "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/grpc"
	profilepg "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/postgres"
	profileapp "github.com/muhananaufal/selaras-platform-go/internal/profile/app"
)

// Seluruh tumpukan dijalankan: gateway HTTP, kedua service gRPC, dan Postgres
// sungguhan. Yang dipalsu hanya pemeriksa pencabutan - ia sudah diuji
// terpisah terhadap Redis sungguhan, dan menghadirkannya di sini hanya
// menambah satu dependensi tanpa menambah yang dibuktikan.
type stack struct {
	server      *httptest.Server
	links       *capturedLinks
	revocations *stubRevocations
}

type capturedLinks struct{ sent []identitydomain.ResetToken }

func (c *capturedLinks) SendResetLink(_ context.Context, _ identitydomain.Email, t identitydomain.ResetToken) error {
	c.sent = append(c.sent, t)
	return nil
}

// stubRevocations menyimpan generasi di memori dan gagal-tertutup, sama
// seperti yang sungguhan.
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
		// Belum pernah terlihat berarti generasi pertama, seperti pengguna
		// yang baru mendaftar.
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

	// profile-svc lebih dulu, karena identity-svc memanggilnya.
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
		Social:                stubSocial{},
		AccessTokenTTLSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("identity NewServer: %v", err)
	}
	identityClient := identityv1.NewIdentityClient(serveGRPC(t, func(s *grpc.Server) {
		identityv1.RegisterIdentityServer(s, identityServer)
	}))

	probes := httpx.NewHealth()
	probes.SetReady(true)

	router := edge.NewRouter(edge.Deps{
		Identity:    identityClient,
		Profiles:    profileClient,
		Tokens:      verifier,
		Revocations: revocations,
		Probes:      probes,
		Now:         time.Now,
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &stack{server: server, links: links, revocations: revocations}
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

func (s *stack) do(t *testing.T, method, path, bearer string, body any) (int, map[string]any) {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, s.server.URL+path, reader)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := s.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body: %v", err)
		}
	}()

	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decoding %s %s: %v", method, path, err)
	}
	return resp.StatusCode, decoded
}

func (s *stack) registerUser(t *testing.T, email string) string {
	t.Helper()

	status, body := s.do(t, http.MethodPost, "/api/v1/register", "", map[string]string{
		"email":                 email,
		"password":              "a-long-enough-password",
		"password_confirmation": "a-long-enough-password",
	})
	if status != http.StatusCreated {
		t.Fatalf("register status = %d; want 201 (%v)", status, body)
	}
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatal("register returned no token")
	}
	return token
}

// errNoAnswer sengaja bertipe sendiri, bukan errors.New, supaya test bisa
// membedakannya dari galat apa pun yang mungkin datang dari lapisan lain.
var _ error = errNoAnswer
