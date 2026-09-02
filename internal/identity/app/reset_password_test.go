package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

type resetFixture struct {
	users   *fakeUsers
	resets  *fakeResets
	links   *fakeResetLinks
	revokes *fakeRevocations
	clock   time.Time
	request *app.RequestPasswordReset
	confirm *app.ConfirmPasswordReset
}

func newResetFixture(t *testing.T) *resetFixture {
	t.Helper()

	users := newFakeUsers()
	resets := newFakeResets()
	uow := &fakeUnitOfWork{users: users, resets: resets}
	links := &fakeResetLinks{}
	revokes := &fakeRevocations{}
	now := time.Now()

	request, err := app.NewRequestPasswordReset(uow, links, fixedClock(now))
	if err != nil {
		t.Fatalf("NewRequestPasswordReset: %v", err)
	}
	confirm, err := app.NewConfirmPasswordReset(uow, fakeHasher{}, revokes, fixedClock(now))
	if err != nil {
		t.Fatalf("NewConfirmPasswordReset: %v", err)
	}

	return &resetFixture{
		users: users, resets: resets, links: links, revokes: revokes,
		clock: now, request: request, confirm: confirm,
	}
}

func (f *resetFixture) seedUser(t *testing.T, email string) *domain.User {
	t.Helper()
	return seedUser(t, f.users, email)
}

