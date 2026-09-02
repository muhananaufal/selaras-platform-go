package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

type registerFixture struct {
	users    *fakeUsers
	uow      *fakeUnitOfWork
	profiles *fakeProfiles
	tokens   *fakeTokens
	register *app.Register
}

func newRegisterFixture(t *testing.T) *registerFixture {
	t.Helper()

	users := newFakeUsers()
	uow := &fakeUnitOfWork{users: users}
	profiles := &fakeProfiles{id: "profile-1"}
	tokens := &fakeTokens{}

	r, err := app.NewRegister(uow, fakeHasher{}, tokens, profiles, fixedClock(time.Now()))
	if err != nil {
		t.Fatalf("NewRegister: %v", err)
	}
	return &registerFixture{users: users, uow: uow, profiles: profiles, tokens: tokens, register: r}
}

func validCommand() app.RegisterCommand {
	return app.RegisterCommand{
		Email:                "new@user.co",
		Password:             "a-long-enough-password",
		PasswordConfirmation: "a-long-enough-password",
	}
}

func TestRegisterStoresTheUserAndReturnsAToken(t *testing.T) {
	f := newRegisterFixture(t)

	got, err := f.register.Execute(context.Background(), validCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got.AccessToken == "" {
		t.Error("no access token was returned")
	}
	if got.UserID == "" {
		t.Error("no user id was returned")
	}
	if f.users.count() != 1 {
		t.Errorf("%d users stored; want 1", f.users.count())
	}
	if f.uow.calls != 1 {
		t.Errorf("the unit of work ran %d times; want 1", f.uow.calls)
	}

	stored, err := f.users.FindByEmail(context.Background(), mustEmail(t, "new@user.co"))
	if err != nil {
		t.Fatalf("the user was not stored under its email: %v", err)
	}
	if stored.TokenGeneration() != 1 {
		t.Errorf("generation = %d; want 1", stored.TokenGeneration())
	}
	if f.tokens.last().Generation != 1 {
		t.Errorf("token generation = %d; want 1", f.tokens.last().Generation)
	}
	if f.tokens.last().Role != domain.RoleUser {
		t.Errorf("token role = %q; want %q", f.tokens.last().Role, domain.RoleUser)
	}
}

// Yang disimpan WAJIB hasil hashing, bukan kata sandinya. Palsuan hasher di
// sini memberi awalan "hashed:", jadi kesamaan persis dengan masukan berarti
// use case melewatkan langkah hashing sama sekali.
func TestRegisterStoresTheHashNotThePassword(t *testing.T) {
	f := newRegisterFixture(t)

	if _, err := f.register.Execute(context.Background(), validCommand()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stored, err := f.users.FindByEmail(context.Background(), mustEmail(t, "new@user.co"))
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if string(stored.PasswordHash()) == "a-long-enough-password" {
		t.Fatal("the raw password was stored as the hash")
	}
	if string(stored.PasswordHash()) != "hashed:a-long-enough-password" {
		t.Errorf("password hash = %q; want it to be the hasher's output", stored.PasswordHash())
	}
}

func TestRegisterRejectsAMismatchedConfirmation(t *testing.T) {
	f := newRegisterFixture(t)

	cmd := validCommand()
	cmd.PasswordConfirmation = "something-else-entirely"

	if _, err := f.register.Execute(context.Background(), cmd); !errors.Is(err, app.ErrPasswordMismatch) {
		t.Fatalf("Execute = %v; want ErrPasswordMismatch", err)
	}
	if f.users.count() != 0 {
		t.Error("a user was stored despite the mismatch")
	}
}

func TestRegisterRejectsAnEmailAlreadyTaken(t *testing.T) {
	f := newRegisterFixture(t)

	if _, err := f.register.Execute(context.Background(), validCommand()); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := f.register.Execute(context.Background(), validCommand()); !errors.Is(err, domain.ErrEmailTaken) {
		t.Fatalf("second Execute = %v; want ErrEmailTaken", err)
	}
	if f.users.count() != 1 {
		t.Errorf("%d users stored; want 1", f.users.count())
	}
}

// Alamat yang hanya berbeda besar-kecil huruf adalah orang yang sama.
func TestRegisterTreatsCaseVariantsAsTheSameAddress(t *testing.T) {
	f := newRegisterFixture(t)

	if _, err := f.register.Execute(context.Background(), validCommand()); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	cmd := validCommand()
	cmd.Email = "New@User.CO"
	if _, err := f.register.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrEmailTaken) {
		t.Errorf("Execute = %v; want ErrEmailTaken", err)
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	f := newRegisterFixture(t)

	cases := map[string]app.RegisterCommand{
		"empty email":    {Email: "", Password: "a-long-enough-password", PasswordConfirmation: "a-long-enough-password"},
		"no at sign":     {Email: "not-an-address", Password: "a-long-enough-password", PasswordConfirmation: "a-long-enough-password"},
		"short password": {Email: "a@b.co", Password: "short", PasswordConfirmation: "short"},
	}

	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := f.register.Execute(context.Background(), cmd); err == nil {
				t.Error("Execute accepted invalid input")
			}
		})
	}
	if f.users.count() != 0 {
		t.Errorf("%d users stored despite invalid input; want 0", f.users.count())
	}
}

