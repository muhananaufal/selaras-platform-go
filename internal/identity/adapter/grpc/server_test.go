package grpc_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/crypto"
	identitygrpc "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/grpc"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

const accessTokenTTL = time.Hour

// stubProfiles berdiri untuk profile-svc. Ia satu-satunya bagian yang dipalsu
// di sini; sisanya - Postgres, argon2id, penandatanganan EdDSA - berjalan
// sungguhan, karena yang diuji adalah apakah service-nya benar-benar bekerja.
type stubProfiles struct {
	id  string
	err error
}

func (s *stubProfiles) CreateEmptyProfile(context.Context, domain.UserID) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.id, nil
}

func (s *stubProfiles) FindProfileID(context.Context, domain.UserID) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.id, nil
}

type stubRevocations struct{}

func (stubRevocations) PublishGeneration(context.Context, domain.UserID, int64) error { return nil }

type stubSocial struct {
	identity app.SocialIdentity
	err      error
}

func (s *stubSocial) Verify(context.Context, string, string) (app.SocialIdentity, error) {
	if s.err != nil {
		return app.SocialIdentity{}, s.err
	}
	return s.identity, nil
}

type stubLinks struct{ sent []domain.ResetToken }

func (s *stubLinks) SendResetLink(_ context.Context, _ domain.Email, t domain.ResetToken) error {
	s.sent = append(s.sent, t)
	return nil
}

type harness struct {
	client   identityv1.IdentityClient
	links    *stubLinks
	profiles *stubProfiles
	verifier *token.Verifier
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	pool := pgtest.Open(t, "identity")
	pgtest.Truncate(t, pool, "users", "password_reset_tokens")

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	issuer, err := token.NewIssuer(priv, "identity-svc", accessTokenTTL)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	verifier, err := token.NewVerifier(pub, "identity-svc")
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	uow := postgres.NewUnitOfWork(pool)
	hasher := crypto.NewArgon2idHasher(crypto.FastParamsForTests())
	profiles := &stubProfiles{id: "profile-1"}
	links := &stubLinks{}
	social := &stubSocial{}
	now := time.Now

	register, err := app.NewRegister(uow, hasher, issuer, profiles, now)
	if err != nil {
		t.Fatalf("NewRegister: %v", err)
	}
	login, err := app.NewLogin(uow, hasher, issuer, profiles, stubRevocations{}, now)
	if err != nil {
		t.Fatalf("NewLogin: %v", err)
	}
	logout, err := app.NewLogout(uow, stubRevocations{}, now)
	if err != nil {
		t.Fatalf("NewLogout: %v", err)
	}
	requestReset, err := app.NewRequestPasswordReset(uow, links, now)
	if err != nil {
		t.Fatalf("NewRequestPasswordReset: %v", err)
	}
	confirmReset, err := app.NewConfirmPasswordReset(uow, hasher, stubRevocations{}, now)
	if err != nil {
		t.Fatalf("NewConfirmPasswordReset: %v", err)
	}
	exchange, err := app.NewExchangeSocialToken(uow, issuer, profiles, stubRevocations{}, now)
	if err != nil {
		t.Fatalf("NewExchangeSocialToken: %v", err)
	}

	server, err := identitygrpc.NewServer(identitygrpc.UseCases{
		Register:              register,
		Login:                 login,
		Logout:                logout,
		RequestReset:          requestReset,
		ConfirmReset:          confirmReset,
		ExchangeSocial:        exchange,
		Users:                 postgres.NewUserRepository(pool),
		Tokens:                verifier,
		Social:                social,
		AccessTokenTTLSeconds: int64(accessTokenTTL.Seconds()),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	return &harness{client: serve(t, server), links: links, profiles: profiles, verifier: verifier}
}

// serve menyalakan server di atas listener dalam proses. Tidak ada port yang
// dibuka, jadi test bisa berjalan paralel tanpa saling merebut alamat.
func serve(t *testing.T, server identityv1.IdentityServer) identityv1.IdentityClient {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	identityv1.RegisterIdentityServer(grpcServer, server)

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			t.Errorf("serving: %v", err)
		}
	}()
	t.Cleanup(grpcServer.Stop)

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

	return identityv1.NewIdentityClient(conn)
}

