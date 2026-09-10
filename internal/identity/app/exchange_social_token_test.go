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
		Email:         "person@contoh.test",
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

// Closes B7 on the social path. The legacy system never created a profile
// for Google registrations at all, so the two registration paths produced
// different states for no reason.
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

// ADR-002 rule 1 applies on this path too.
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

// S5. The legacy system used updateOrCreate and overwrote an existing
// account's password with 32 random characters on every social login,
// destroying a working credential without telling anyone.
func TestSigningInWithGoogleNeverDestroysAnExistingPassword(t *testing.T) {
	f := newSocialFixture(t)
	existing := seedUser(t, f.users, "person@contoh.test")

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

// One account, not two. Linking to an existing account means no second
// account is created for the same address.
func TestLinkingDoesNotCreateASecondAccount(t *testing.T) {
	f := newSocialFixture(t)
	seedUser(t, f.users, "person@contoh.test")

	if _, err := f.exchange.Execute(context.Background(), googleIdentity()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.users.count() != 1 {
		t.Errorf("%d accounts exist; want 1", f.users.count())
	}
}

// An address the provider has not verified MUST NOT be used for linking.
// Anyone can create an account at a provider with someone else's address;
// if the provider does not state the address is proven theirs, linking by
// address is a way to take over someone's account.
func TestAnUnverifiedAddressCannotLinkToAnExistingAccount(t *testing.T) {
	f := newSocialFixture(t)
	victim := seedUser(t, f.users, "person@contoh.test")

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

// An unverified address may not create a new account either: one day the
// real owner of the address will register and find their account already
// taken.
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

// A Google identity is recognised by its sub, not by its address. The
// address at the provider can change; the sub cannot.
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

// One Google identity points at one account. If the address now matches
// another account that has linked a different Google identity, overwriting
// it would move ownership silently.
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

// D1 applies on this path too: the legacy system called tokens()->delete()
// on every successful social login.
func TestASocialSignInEndsEveryPreviousSession(t *testing.T) {
	f := newSocialFixture(t)
	existing := seedUser(t, f.users, "person@contoh.test")

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
