package authn_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	identitydomain "github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
)

const issuer = "identity-svc"

type keys struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newKeys(t *testing.T) keys {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return keys{pub: pub, priv: priv}
}

// mint signs a token with the same shape identity-svc produces, straight
// through the JWT library so this test does not depend on identity.
func (k keys) mint(t *testing.T, sub string, gen int64, tune func(*jwt.RegisteredClaims)) string {
	t.Helper()
	now := time.Now()
	reg := jwt.RegisteredClaims{
		Subject:   sub,
		Issuer:    issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		ID:        uuid.NewString(),
	}
	if tune != nil {
		tune(&reg)
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, struct {
		jwt.RegisteredClaims
		Gen int64 `json:"gen"`
	}{reg, gen}).SignedString(k.priv)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func verifier(t *testing.T, k keys) *authn.Verifier {
	t.Helper()
	v, err := authn.NewVerifier(k.pub, issuer)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func withBearer(ctx context.Context, raw string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+raw))
}

func call(t *testing.T, v authn.TokenVerifier, ctx context.Context, req any) (context.Context, error) {
	t.Helper()
	var seen context.Context
	_, err := authn.UnaryServerInterceptor(v)(ctx, req,
		&grpc.UnaryServerInfo{FullMethod: "/profile.v1.Profile/GetProfile"},
		func(ctx context.Context, _ any) (any, error) { seen = ctx; return nil, nil })
	return seen, err
}

func TestATokenIssuedByIdentityIsAccepted(t *testing.T) {
	// Compatibility with the real issuer, not only with mint().
	k := newKeys(t)
	iss, err := token.NewIssuer(k.priv, issuer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := identitydomain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := iss.Issue(identitydomain.Claims{UserID: uid, Role: identitydomain.RoleUser, Generation: 3})
	if err != nil {
		t.Fatal(err)
	}

	p, err := verifier(t, k).Verify(raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.UserID != uid.String() || p.Generation != 3 {
		t.Fatalf("principal %+v does not match the issued claims", p)
	}
}

func TestAMatchingUserIDPassesAndThePrincipalIsAvailable(t *testing.T) {
	k := newKeys(t)
	sub := uuid.NewString()
	ctx, err := call(t, verifier(t, k), withBearer(context.Background(), k.mint(t, sub, 1, nil)),
		&profilev1.GetProfileRequest{UserId: sub})
	if err != nil {
		t.Fatalf("a matching token was refused: %v", err)
	}
	p, err := authn.PrincipalFrom(ctx)
	if err != nil || p.UserID != sub {
		t.Fatalf("principal not on ctx: %+v %v", p, err)
	}
}

func TestSomeoneElsesUserIDIsForbidden(t *testing.T) {
	k := newKeys(t)
	_, err := call(t, verifier(t, k), withBearer(context.Background(), k.mint(t, uuid.NewString(), 1, nil)),
		&profilev1.GetProfileRequest{UserId: uuid.NewString()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("want PermissionDenied, got %v", err)
	}
}

func TestARequestWithUserIDButNoTokenIsUnauthenticated(t *testing.T) {
	k := newKeys(t)
	_, err := call(t, verifier(t, k), context.Background(), &profilev1.GetProfileRequest{UserId: uuid.NewString()})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestAPublicRPCPassesWithoutAToken(t *testing.T) {
	k := newKeys(t)
	if _, err := call(t, verifier(t, k), context.Background(),
		&assessmentv1.ResolveRiskRegionRequest{CountryOfResidence: "Indonesia"}); err != nil {
		t.Fatalf("a public RPC must not need a token: %v", err)
	}
}

func TestABadTokenIsRefusedEvenOnAPublicRPC(t *testing.T) {
	k, other := newKeys(t), newKeys(t)
	forged := other.mint(t, uuid.NewString(), 1, nil)
	_, err := call(t, verifier(t, k), withBearer(context.Background(), forged),
		&assessmentv1.ResolveRiskRegionRequest{CountryOfResidence: "Indonesia"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a token signed by another key must be refused: %v", err)
	}
}

func TestTokensWithoutGenerationOrExpiryAreRefused(t *testing.T) {
	k := newKeys(t)
	v := verifier(t, k)
	if _, err := v.Verify(k.mint(t, uuid.NewString(), 0, nil)); !errors.Is(err, authn.ErrInvalidToken) {
		t.Fatalf("generation 0 accepted: %v", err)
	}
	noExp := k.mint(t, uuid.NewString(), 1, func(c *jwt.RegisteredClaims) { c.ExpiresAt = nil })
	if _, err := v.Verify(noExp); !errors.Is(err, authn.ErrInvalidToken) {
		t.Fatalf("token without exp accepted: %v", err)
	}
	if _, err := v.Verify(k.mint(t, "not-a-uuid", 1, nil)); !errors.Is(err, authn.ErrInvalidToken) {
		t.Fatalf("non-uuid subject accepted: %v", err)
	}
}

func TestPlumbingIsNeverChallenged(t *testing.T) {
	k := newKeys(t)
	var handled bool
	_, err := authn.UnaryServerInterceptor(verifier(t, k))(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"},
		func(context.Context, any) (any, error) { handled = true; return nil, nil })
	if err != nil || !handled {
		t.Fatalf("health check was challenged: %v", err)
	}
}

// The client forwards the token: from WithToken (the gateway) or from the
// incoming metadata (a service calling another service on behalf of the
// same user).
func TestTheClientForwardsTheToken(t *testing.T) {
	capture := func(ctx context.Context) string {
		md, _ := metadata.FromOutgoingContext(ctx)
		if v := md.Get("authorization"); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	invoke := func(ctx context.Context) string {
		var got string
		_ = authn.UnaryClientInterceptor()(ctx, "/x/Y", nil, nil, nil,
			func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
				got = capture(ctx)
				return nil
			})
		return got
	}

	if got := invoke(authn.WithToken(context.Background(), "abc")); got != "Bearer abc" {
		t.Fatalf("WithToken not forwarded: %q", got)
	}
	if got := invoke(withBearer(context.Background(), "def")); got != "Bearer def" {
		t.Fatalf("incoming metadata not forwarded: %q", got)
	}
	if got := invoke(context.Background()); got != "" {
		t.Fatalf("a token appeared from nowhere: %q", got)
	}
}
