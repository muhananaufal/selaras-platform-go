package token_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

const issuer = "identity-svc"

func newPair(t *testing.T) (*token.Issuer, *token.Verifier, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	iss, err := token.NewIssuer(priv, issuer, 15*time.Minute)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	ver, err := token.NewVerifier(pub, issuer)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return iss, ver, priv
}

func sampleClaims(t *testing.T) domain.Claims {
	t.Helper()
	id, err := domain.ParseUserID("018f4c1e-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	return domain.Claims{
		UserID:        id,
		UserProfileID: "018f4c1e-0000-7000-8000-0000000000aa",
		Role:          domain.RoleUser,
		Generation:    7,
	}
}

func TestEveryClaimSurvivesARoundTrip(t *testing.T) {
	iss, ver, _ := newPair(t)
	want := sampleClaims(t)

	raw, err := iss.Issue(want)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	got, err := ver.Verify(raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if got.UserID != want.UserID {
		t.Errorf("user id = %s; want %s", got.UserID, want.UserID)
	}
	if got.UserProfileID != want.UserProfileID {
		t.Errorf("user profile id = %q; want %q", got.UserProfileID, want.UserProfileID)
	}
	if got.Role != want.Role {
		t.Errorf("role = %q; want %q", got.Role, want.Role)
	}
	if got.Generation != want.Generation {
		t.Errorf("generation = %d; want %d", got.Generation, want.Generation)
	}
	if got.ExpiresAt.Sub(got.IssuedAt) != 15*time.Minute {
		t.Errorf("lifetime = %v; want 15m", got.ExpiresAt.Sub(got.IssuedAt))
	}
}

// Profil boleh belum ada saat login (ADR-002 aturan 1, B7). Klaim kosong
// adalah keadaan yang sah, bukan galat.
func TestATokenCanBeIssuedBeforeAProfileExists(t *testing.T) {
	iss, ver, _ := newPair(t)
	c := sampleClaims(t)
	c.UserProfileID = ""

	raw, err := iss.Issue(c)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, err := ver.Verify(raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.UserProfileID != "" {
		t.Errorf("user profile id = %q; want empty", got.UserProfileID)
	}
}

func TestATokenSignedByAnotherKeyIsRejected(t *testing.T) {
	_, ver, _ := newPair(t)
	otherIssuer, _, _ := newPair(t)

	raw, err := otherIssuer.Issue(sampleClaims(t))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := ver.Verify(raw); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("Verify = %v; want ErrInvalidToken", err)
	}
}

func TestAnExpiredTokenIsRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	// Umur negatif menerbitkan token yang sudah lewat saat lahir, sehingga
	// test ini tidak perlu menunggu waktu berjalan.
	iss, err := token.NewIssuer(priv, issuer, -time.Minute)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	ver, err := token.NewVerifier(pub, issuer)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	raw, err := iss.Issue(sampleClaims(t))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := ver.Verify(raw); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("Verify = %v; want ErrInvalidToken", err)
	}
}

// Serangan alg=none dan pertukaran algoritma adalah cara klasik memalsukan
// JWT: penyerang mengganti header menjadi algoritma yang tidak diverifikasi,
// atau menjadi HMAC dengan kunci publik sebagai rahasianya.
func TestATokenWithASwappedAlgorithmIsRejected(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	ver, err := token.NewVerifier(pub, issuer)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	forged := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, jwtlib.MapClaims{
		"sub": "018f4c1e-0000-7000-8000-000000000001",
		"iss": issuer,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
		"gen": 7,
	})
	raw, err := forged.SignedString([]byte(pub))
	if err != nil {
		t.Fatalf("signing the forgery: %v", err)
	}

	if _, err := ver.Verify(raw); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("an HMAC token signed with the public key was accepted: %v", err)
	}
}

func TestATokenFromAnotherIssuerIsRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	iss, err := token.NewIssuer(priv, "some-other-service", time.Hour)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	ver, err := token.NewVerifier(pub, issuer)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	raw, err := iss.Issue(sampleClaims(t))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := ver.Verify(raw); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("Verify = %v; want ErrInvalidToken", err)
	}
}