// requestAndCapture menjalankan permintaan reset dan mengembalikan token yang
// dikirim, seperti yang akan diterima pengguna lewat surel.
func (f *resetFixture) requestAndCapture(t *testing.T, email string) string {
	t.Helper()

	if err := f.request.Execute(context.Background(), app.RequestPasswordResetCommand{Email: email}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(f.links.sent) == 0 {
		t.Fatal("no reset link was sent")
	}
	return f.links.sent[len(f.links.sent)-1].token
}

func TestRequestingAResetSendsALinkToAKnownAddress(t *testing.T) {
	f := newResetFixture(t)
	u := f.seedUser(t, "known@user.co")

	token := f.requestAndCapture(t, "known@user.co")
	if token == "" {
		t.Fatal("an empty token was sent")
	}
	if got := f.links.sent[0].email; got != "known@user.co" {
		t.Errorf("sent to %q; want %q", got, "known@user.co")
	}
	if f.resets.count() != 1 {
		t.Errorf("%d reset requests stored; want 1", f.resets.count())
	}
	if f.resets.only(t).UserID != u.ID() {
		t.Error("the stored request belongs to the wrong user")
	}
}

// Menutup separuh S1. Yang disimpan WAJIB hash-nya: sebuah dump basis data
// tidak boleh langsung berarti kemampuan mengambil alih setiap akun yang
// sedang punya permintaan reset yang beredar.
func TestTheTokenItselfIsNeverStored(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")

	token := f.requestAndCapture(t, "known@user.co")

	parsed, err := domain.ParseResetToken(token)
	if err != nil {
		t.Fatalf("ParseResetToken: %v", err)
	}
	stored := f.resets.only(t)
	if !stored.TokenHash.Equal(domain.HashResetToken(parsed)) {
		t.Error("the stored hash does not match the token that was sent")
	}
	for _, b := range stored.TokenHash {
		if b != 0 {
			return // hash-nya terisi, artinya bukan nilai nol
		}
	}
	t.Error("the stored hash is all zeroes")
}

// Endpoint ini menjawab sama untuk alamat yang terdaftar dan yang tidak.
// Membedakannya mengubahnya menjadi alat pencacahan akun - dan itu justru
// yang dilakukan sistem lama lewat aturan `exists:users,email`.
func TestRequestingAResetForAnUnknownAddressLooksIdentical(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")

	for name, email := range map[string]string{
		"unknown address": "nobody@here.co",
		"malformed input": "not-an-address",
		"empty input":     "",
	} {
		t.Run(name, func(t *testing.T) {
			if err := f.request.Execute(context.Background(), app.RequestPasswordResetCommand{Email: email}); err != nil {
				t.Errorf("Execute = %v; want nil, otherwise the answer distinguishes them", err)
			}
		})
	}

	if f.resets.count() != 0 {
		t.Errorf("%d reset requests stored for addresses that do not exist; want 0", f.resets.count())
	}
	if len(f.links.sent) != 0 {
		t.Error("a link was sent for an address that does not exist")
	}
}

// Surel yang gagal terkirim DILARANG dilaporkan ke pemanggil: pengiriman
// hanya pernah dicoba untuk alamat yang terdaftar, jadi galatnya sendiri
// mengumumkan bahwa alamatnya ada.
func TestAFailedSendIsNotReportedToTheCaller(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")
	f.links.err = errors.New("the mail provider is down")

	if err := f.request.Execute(context.Background(), app.RequestPasswordResetCommand{Email: "known@user.co"}); err != nil {
		t.Errorf("Execute = %v; want nil, otherwise a send failure reveals the address exists", err)
	}
}

func TestConfirmingAResetChangesThePassword(t *testing.T) {
	f := newResetFixture(t)
	u := f.seedUser(t, "known@user.co")
	token := f.requestAndCapture(t, "known@user.co")

	err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                token,
		Password:             "a-brand-new-password",
		PasswordConfirmation: "a-brand-new-password",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stored, err := f.users.FindByID(context.Background(), u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if string(stored.PasswordHash()) != "hashed:a-brand-new-password" {
		t.Errorf("password hash = %q; want the new password hashed", stored.PasswordHash())
	}
}

// Kalau akunnya direbut, sesi si perebut WAJIB mati bersama kata sandinya.
// Reset yang tidak mencabut sesi hanya mengubah kata sandi sambil membiarkan
// penyerangnya tetap masuk.
func TestConfirmingAResetEndsEverySession(t *testing.T) {
	f := newResetFixture(t)
	u := f.seedUser(t, "known@user.co")
	token := f.requestAndCapture(t, "known@user.co")

	if err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                token,
		Password:             "a-brand-new-password",
		PasswordConfirmation: "a-brand-new-password",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stored, err := f.users.FindByID(context.Background(), u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.TokenGeneration() != 2 {
		t.Errorf("generation = %d after a reset; want 2", stored.TokenGeneration())
	}
	if len(f.revokes.published) != 1 {
		t.Errorf("%d generations published; want 1", len(f.revokes.published))
	}
}

// Menutup S1 sepenuhnya di sisi ini: token sekali pakai.
func TestATokenCannotBeUsedTwice(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")
	token := f.requestAndCapture(t, "known@user.co")

	cmd := app.ConfirmPasswordResetCommand{
		Token:                token,
		Password:             "a-brand-new-password",
		PasswordConfirmation: "a-brand-new-password",
	}
	if err := f.confirm.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	cmd.Password = "yet-another-password"
	cmd.PasswordConfirmation = "yet-another-password"
	if err := f.confirm.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("second Execute = %v; want ErrResetTokenInvalid", err)
	}
}

// Permintaan lain yang masih beredar adalah kredensial yang masih berlaku
// atas akun yang baru saja diamankan, dan yang paling mungkin menerbitkannya
// adalah orang yang sedang mencoba merebutnya.
func TestConfirmingAResetKillsEveryOtherOutstandingRequest(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")

	first := f.requestAndCapture(t, "known@user.co")
	second := f.requestAndCapture(t, "known@user.co")

	if err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                second,
		Password:             "a-brand-new-password",
		PasswordConfirmation: "a-brand-new-password",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                first,
		Password:             "an-attackers-password",
		PasswordConfirmation: "an-attackers-password",
	}); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("an older outstanding token still worked: %v", err)
	}
}