func TestRegisterOverGrpcReturnsAUsableToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	got, err := h.client.Register(ctx, &identityv1.RegisterRequest{
		Email:    "new@user.co",
		Password: "a-long-enough-password",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if got.GetUser().GetEmail() != "new@user.co" {
		t.Errorf("email = %q; want %q", got.GetUser().GetEmail(), "new@user.co")
	}
	if got.GetUser().GetRole() != identityv1.Role_ROLE_USER {
		t.Errorf("role = %v; want ROLE_USER", got.GetUser().GetRole())
	}
	if got.GetUser().GetEmailVerified() {
		t.Error("a self-registered account came back verified")
	}
	if got.GetToken().GetExpiresInSeconds() != int64(accessTokenTTL.Seconds()) {
		t.Errorf("expires in = %d; want %d",
			got.GetToken().GetExpiresInSeconds(), int64(accessTokenTTL.Seconds()))
	}

	// Tokennya harus benar-benar bisa diverifikasi, bukan sekadar tidak
	// kosong. Klaimnya diperiksa terhadap identitas yang dikembalikan.
	claims, err := h.verifier.Verify(got.GetToken().GetAccessToken())
	if err != nil {
		t.Fatalf("the returned token does not verify: %v", err)
	}
	if claims.UserID.String() != got.GetIdentity().GetUserId() {
		t.Errorf("token subject = %s; want %s", claims.UserID, got.GetIdentity().GetUserId())
	}
	if claims.UserProfileID != "profile-1" {
		t.Errorf("token profile id = %q; want %q", claims.UserProfileID, "profile-1")
	}
	if claims.Generation != 1 {
		t.Errorf("token generation = %d; want 1", claims.Generation)
	}
}

func TestRegisteringTheSameAddressTwiceIsAlreadyExists(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	req := &identityv1.RegisterRequest{Email: "dup@user.co", Password: "a-long-enough-password"}
	if _, err := h.client.Register(ctx, req); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	_, err := h.client.Register(ctx, req)
	if got := status.Code(err); got != codes.AlreadyExists {
		t.Errorf("code = %v; want AlreadyExists", got)
	}
}

func TestInvalidInputIsInvalidArgument(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for name, req := range map[string]*identityv1.RegisterRequest{
		"malformed email": {Email: "not-an-address", Password: "a-long-enough-password"},
		"short password":  {Email: "a@b.co", Password: "short"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.client.Register(ctx, req)
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("code = %v; want InvalidArgument", got)
			}
		})
	}
}