func TestGarbageIsRejectedWithoutPanicking(t *testing.T) {
	_, ver, _ := newPair(t)

	for _, raw := range []string{"", "not.a.token", "a.b", strings.Repeat("x", 500)} {
		if _, err := ver.Verify(raw); !errors.Is(err, domain.ErrInvalidToken) {
			t.Errorf("Verify(%.20q) = %v; want ErrInvalidToken", raw, err)
		}
	}
}

func TestConstructorsRefuseKeysOfTheWrongSize(t *testing.T) {
	if _, err := token.NewIssuer(ed25519.PrivateKey("too short"), issuer, time.Hour); err == nil {
		t.Error("NewIssuer accepted a malformed private key")
	}
	if _, err := token.NewVerifier(ed25519.PublicKey("too short"), issuer); err == nil {
		t.Error("NewVerifier accepted a malformed public key")
	}

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := token.NewIssuer(priv, "", time.Hour); err == nil {
		t.Error("NewIssuer accepted an empty issuer")
	}
	if _, err := token.NewIssuer(priv, issuer, 0); err == nil {
		t.Error("NewIssuer accepted a zero lifetime")
	}
}

// Tanpa jti, dua token yang terbit pada detik yang sama untuk pengguna yang
// sama akan identik byte demi byte, dan log tidak bisa membedakan keduanya.
func TestTwoTokensIssuedTogetherAreNotIdentical(t *testing.T) {
	iss, _, _ := newPair(t)
	c := sampleClaims(t)

	first, err := iss.Issue(c)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	second, err := iss.Issue(c)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if first == second {
		t.Error("two tokens issued in the same second are byte for byte identical")
	}
}

func TestIssuerSatisfiesTheDomainPorts(t *testing.T) {
	iss, ver, _ := newPair(t)
	var _ domain.TokenIssuer = iss
	var _ domain.TokenVerifier = ver
}

// Ditandatangani kunci yang benar, jadi tanda tangannya sah - yang salah
// hanya isinya. Tanpa test ini, pemeriksaan generasi bisa dicabut tanpa satu
// pun test berubah merah, dan token tanpa klaim gen akan selamat dari setiap
// pencabutan.
func TestATokenWithoutAGenerationIsRejected(t *testing.T) {
	_, ver, priv := newPair(t)

	for name, gen := range map[string]int64{"missing": 0, "negative": -1} {
		t.Run(name, func(t *testing.T) {
			forged := jwtlib.NewWithClaims(jwtlib.SigningMethodEdDSA, jwtlib.MapClaims{
				"sub":  "018f4c1e-0000-7000-8000-000000000001",
				"iss":  issuer,
				"role": "user",
				"gen":  gen,
				"exp":  time.Now().Add(time.Hour).Unix(),
				"iat":  time.Now().Unix(),
			})
			raw, err := forged.SignedString(priv)
			if err != nil {
				t.Fatalf("signing: %v", err)
			}
			if _, err := ver.Verify(raw); !errors.Is(err, domain.ErrInvalidToken) {
				t.Errorf("Verify = %v; want ErrInvalidToken", err)
			}
		})
	}
}

// Peran ikut ditandatangani, jadi ia tidak bisa diubah di perjalanan - tetapi
// peran yang tidak dikenal tetap harus ditolak, bukan diteruskan sebagai
// string kosong ke pemeriksaan otorisasi.
func TestATokenWithAnUnknownRoleIsRejected(t *testing.T) {
	_, ver, priv := newPair(t)

	forged := jwtlib.NewWithClaims(jwtlib.SigningMethodEdDSA, jwtlib.MapClaims{
		"sub":  "018f4c1e-0000-7000-8000-000000000001",
		"iss":  issuer,
		"role": "superuser",
		"gen":  1,
		"exp":  time.Now().Add(time.Hour).Unix(),
		"iat":  time.Now().Unix(),
	})
	raw, err := forged.SignedString(priv)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	if _, err := ver.Verify(raw); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("Verify = %v; want ErrInvalidToken", err)
	}
}
