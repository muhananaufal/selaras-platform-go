package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func mustEmail(t *testing.T, raw string) domain.Email {
	t.Helper()
	e, err := domain.NewEmail(raw)
	if err != nil {
		t.Fatalf("NewEmail(%q): %v", raw, err)
	}
	return e
}

func TestRoleAcceptsOnlyKnownValues(t *testing.T) {
	for _, raw := range []string{"user", "admin", "USER", " Admin "} {
		if _, err := domain.NewRole(raw); err != nil {
			t.Errorf("NewRole(%q) rejected a known role: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "superuser", "root", "moderator"} {
		if _, err := domain.NewRole(raw); !errors.Is(err, domain.ErrInvalidRole) {
			t.Errorf("NewRole(%q) = %v; want ErrInvalidRole", raw, err)
		}
	}
}

func TestRegisterDefaultsToTheLeastPrivilegedRole(t *testing.T) {
	u, err := domain.Register(mustEmail(t, "a@b.co"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.Role() != domain.RoleUser {
		t.Errorf("role = %q; want %q", u.Role(), domain.RoleUser)
	}
	if u.ID().String() == "" {
		t.Error("Register produced a user with an empty id")
	}
	if u.IsEmailVerified() {
		t.Error("a self-registered user starts verified; nothing has proven the address yet")
	}
}

func TestRegisterRejectsAnEmptyHash(t *testing.T) {
	if _, err := domain.Register(mustEmail(t, "a@b.co"), "", time.Now()); err == nil {
		t.Fatal("Register accepted an empty password hash")
	}
}

func TestRegisterWithGoogleHasNoPassword(t *testing.T) {
	u, err := domain.RegisterWithGoogle(mustEmail(t, "a@b.co"), "google-123", time.Now())
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}
	if u.CanAuthenticateWithPassword() {
		t.Error("a Google-only user can authenticate with a password")
	}
	if !u.IsEmailVerified() {
		t.Error("Google asserted the address; the user should be verified")
	}
	if u.GoogleID() != "google-123" {
		t.Errorf("google id = %q; want %q", u.GoogleID(), "google-123")
	}
}

// Menutup S5: di sistem lama, login Google memakai updateOrCreate dan menimpa
// kata sandi akun yang sudah ada dengan string acak. Menautkan identitas
// Google DILARANG menyentuh kredensial.
func TestLinkingGoogleNeverTouchesThePassword(t *testing.T) {
	u, err := domain.Register(mustEmail(t, "a@b.co"), "the-original-hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := u.LinkGoogle("google-123", time.Now()); err != nil {
		t.Fatalf("LinkGoogle: %v", err)
	}

	if got := u.PasswordHash(); got != "the-original-hash" {
		t.Errorf("password hash = %q; want it untouched", got)
	}
	if !u.CanAuthenticateWithPassword() {
		t.Error("the user lost password authentication by linking Google")
	}
	if !u.IsEmailVerified() {
		t.Error("Google asserted the address; verification should carry over")
	}
}

func TestLinkingASecondGoogleAccountIsRejected(t *testing.T) {
	u, err := domain.RegisterWithGoogle(mustEmail(t, "a@b.co"), "google-123", time.Now())
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}

	if err := u.LinkGoogle("google-123", time.Now()); err != nil {
		t.Errorf("re-linking the same account should be a no-op, got: %v", err)
	}
	if err := u.LinkGoogle("google-999", time.Now()); !errors.Is(err, domain.ErrGoogleAlreadyLinked) {
		t.Errorf("LinkGoogle(other) = %v; want ErrGoogleAlreadyLinked", err)
	}
}

func TestSetPasswordReplacesTheHash(t *testing.T) {
	u, err := domain.RegisterWithGoogle(mustEmail(t, "a@b.co"), "google-123", time.Now())
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}

	if err := u.SetPasswordHash("", time.Now()); err == nil {
		t.Error("SetPasswordHash accepted an empty hash")
	}
	if err := u.SetPasswordHash("new-hash", time.Now()); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	if !u.CanAuthenticateWithPassword() {
		t.Error("the user still cannot authenticate with a password after one was set")
	}
}

func TestDeleteIsSoftAndRepeatable(t *testing.T) {
	u, err := domain.Register(mustEmail(t, "a@b.co"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.IsDeleted() {
		t.Fatal("a fresh user reports itself deleted")
	}

	at := time.Now()
	u.Delete(at)
	if !u.IsDeleted() {
		t.Error("the user is not deleted after Delete")
	}
	if !u.DeletedAt().Equal(at) {
		t.Errorf("deleted at = %v; want %v", u.DeletedAt(), at)
	}

	u.Delete(at.Add(time.Hour))
	if !u.DeletedAt().Equal(at) {
		t.Error("a second Delete moved the original deletion time")
	}
}

// Sebuah user yang dibaca ulang dari basis data WAJIB bisa dibentuk utuh
// tanpa melewati konstruktor, kalau tidak repository terpaksa memakai
// refleksi atau domain terpaksa mengekspos bidangnya.
func TestHydrateRebuildsAUserExactly(t *testing.T) {
	id, err := domain.ParseUserID("018f4c1e-0000-7000-8000-000000000000")
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	created := time.Now().Add(-48 * time.Hour)

	u := domain.Hydrate(domain.UserState{
		ID:              id,
		Email:           mustEmail(t, "a@b.co"),
		Role:            domain.RoleAdmin,
		PasswordHash:    "stored-hash",
		GoogleID:        "google-123",
		EmailVerifiedAt: &created,
		CreatedAt:       created,
		UpdatedAt:       created,
	})

	if u.ID() != id || u.Role() != domain.RoleAdmin || u.GoogleID() != "google-123" {
		t.Errorf("Hydrate lost data: %+v", u.State())
	}
	if !u.IsEmailVerified() {
		t.Error("Hydrate dropped the verification timestamp")
	}
}

func TestParseUserIDRejectsNonsense(t *testing.T) {
	for _, raw := range []string{"", "1", "not-a-uuid", strings.Repeat("a", 36)} {
		if _, err := domain.ParseUserID(raw); err == nil {
			t.Errorf("ParseUserID(%q) accepted a malformed id", raw)
		}
	}
}

// ADR-012 lewat D1: satu login berhasil membatalkan seluruh token
// sebelumnya. Itu berarti pencabutan harus mengenai semua token seorang
// pengguna sekaligus, dan penghitung generasi melakukannya dengan satu
// tulisan alih-alih menghapus sebanyak jumlah token yang beredar.
func TestRevokingAllTokensAdvancesTheGeneration(t *testing.T) {
	u, err := domain.Register(mustEmail(t, "a@b.co"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.TokenGeneration() != 1 {
		t.Fatalf("a fresh user starts at generation %d; want 1", u.TokenGeneration())
	}

	u.RevokeAllTokens(time.Now())
	if u.TokenGeneration() != 2 {
		t.Errorf("generation = %d after one revocation; want 2", u.TokenGeneration())
	}

	u.RevokeAllTokens(time.Now())
	if u.TokenGeneration() != 3 {
		t.Errorf("generation = %d after two revocations; want 3", u.TokenGeneration())
	}
}

func TestGoogleUsersAlsoStartAtGenerationOne(t *testing.T) {
	u, err := domain.RegisterWithGoogle(mustEmail(t, "a@b.co"), "google-1", time.Now())
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}
	if u.TokenGeneration() != 1 {
		t.Errorf("generation = %d; want 1", u.TokenGeneration())
	}
}
