package social_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/social"
)

const (
	clientID = "selaras-client-id.apps.googleusercontent.com"
	keyID    = "test-key-1"
)

// provider berdiri untuk Google: ia menerbitkan JWKS dan menandatangani
// token, sehingga seluruh verifikasi diuji tanpa menyentuh jaringan.
type provider struct {
	key      *rsa.PrivateKey
	server   *httptest.Server
	requests int
}

func newProvider(t *testing.T) *provider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	p := &provider{key: key}

	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.requests++
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": keyID,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}},
		}); err != nil {
			t.Errorf("encoding jwks: %v", err)
		}
	}))
	t.Cleanup(p.server.Close)

	return p
}

type tokenOptions struct {
	issuer        string
	audience      string
	subject       string
	email         string
	emailVerified any
	expiresAt     time.Time
	kid           string
	signWith      *rsa.PrivateKey
}

func (p *provider) issue(t *testing.T, opts tokenOptions) string {
	t.Helper()

	if opts.issuer == "" {
		opts.issuer = "https://accounts.google.com"
	}
	if opts.audience == "" {
		opts.audience = clientID
	}
	if opts.subject == "" {
		opts.subject = "google-sub-123"
	}
	if opts.email == "" {
		opts.email = "person@contoh.test"
	}
	if opts.emailVerified == nil {
		opts.emailVerified = true
	}
	if opts.expiresAt.IsZero() {
		opts.expiresAt = time.Now().Add(time.Hour)
	}
	if opts.kid == "" {
		opts.kid = keyID
	}
	if opts.signWith == nil {
		opts.signWith = p.key
	}

	claims := jwt.MapClaims{
		"iss":            opts.issuer,
		"aud":            opts.audience,
		"sub":            opts.subject,
		"email":          opts.email,
		"email_verified": opts.emailVerified,
		"exp":            opts.expiresAt.Unix(),
		"iat":            time.Now().Add(-time.Minute).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = opts.kid

	signed, err := token.SignedString(opts.signWith)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	return signed
}

func newVerifier(t *testing.T, p *provider) *social.GoogleVerifier {
	t.Helper()

	v, err := social.NewGoogleVerifier(clientID, p.server.URL, p.server.Client(), time.Hour)
	if err != nil {
		t.Fatalf("NewGoogleVerifier: %v", err)
	}
	return v
}

func TestAValidIDTokenYieldsTheIdentity(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	identity, err := v.Verify(context.Background(), "google", p.issue(t, tokenOptions{}))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if identity.Provider != "google" {
		t.Errorf("provider = %q; want google", identity.Provider)
	}
	if identity.ProviderID != "google-sub-123" {
		t.Errorf("provider id = %q; want the subject", identity.ProviderID)
	}
	if identity.Email != "person@contoh.test" {
		t.Errorf("email = %q", identity.Email)
	}
	if !identity.EmailVerified {
		t.Error("email_verified was lost")
	}
}

// Kedua bentuk penerbit Google sama-sama sah, dan menerima hanya satu akan
// menolak setengah token yang benar.
func TestBothGoogleIssuerFormsAreAccepted(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	for _, issuer := range []string{"https://accounts.google.com", "accounts.google.com"} {
		if _, err := v.Verify(context.Background(), "google", p.issue(t, tokenOptions{issuer: issuer})); err != nil {
			t.Errorf("issuer %q was rejected: %v", issuer, err)
		}
	}
}

// Audience adalah pemeriksaan yang paling sering terlewat, dan tanpanya siapa
// pun yang punya aplikasi Google bisa menukar token penggunanya menjadi sesi
// di sistem ini.
func TestATokenIssuedForAnotherApplicationIsRefused(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	token := p.issue(t, tokenOptions{audience: "somebody-else.apps.googleusercontent.com"})
	if _, err := v.Verify(context.Background(), "google", token); !errors.Is(err, social.ErrInvalidIDToken) {
		t.Errorf("Verify = %v; want ErrInvalidIDToken", err)
	}
}

func TestATokenFromAnotherIssuerIsRefused(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	token := p.issue(t, tokenOptions{issuer: "https://accounts.example.com"})
	if _, err := v.Verify(context.Background(), "google", token); !errors.Is(err, social.ErrInvalidIDToken) {
		t.Errorf("Verify = %v; want ErrInvalidIDToken", err)
	}
}

func TestATokenSignedByAnotherKeyIsRefused(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	token := p.issue(t, tokenOptions{signWith: other})
	if _, err := v.Verify(context.Background(), "google", token); !errors.Is(err, social.ErrInvalidIDToken) {
		t.Errorf("Verify = %v; want ErrInvalidIDToken", err)
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	token := p.issue(t, tokenOptions{expiresAt: time.Now().Add(-time.Minute)})
	if _, err := v.Verify(context.Background(), "google", token); !errors.Is(err, social.ErrInvalidIDToken) {
		t.Errorf("Verify = %v; want ErrInvalidIDToken", err)
	}
}

func TestATokenNamingAnUnknownKeyIsRefused(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	token := p.issue(t, tokenOptions{kid: "a-key-that-does-not-exist"})
	if _, err := v.Verify(context.Background(), "google", token); !errors.Is(err, social.ErrInvalidIDToken) {
		t.Errorf("Verify = %v; want ErrInvalidIDToken", err)
	}
}

// email_verified adalah tumpuan pengerasan di F1-11. Bentuk yang tidak
// terduga WAJIB menjadi penolakan, bukan diam-diam menjadi false - dan sama
// sekali bukan diam-diam menjadi true.
func TestEmailVerifiedIsReadInBothShapesAndNothingElse(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	cases := map[string]struct {
		raw  any
		want bool
	}{
		"boolean true":  {true, true},
		"boolean false": {false, false},
		"string true":   {"true", true},
		"string false":  {"false", false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			identity, err := v.Verify(context.Background(), "google",
				p.issue(t, tokenOptions{emailVerified: c.raw}))
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if identity.EmailVerified != c.want {
				t.Errorf("email verified = %v; want %v", identity.EmailVerified, c.want)
			}
		})
	}

	t.Run("nonsense", func(t *testing.T) {
		_, err := v.Verify(context.Background(), "google",
			p.issue(t, tokenOptions{emailVerified: "perhaps"}))
		if !errors.Is(err, social.ErrInvalidIDToken) {
			t.Errorf("Verify = %v; want ErrInvalidIDToken", err)
		}
	})
}

func TestAnUnsupportedProviderIsRefusedWithoutTouchingTheNetwork(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	before := p.requests
	if _, err := v.Verify(context.Background(), "facebook", "whatever"); !errors.Is(err, social.ErrUnsupportedProvider) {
		t.Errorf("Verify = %v; want ErrUnsupportedProvider", err)
	}
	if p.requests != before {
		t.Error("an unsupported provider still caused a jwks fetch")
	}
}

// Tanpa cache, setiap masuk lewat Google berarti satu permintaan HTTP ke
// penyedia sebelum apa pun bisa diverifikasi.
func TestTheKeySetIsFetchedOnceAndReused(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	for range 5 {
		if _, err := v.Verify(context.Background(), "google", p.issue(t, tokenOptions{})); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	}
	if p.requests != 1 {
		t.Errorf("the key set was fetched %d times; want 1", p.requests)
	}
}

// Penyedia merotasi kuncinya, dan kunci baru muncul sebelum salinan lama
// kedaluwarsa. kid yang tidak dikenal karena itu memicu pengambilan ulang.
func TestAnUnknownKeyIdTriggersARefetch(t *testing.T) {
	p := newProvider(t)
	v := newVerifier(t, p)

	if _, err := v.Verify(context.Background(), "google", p.issue(t, tokenOptions{})); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.requests != 1 {
		t.Fatalf("requests = %d; want 1", p.requests)
	}

	if _, err := v.Verify(context.Background(), "google", p.issue(t, tokenOptions{kid: "rotated"})); err == nil {
		t.Fatal("a token naming an unknown key was accepted")
	}
	if p.requests != 2 {
		t.Errorf("requests = %d; want 2, the unknown key should have forced a refetch", p.requests)
	}
}

func TestAnUnreachableProviderIsRefused(t *testing.T) {
	p := newProvider(t)
	token := p.issue(t, tokenOptions{})
	p.server.Close()

	v := newVerifier(t, p)
	if _, err := v.Verify(context.Background(), "google", token); !errors.Is(err, social.ErrInvalidIDToken) {
		t.Errorf("Verify = %v; want ErrInvalidIDToken", err)
	}
}

func TestNewGoogleVerifierRefusesAnEmptyClientID(t *testing.T) {
	if _, err := social.NewGoogleVerifier("", "", nil, time.Hour); err == nil {
		t.Error("NewGoogleVerifier accepted an empty client id")
	}
	if _, err := social.NewGoogleVerifier("   ", "", nil, time.Hour); err == nil {
		t.Error("NewGoogleVerifier accepted a blank client id")
	}
}

// Verifier yang tidak dikonfigurasi menolak dengan menyebut apa yang kurang,
// bukan berpura-pura berhasil.
func TestTheUnconfiguredVerifierSaysWhatIsMissing(t *testing.T) {
	_, err := social.Unconfigured{}.Verify(context.Background(), "google", "whatever")
	if err == nil {
		t.Fatal("the unconfigured verifier accepted a token")
	}
	if !contains(err.Error(), "GOOGLE_CLIENT_ID") {
		t.Errorf("error = %q; want it to name the missing configuration", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(haystack) > 0 && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
