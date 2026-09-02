package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

type socialFixture struct {
	users    *fakeUsers
	profiles *fakeProfiles
	tokens   *fakeTokens
	revokes  *fakeRevocations
	exchange *app.ExchangeSocialToken
}

func newSocialFixture(t *testing.T) *socialFixture {
	t.Helper()

	users := newFakeUsers()
	uow := &fakeUnitOfWork{users: users, resets: newFakeResets()}
	profiles := &fakeProfiles{id: "profile-1"}
	tokens := &fakeTokens{}
	revokes := &fakeRevocations{}

	e, err := app.NewExchangeSocialToken(uow, tokens, profiles, revokes, fixedClock(time.Now()))
	if err != nil {
		t.Fatalf("NewExchangeSocialToken: %v", err)
	}
	return &socialFixture{users: users, profiles: profiles, tokens: tokens, revokes: revokes, exchange: e}
}

func googleIdentity() app.SocialIdentity {
	return app.SocialIdentity{
		Provider:      "google",
		ProviderID:    "google-sub-123",
		Email:         "person@gmail.com",
		EmailVerified: true,
	}
}

func TestAFirstTimeGoogleSignInCreatesAnAccount(t *testing.T) {
	f := newSocialFixture(t)

	got, err := f.exchange.Execute(context.Background(), googleIdentity())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.AccessToken == "" {
		t.Error("no token was returned")
	}

	stored, err := f.users.FindByGoogleID(context.Background(), "google-sub-123")
	if err != nil {
		t.Fatalf("the account was not stored: %v", err)
	}
	if stored.CanAuthenticateWithPassword() {
		t.Error("a Google-only account was given a password")
	}
	if !stored.IsEmailVerified() {
		t.Error("Google asserted the address; the account should be verified")
	}
}

// Menutup B7 di jalur sosial. Sistem lama tidak pernah membuat profil untuk
// pendaftaran lewat Google sama sekali, sehingga kedua jalur pendaftaran
// menghasilkan keadaan yang berbeda tanpa alasan.
func TestAFirstTimeGoogleSignInAlsoAsksForAProfile(t *testing.T) {
	f := newSocialFixture(t)

	got, err := f.exchange.Execute(context.Background(), googleIdentity())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.profiles.called != 1 {
		t.Errorf("the profile service was called %d times; want 1", f.profiles.called)
	}
	if got.UserProfileID != "profile-1" {
		t.Errorf("user profile id = %q; want %q", got.UserProfileID, "profile-1")
	}
}

// ADR-002 aturan 1 berlaku di jalur ini juga.
func TestAFirstTimeGoogleSignInSurvivesADeadProfileService(t *testing.T) {
	f := newSocialFixture(t)
	f.profiles.err = errors.New("profile-svc is unreachable")

	got, err := f.exchange.Execute(context.Background(), googleIdentity())
	if err != nil {
		t.Fatalf("sign-in failed because the profile service did: %v", err)
	}
	if got.UserProfileID != "" {
		t.Errorf("user profile id = %q; want empty", got.UserProfileID)
	}
}