func TestConfirmingRejectsEveryBadTokenTheSameWay(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")

	valid, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}

	for name, token := range map[string]string{
		"never issued": valid.Expose(),
		"empty":        "",
		"garbage":      "not-a-token-at-all",
	} {
		t.Run(name, func(t *testing.T) {
			err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
				Token:                token,
				Password:             "a-brand-new-password",
				PasswordConfirmation: "a-brand-new-password",
			})
			if !errors.Is(err, domain.ErrResetTokenInvalid) {
				t.Errorf("Execute = %v; want ErrResetTokenInvalid", err)
			}
		})
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")
	token := f.requestAndCapture(t, "known@user.co")

	// Konfirmasi dijalankan dengan jam yang sudah lewat masa berlakunya.
	confirm, err := app.NewConfirmPasswordReset(
		&fakeUnitOfWork{users: f.users, resets: f.resets},
		fakeHasher{},
		f.revokes,
		fixedClock(f.clock.Add(48*time.Hour)),
	)
	if err != nil {
		t.Fatalf("NewConfirmPasswordReset: %v", err)
	}

	if err := confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                token,
		Password:             "a-brand-new-password",
		PasswordConfirmation: "a-brand-new-password",
	}); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("Execute = %v; want ErrResetTokenInvalid", err)
	}
}

func TestConfirmingRejectsAMismatchedConfirmation(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")
	token := f.requestAndCapture(t, "known@user.co")

	if err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                token,
		Password:             "a-brand-new-password",
		PasswordConfirmation: "a-different-password",
	}); !errors.Is(err, app.ErrPasswordMismatch) {
		t.Errorf("Execute = %v; want ErrPasswordMismatch", err)
	}
}

// Kata sandi yang ditolak DILARANG menghanguskan tokennya: pengguna yang
// salah ketik masih berhak memakai tautan yang ia terima.
func TestARejectedPasswordLeavesTheTokenUsable(t *testing.T) {
	f := newResetFixture(t)
	f.seedUser(t, "known@user.co")
	token := f.requestAndCapture(t, "known@user.co")

	if err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                token,
		Password:             "short",
		PasswordConfirmation: "short",
	}); err == nil {
		t.Fatal("a password below the minimum length was accepted")
	}

	if err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                token,
		Password:             "a-brand-new-password",
		PasswordConfirmation: "a-brand-new-password",
	}); err != nil {
		t.Errorf("the token was burned by a rejected password: %v", err)
	}
}

// Pengguna yang selama ini hanya memakai Google boleh menetapkan kata sandi
// lewat jalur ini - alamatnya sudah terbukti miliknya oleh Google.
func TestAGoogleOnlyUserCanSetAPasswordThisWay(t *testing.T) {
	f := newResetFixture(t)

	u, err := domain.RegisterWithGoogle(mustEmail(t, "social@only.co"), "google-1", f.clock)
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}
	if err := f.users.Create(context.Background(), u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	token := f.requestAndCapture(t, "social@only.co")
	if err := f.confirm.Execute(context.Background(), app.ConfirmPasswordResetCommand{
		Token:                token,
		Password:             "a-brand-new-password",
		PasswordConfirmation: "a-brand-new-password",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stored, err := f.users.FindByID(context.Background(), u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if !stored.CanAuthenticateWithPassword() {
		t.Error("the account still cannot authenticate with a password")
	}
	if stored.GoogleID() != "google-1" {
		t.Error("setting a password unlinked the Google identity")
	}
}

func TestResetConstructorsRefuseMissingDependencies(t *testing.T) {
	uow := &fakeUnitOfWork{users: newFakeUsers(), resets: newFakeResets()}

	if _, err := app.NewRequestPasswordReset(nil, &fakeResetLinks{}, time.Now); err == nil {
		t.Error("NewRequestPasswordReset accepted a nil unit of work")
	}
	if _, err := app.NewRequestPasswordReset(uow, nil, time.Now); err == nil {
		t.Error("NewRequestPasswordReset accepted a nil link sender")
	}
	if _, err := app.NewConfirmPasswordReset(uow, nil, &fakeRevocations{}, time.Now); err == nil {
		t.Error("NewConfirmPasswordReset accepted a nil hasher")
	}
	if _, err := app.NewConfirmPasswordReset(uow, fakeHasher{}, nil, time.Now); err == nil {
		t.Error("NewConfirmPasswordReset accepted a nil revocation publisher")
	}
}