// ADR-002 aturan 1, menutup B7. Profil yang gagal dibuat DILARANG
// menggagalkan registrasi: pengguna tanpa profil adalah keadaan yang sudah
// sah hari ini, dan event rekonsiliasi yang memperbaikinya belakangan.
func TestRegisterSucceedsEvenWhenTheProfileServiceIsDown(t *testing.T) {
	f := newRegisterFixture(t)
	f.profiles.err = errors.New("profile-svc is unreachable")

	got, err := f.register.Execute(context.Background(), validCommand())
	if err != nil {
		t.Fatalf("registration failed because the profile service did: %v", err)
	}
	if got.AccessToken == "" {
		t.Error("no token was issued")
	}
	if got.UserProfileID != "" {
		t.Errorf("user profile id = %q; want empty", got.UserProfileID)
	}
	if f.tokens.last().UserProfileID != "" {
		t.Errorf("the token carries a profile id that does not exist: %q", f.tokens.last().UserProfileID)
	}
	if f.users.count() != 1 {
		t.Error("the user was not stored")
	}
}

func TestRegisterPutsTheProfileIdInTheTokenWhenItExists(t *testing.T) {
	f := newRegisterFixture(t)

	got, err := f.register.Execute(context.Background(), validCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.UserProfileID != "profile-1" {
		t.Errorf("user profile id = %q; want %q", got.UserProfileID, "profile-1")
	}
	if f.tokens.last().UserProfileID != "profile-1" {
		t.Errorf("token profile id = %q; want %q", f.tokens.last().UserProfileID, "profile-1")
	}
	if f.profiles.called != 1 {
		t.Errorf("the profile service was called %d times; want 1", f.profiles.called)
	}
}

// Sebaliknya, penyimpanan yang gagal WAJIB menggagalkan registrasi. Tanpa ini
// pengguna diberi token untuk akun yang tidak pernah tersimpan.
func TestRegisterFailsWhenTheUserCannotBeStored(t *testing.T) {
	f := newRegisterFixture(t)
	f.users.failNow = errStorage

	if _, err := f.register.Execute(context.Background(), validCommand()); err == nil {
		t.Fatal("Execute succeeded even though the user could not be stored")
	}
	if len(f.tokens.issued) != 0 {
		t.Error("a token was issued for a user that was never stored")
	}
}

// Ketergantungan yang hilang harus menggagalkan penyusunan service, bukan
// permintaan pertama yang kebetulan menyentuhnya.
func TestNewRegisterRefusesMissingDependencies(t *testing.T) {
	users := newFakeUsers()
	uow := &fakeUnitOfWork{users: users}

	if _, err := app.NewRegister(nil, fakeHasher{}, &fakeTokens{}, &fakeProfiles{}, time.Now); err == nil {
		t.Error("NewRegister accepted a nil unit of work")
	}
	if _, err := app.NewRegister(uow, nil, &fakeTokens{}, &fakeProfiles{}, time.Now); err == nil {
		t.Error("NewRegister accepted a nil hasher")
	}
	if _, err := app.NewRegister(uow, fakeHasher{}, nil, &fakeProfiles{}, time.Now); err == nil {
		t.Error("NewRegister accepted a nil token issuer")
	}
	if _, err := app.NewRegister(uow, fakeHasher{}, &fakeTokens{}, nil, time.Now); err == nil {
		t.Error("NewRegister accepted a nil profile creator")
	}
}

func mustEmail(t *testing.T, raw string) domain.Email {
	t.Helper()
	e, err := domain.NewEmail(raw)
	if err != nil {
		t.Fatalf("NewEmail(%q): %v", raw, err)
	}
	return e
}