// S5. Sistem lama memakai updateOrCreate dan menimpa kata sandi akun yang
// sudah ada dengan 32 karakter acak setiap kali login sosial, menghancurkan
// kredensial yang berfungsi tanpa memberi tahu siapa pun.
func TestSigningInWithGoogleNeverDestroysAnExistingPassword(t *testing.T) {
	f := newSocialFixture(t)
	existing := seedUser(t, f.users, "person@gmail.com")

	if _, err := f.exchange.Execute(context.Background(), googleIdentity()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stored, err := f.users.FindByID(context.Background(), existing.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if string(stored.PasswordHash()) != "hashed:whatever" {
		t.Errorf("password hash = %q; want it untouched", stored.PasswordHash())
	}
	if !stored.CanAuthenticateWithPassword() {
		t.Error("the account lost password authentication")
	}
	if stored.GoogleID() != "google-sub-123" {
		t.Errorf("google id = %q; want it linked", stored.GoogleID())
	}
}

// Satu akun, bukan dua. Menautkan ke akun yang sudah ada berarti tidak ada
// akun kedua yang dibuat untuk alamat yang sama.
func TestLinkingDoesNotCreateASecondAccount(t *testing.T) {
	f := newSocialFixture(t)
	seedUser(t, f.users, "person@gmail.com")

	if _, err := f.exchange.Execute(context.Background(), googleIdentity()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.users.count() != 1 {
		t.Errorf("%d accounts exist; want 1", f.users.count())
	}
}

// Alamat yang belum diverifikasi penyedia DILARANG dipakai untuk menautkan.
// Siapa pun bisa membuat akun di penyedia dengan alamat orang lain; kalau
// penyedianya tidak menyatakan alamat itu terbukti miliknya, menautkan
// berdasarkan alamat adalah cara mengambil alih akun orang.
func TestAnUnverifiedAddressCannotLinkToAnExistingAccount(t *testing.T) {
	f := newSocialFixture(t)
	victim := seedUser(t, f.users, "person@gmail.com")

	identity := googleIdentity()
	identity.EmailVerified = false

	if _, err := f.exchange.Execute(context.Background(), identity); !errors.Is(err, app.ErrEmailNotVerifiedByProvider) {
		t.Fatalf("Execute = %v; want ErrEmailNotVerifiedByProvider", err)
	}

	stored, err := f.users.FindByID(context.Background(), victim.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.GoogleID() != "" {
		t.Error("an unverified address was linked to an existing account")
	}
}

// Alamat yang belum diverifikasi juga tidak boleh membuat akun baru: kelak
// pemilik alamat yang sebenarnya akan mendaftar dan menemukan akunnya sudah
// ditempati.
func TestAnUnverifiedAddressCannotCreateAnAccountEither(t *testing.T) {
	f := newSocialFixture(t)

	identity := googleIdentity()
	identity.EmailVerified = false

	if _, err := f.exchange.Execute(context.Background(), identity); !errors.Is(err, app.ErrEmailNotVerifiedByProvider) {
		t.Fatalf("Execute = %v; want ErrEmailNotVerifiedByProvider", err)
	}
	if f.users.count() != 0 {
		t.Error("an account was created from an unverified address")
	}
}

// Identitas Google dikenali lewat sub-nya, bukan lewat alamatnya. Alamat di
// penyedia bisa berubah; sub tidak.
func TestAReturningUserIsFoundByProviderIdNotByEmail(t *testing.T) {
	f := newSocialFixture(t)

	if _, err := f.exchange.Execute(context.Background(), googleIdentity()); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	moved := googleIdentity()
	moved.Email = "person@newdomain.co"

	if _, err := f.exchange.Execute(context.Background(), moved); err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if f.users.count() != 1 {
		t.Errorf("%d accounts exist after the address changed; want 1", f.users.count())
	}
	if f.profiles.called != 1 {
		t.Errorf("the profile service was called %d times; want 1, only for the first sign-in", f.profiles.called)
	}
}

// Satu identitas Google menunjuk ke satu akun. Kalau alamatnya kini cocok
// dengan akun lain yang sudah menautkan Google yang berbeda, menimpanya akan
// memindahkan kepemilikan diam-diam.
func TestASecondGoogleIdentityCannotOverwriteTheFirst(t *testing.T) {
	f := newSocialFixture(t)

	if _, err := f.exchange.Execute(context.Background(), googleIdentity()); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	other := googleIdentity()
	other.ProviderID = "google-sub-999"

	if _, err := f.exchange.Execute(context.Background(), other); !errors.Is(err, domain.ErrGoogleAlreadyLinked) {
		t.Errorf("Execute = %v; want ErrGoogleAlreadyLinked", err)
	}
}

// D1 berlaku di jalur ini juga: sistem lama memanggil tokens()->delete()
// setiap kali login sosial berhasil.
func TestASocialSignInEndsEveryPreviousSession(t *testing.T) {
	f := newSocialFixture(t)
	existing := seedUser(t, f.users, "person@gmail.com")

	if _, err := f.exchange.Execute(context.Background(), googleIdentity()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stored, err := f.users.FindByID(context.Background(), existing.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.TokenGeneration() != 2 {
		t.Errorf("generation = %d; want 2", stored.TokenGeneration())
	}
	if got := f.tokens.last().Generation; got != 2 {
		t.Errorf("the new token carries generation %d; want 2", got)
	}
}

func TestAnUnsupportedProviderIsRejected(t *testing.T) {
	f := newSocialFixture(t)

	for _, provider := range []string{"", "facebook", "GOOGLE ", "twitter"} {
		identity := googleIdentity()
		identity.Provider = provider

		if _, err := f.exchange.Execute(context.Background(), identity); !errors.Is(err, app.ErrUnsupportedProvider) {
			t.Errorf("Execute(provider=%q) = %v; want ErrUnsupportedProvider", provider, err)
		}
	}
	if f.users.count() != 0 {
		t.Error("an account was created for an unsupported provider")
	}
}

func TestAnIncompleteIdentityIsRejected(t *testing.T) {
	f := newSocialFixture(t)

	missingID := googleIdentity()
	missingID.ProviderID = ""
	if _, err := f.exchange.Execute(context.Background(), missingID); err == nil {
		t.Error("Execute accepted an identity without a provider id")
	}

	badEmail := googleIdentity()
	badEmail.Email = "not-an-address"
	if _, err := f.exchange.Execute(context.Background(), badEmail); err == nil {
		t.Error("Execute accepted an identity with a malformed address")
	}

	if f.users.count() != 0 {
		t.Error("an account was created from an incomplete identity")
	}
}

func TestNewExchangeSocialTokenRefusesMissingDependencies(t *testing.T) {
	uow := &fakeUnitOfWork{users: newFakeUsers(), resets: newFakeResets()}

	if _, err := app.NewExchangeSocialToken(nil, &fakeTokens{}, &fakeProfiles{}, &fakeRevocations{}, time.Now); err == nil {
		t.Error("accepted a nil unit of work")
	}
	if _, err := app.NewExchangeSocialToken(uow, nil, &fakeProfiles{}, &fakeRevocations{}, time.Now); err == nil {
		t.Error("accepted a nil token issuer")
	}
	if _, err := app.NewExchangeSocialToken(uow, &fakeTokens{}, nil, &fakeRevocations{}, time.Now); err == nil {
		t.Error("accepted a nil profile creator")
	}
	if _, err := app.NewExchangeSocialToken(uow, &fakeTokens{}, &fakeProfiles{}, nil, time.Now); err == nil {
		t.Error("accepted a nil revocation publisher")
	}
}