// Login yang berhasil menaikkan generasi, sehingga token dari registrasi
// sebelumnya tertinggal satu generasi (D1).
func TestLoginOverGrpcAdvancesTheGeneration(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	registered, err := h.client.Register(ctx, &identityv1.RegisterRequest{
		Email: "known@user.co", Password: "a-long-enough-password",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	loggedIn, err := h.client.Login(ctx, &identityv1.LoginRequest{
		Email: "known@user.co", Password: "a-long-enough-password",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	before, err := h.verifier.Verify(registered.GetToken().GetAccessToken())
	if err != nil {
		t.Fatalf("verifying the registration token: %v", err)
	}
	after, err := h.verifier.Verify(loggedIn.GetToken().GetAccessToken())
	if err != nil {
		t.Fatalf("verifying the login token: %v", err)
	}
	if after.Generation != before.Generation+1 {
		t.Errorf("generation went %d -> %d; want it to advance by one",
			before.Generation, after.Generation)
	}

	// Dan sumbernya setuju.
	gen, err := h.client.GetTokenGeneration(ctx, &identityv1.GetTokenGenerationRequest{
		UserId: loggedIn.GetIdentity().GetUserId(),
	})
	if err != nil {
		t.Fatalf("GetTokenGeneration: %v", err)
	}
	if gen.GetGeneration() != after.Generation {
		t.Errorf("the source says %d; the token says %d", gen.GetGeneration(), after.Generation)
	}
}

// Setiap kegagalan masuk menjawab Unauthenticated, dan pesannya sama.
func TestEveryLoginFailureLooksTheSameOverGrpc(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.client.Register(ctx, &identityv1.RegisterRequest{
		Email: "known@user.co", Password: "a-long-enough-password",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var messages []string
	for _, req := range []*identityv1.LoginRequest{
		{Email: "nobody@here.co", Password: "a-long-enough-password"},
		{Email: "known@user.co", Password: "the-wrong-password"},
	} {
		_, err := h.client.Login(ctx, req)
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("Login returned a non-status error: %v", err)
		}
		if st.Code() != codes.Unauthenticated {
			t.Errorf("code = %v; want Unauthenticated", st.Code())
		}
		messages = append(messages, st.Message())
	}

	if messages[0] != messages[1] {
		t.Errorf("the two failures answer differently: %q vs %q", messages[0], messages[1])
	}
}

// Alur reset lengkap lewat kabel, dan tokennya diambil dari yang benar-benar
// dikirim - bukan dibaca dari basis data, karena yang disimpan adalah hash.
func TestThePasswordResetFlowWorksEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.client.Register(ctx, &identityv1.RegisterRequest{
		Email: "reset@user.co", Password: "the-original-password",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := h.client.RequestPasswordReset(ctx, &identityv1.RequestPasswordResetRequest{
		Email: "reset@user.co",
	}); err != nil {
		t.Fatalf("RequestPasswordReset: %v", err)
	}
	if len(h.links.sent) != 1 {
		t.Fatalf("%d links sent; want 1", len(h.links.sent))
	}

	if _, err := h.client.ConfirmPasswordReset(ctx, &identityv1.ConfirmPasswordResetRequest{
		Token:       h.links.sent[0].Expose(),
		NewPassword: "a-brand-new-password",
	}); err != nil {
		t.Fatalf("ConfirmPasswordReset: %v", err)
	}

	if _, err := h.client.Login(ctx, &identityv1.LoginRequest{
		Email: "reset@user.co", Password: "a-brand-new-password",
	}); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if _, err := h.client.Login(ctx, &identityv1.LoginRequest{
		Email: "reset@user.co", Password: "the-original-password",
	}); status.Code(err) != codes.Unauthenticated {
		t.Error("the old password still works after a reset")
	}
}

// Endpoint permintaan reset menjawab sama untuk alamat yang tidak terdaftar.
func TestRequestingAResetForAnUnknownAddressStillSucceeds(t *testing.T) {
	h := newHarness(t)

	if _, err := h.client.RequestPasswordReset(context.Background(),
		&identityv1.RequestPasswordResetRequest{Email: "nobody@here.co"}); err != nil {
		t.Errorf("RequestPasswordReset = %v; want success, otherwise it enumerates", err)
	}
	if len(h.links.sent) != 0 {
		t.Error("a link was sent for an address that does not exist")
	}
}

func TestAnUnknownUserIdIsNotFound(t *testing.T) {
	h := newHarness(t)

	_, err := h.client.GetTokenGeneration(context.Background(),
		&identityv1.GetTokenGenerationRequest{UserId: "018f4c1e-0000-7000-8000-0000000000ff"})
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code = %v; want NotFound", got)
	}

	_, err = h.client.GetTokenGeneration(context.Background(),
		&identityv1.GetTokenGenerationRequest{UserId: "not-a-uuid"})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %v; want InvalidArgument", got)
	}
}

// Yang belum ada menjawab Unimplemented, bukan berpura-pura berhasil.
func TestUnbuiltOperationsSaySoPlainly(t *testing.T) {
	h := newHarness(t)

	if _, err := h.client.DeleteAccount(context.Background(), &identityv1.DeleteAccountRequest{
		UserId: "018f4c1e-0000-7000-8000-000000000001", Password: "whatever",
	}); status.Code(err) != codes.Unimplemented {
		t.Errorf("DeleteAccount = %v; want Unimplemented", status.Code(err))
	}
}

// Logout memverifikasi tanda tangan tokennya SENDIRI, bukan mempercayai
// pemanggilnya. Token yang tidak sah ditolak sebelum apa pun dicabut -
// kalau tidak, siapa pun yang bisa menjangkau service ini bisa mengeluarkan
// pengguna mana pun dari sesinya.
func TestLogoutRefusesATokenItDidNotIssue(t *testing.T) {
	h := newHarness(t)

	for name, raw := range map[string]string{
		"garbage":     "not-a-token",
		"empty":       "",
		"three parts": "a.b.c",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.client.Logout(context.Background(), &identityv1.LogoutRequest{AccessToken: raw})
			if status.Code(err) != codes.Unauthenticated {
				t.Errorf("code = %v; want Unauthenticated", status.Code(err))
			}
		})
	}
}

// Dan token yang sah benar-benar mencabut sesinya: generasi setelahnya naik.
func TestLogoutAdvancesTheGenerationOverGrpc(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	reg, err := h.client.Register(ctx, &identityv1.RegisterRequest{
		Email: "logout@user.co", Password: "a-long-enough-password",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := h.client.Logout(ctx, &identityv1.LogoutRequest{
		AccessToken: reg.GetToken().GetAccessToken(),
	}); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	gen, err := h.client.GetTokenGeneration(ctx, &identityv1.GetTokenGenerationRequest{
		UserId: reg.GetIdentity().GetUserId(),
	})
	if err != nil {
		t.Fatalf("GetTokenGeneration: %v", err)
	}
	if gen.GetGeneration() != 2 {
		t.Errorf("generation = %d after logout; want 2", gen.GetGeneration())
	}
}

func TestNewServerRefusesMissingDependencies(t *testing.T) {
	if _, err := identitygrpc.NewServer(identitygrpc.UseCases{}); err == nil {
		t.Error("NewServer accepted an empty set of use cases")
	}
}
