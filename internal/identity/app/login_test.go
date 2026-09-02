package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

type loginFixture struct {
	users    *fakeUsers
	uow      *fakeUnitOfWork
	profiles *fakeProfiles
	tokens   *fakeTokens
	hasher   *countingHasher
	login    *app.Login
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()

	users := newFakeUsers()
	uow := &fakeUnitOfWork{users: users}
	profiles := &fakeProfiles{id: "profile-1"}
	tokens := &fakeTokens{}
	hasher := &countingHasher{}

	l, err := app.NewLogin(uow, hasher, tokens, profiles, fixedClock(time.Now()))
	if err != nil {
		t.Fatalf("NewLogin: %v", err)
	}
	return &loginFixture{users: users, uow: uow, profiles: profiles, tokens: tokens, hasher: hasher, login: l}
}

// seed menaruh satu pengguna berkata sandi ke dalam penyimpanan palsu.
func (f *loginFixture) seed(t *testing.T, email, password string) *domain.User {
	t.Helper()

	hash, err := f.hasher.Hash(mustPassword(t, password))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	u, err := domain.Register(mustEmail(t, email), hash, time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := f.users.Create(context.Background(), u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.hasher.hashes = 0
	f.hasher.verifies = 0
	return u
}

func TestLoginReturnsATokenForCorrectCredentials(t *testing.T) {
	f := newLoginFixture(t)
	f.seed(t, "known@user.co", "a-long-enough-password")

	got, err := f.login.Execute(context.Background(), app.LoginCommand{
		Email:    "known@user.co",
		Password: "a-long-enough-password",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.AccessToken == "" {
		t.Error("no token was returned")
	}
	if got.UserProfileID != "profile-1" {
		t.Errorf("user profile id = %q; want %q", got.UserProfileID, "profile-1")
	}
	if f.profiles.called != 1 {
		t.Errorf("the profile service was called %d times; want exactly 1 per login", f.profiles.called)
	}
}

// D1 lewat ADR-012: satu login berhasil membatalkan seluruh sesi sebelumnya,
// dan token yang baru terbit WAJIB membawa generasi yang baru - kalau tidak,
// ia mencabut dirinya sendiri.
func TestLoginRevokesEveryPreviousSession(t *testing.T) {
	f := newLoginFixture(t)
	u := f.seed(t, "known@user.co", "a-long-enough-password")

	if _, err := f.login.Execute(context.Background(), app.LoginCommand{
		Email:    "known@user.co",
		Password: "a-long-enough-password",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stored, err := f.users.FindByID(context.Background(), u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.TokenGeneration() != 2 {
		t.Errorf("stored generation = %d; want 2", stored.TokenGeneration())
	}
	if got := f.tokens.last().Generation; got != 2 {
		t.Errorf("the new token carries generation %d; want 2, otherwise it revokes itself", got)
	}
}

// Pesan yang membedakan "email tidak terdaftar" dari "kata sandi salah"
// mengubah halaman masuk menjadi alat pencacahan akun.
func TestLoginGivesTheSameAnswerForEveryFailure(t *testing.T) {
	f := newLoginFixture(t)
	f.seed(t, "known@user.co", "a-long-enough-password")

	cases := map[string]app.LoginCommand{
		"unknown email":   {Email: "nobody@here.co", Password: "a-long-enough-password"},
		"wrong password":  {Email: "known@user.co", Password: "the-wrong-password"},
		"empty password":  {Email: "known@user.co", Password: ""},
		"malformed email": {Email: "not-an-address", Password: "a-long-enough-password"},
	}

	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.login.Execute(context.Background(), cmd)
			if !errors.Is(err, app.ErrInvalidCredentials) {
				t.Fatalf("Execute = %v; want ErrInvalidCredentials", err)
			}
			if errors.Is(err, domain.ErrUserNotFound) {
				t.Error("the error reveals that no such user exists")
			}
		})
	}
}

// Jawaban yang seragam tidak ada gunanya bila waktunya membocorkan
// jawabannya. Melewatkan verifikasi hash saat email tidak dikenal membuat
// jalur itu jauh lebih cepat, dan selisihnya cukup untuk mencacah akun.
func TestLoginHashesEvenWhenTheEmailIsUnknown(t *testing.T) {
	f := newLoginFixture(t)
	f.seed(t, "known@user.co", "a-long-enough-password")

	if _, err := f.login.Execute(context.Background(), app.LoginCommand{
		Email:    "nobody@here.co",
		Password: "a-long-enough-password",
	}); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Fatalf("Execute = %v; want ErrInvalidCredentials", err)
	}

	if f.hasher.verifies == 0 {
		t.Error("no verification ran for an unknown email; the fast path is an enumeration oracle")
	}
}

// Pengguna yang hanya punya Google memang tidak punya kata sandi. Ia harus
// ditolak lewat jalur yang sama, dan dengan biaya waktu yang sama.
func TestLoginRejectsAGoogleOnlyUserWithoutLeakingThat(t *testing.T) {
	f := newLoginFixture(t)

	u, err := domain.RegisterWithGoogle(mustEmail(t, "social@only.co"), "google-1", time.Now())
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}
	if err := f.users.Create(context.Background(), u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.hasher.verifies = 0

	if _, err := f.login.Execute(context.Background(), app.LoginCommand{
		Email:    "social@only.co",
		Password: "any-password-at-all",
	}); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Fatalf("Execute = %v; want ErrInvalidCredentials", err)
	}
	if f.hasher.verifies == 0 {
		t.Error("no verification ran for a passwordless account; the timing gives it away")
	}
}

// Kredensial yang salah DILARANG menyentuh generasi. Kalau ia menaikkannya,
// siapa pun yang tahu alamat email seseorang bisa mengeluarkan orang itu
// dari sesinya berulang kali.
func TestAFailedLoginDoesNotEndTheExistingSession(t *testing.T) {
	f := newLoginFixture(t)
	u := f.seed(t, "known@user.co", "a-long-enough-password")

	if _, err := f.login.Execute(context.Background(), app.LoginCommand{
		Email:    "known@user.co",
		Password: "the-wrong-password",
	}); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Fatalf("Execute = %v; want ErrInvalidCredentials", err)
	}

	stored, err := f.users.FindByID(context.Background(), u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.TokenGeneration() != 1 {
		t.Errorf("generation = %d after a failed login; want 1", stored.TokenGeneration())
	}
	if len(f.tokens.issued) != 0 {
		t.Error("a token was issued for a failed login")
	}
}

// ADR-002 aturan 2: profil yang belum ada berarti klaim kosong, bukan galat.
func TestLoginSucceedsWhenTheProfileServiceIsDown(t *testing.T) {
	f := newLoginFixture(t)
	f.seed(t, "known@user.co", "a-long-enough-password")
	f.profiles.err = errors.New("profile-svc is unreachable")

	got, err := f.login.Execute(context.Background(), app.LoginCommand{
		Email:    "known@user.co",
		Password: "a-long-enough-password",
	})
	if err != nil {
		t.Fatalf("login failed because the profile service did: %v", err)
	}
	if got.UserProfileID != "" {
		t.Errorf("user profile id = %q; want empty", got.UserProfileID)
	}
}

func TestLoginRefusesADeletedAccount(t *testing.T) {
	f := newLoginFixture(t)
	u := f.seed(t, "known@user.co", "a-long-enough-password")

	u.Delete(time.Now())
	if err := f.users.Update(context.Background(), u); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := f.login.Execute(context.Background(), app.LoginCommand{
		Email:    "known@user.co",
		Password: "a-long-enough-password",
	}); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Errorf("Execute = %v; want ErrInvalidCredentials", err)
	}
}

func mustPassword(t *testing.T, raw string) domain.Password {
	t.Helper()
	p, err := domain.NewPassword(raw)
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	return p
}
