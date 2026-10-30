package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

type mfaFixture struct {
	*loginFixture
	mfa     *app.MFA
	tickets *fakeTickets
	store   *fakeMFA
	clock   *time.Time
	user    *domain.User
}

const mfaPassword = "correct-horse-battery"

func newMFAFixture(t *testing.T) *mfaFixture {
	t.Helper()
	f := newLoginFixture(t)
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	sessions, err := app.NewSessionIssuer(f.uow, f.tokens, f.profiles, f.revocations, clock)
	if err != nil {
		t.Fatal(err)
	}
	tickets := newFakeTickets()
	m, err := app.NewMFA(f.uow, f.hasher, fakeSealer{}, tickets, sessions, &countingReader{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	f.login.WithMFA(m)
	user := f.seed(t, "ani@example.com", mfaPassword)
	return &mfaFixture{loginFixture: f, mfa: m, tickets: tickets, store: f.uow.MFA().(*fakeMFA), clock: &now, user: user}
}

func (f *mfaFixture) id() string { return f.user.ID().String() }

// enrolTOTP enrols and confirms TOTP and returns the secret and the codes.
func (f *mfaFixture) enrolTOTP(t *testing.T) ([]byte, []string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := f.mfa.StartTOTP(ctx, f.id(), app.Reauth{Password: mfaPassword}); err != nil {
		t.Fatalf("StartTOTP: %v", err)
	}
	secret := f.secret(t)
	codes, err := f.mfa.ConfirmTOTP(ctx, f.id(), domain.TOTPCode(secret, *f.clock))
	if err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}
	*f.clock = f.clock.Add(domain.TOTPPeriod) // the next code is a new step
	return secret, codes
}

func (f *mfaFixture) secret(t *testing.T) []byte {
	t.Helper()
	row, err := f.store.FindTOTP(context.Background(), f.user.ID())
	if err != nil {
		t.Fatal(err)
	}
	s, err := fakeSealer{}.Open(f.user.ID(), row.SecretCiphertext, row.KeyVersion)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *mfaFixture) generation(t *testing.T) int64 {
	t.Helper()
	u, err := f.users.FindByID(context.Background(), f.user.ID())
	if err != nil {
		t.Fatal(err)
	}
	return u.TokenGeneration()
}

func (f *mfaFixture) loginChallenge(t *testing.T) *app.Challenge {
	t.Helper()
	res, err := f.login.Execute(context.Background(), app.LoginCommand{Email: "ani@example.com", Password: mfaPassword})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Challenge == nil || res.AccessToken != "" {
		t.Fatalf("Login = %+v; want a challenge and no token", res)
	}
	return res.Challenge
}

func TestFirstFactorIssuesTenRecoveryCodes(t *testing.T) {
	f := newMFAFixture(t)
	_, codes := f.enrolTOTP(t)
	if len(codes) != domain.RecoveryCodeCount {
		t.Fatalf("got %d recovery codes; want %d", len(codes), domain.RecoveryCodeCount)
	}
	s, err := f.mfa.Status(context.Background(), f.id())
	if err != nil {
		t.Fatal(err)
	}
	if !s.TOTPEnabled || !s.MFARequired() || s.RecoveryCodesRemaining != domain.RecoveryCodeCount {
		t.Fatalf("status after enrolment = %+v", s)
	}
}

// The password step of an account with a second factor issues no token and
// bumps nothing: the owner's session survives a stolen password.
func TestAPasswordAloneNeitherSignsInNorEndsTheSession(t *testing.T) {
	f := newMFAFixture(t)
	f.enrolTOTP(t)
	before := f.generation(t)
	issued := len(f.tokens.issued)

	c := f.loginChallenge(t)
	if len(c.Methods) != 2 || c.Methods[0] != app.MFAMethodTOTP || c.Methods[1] != app.MFAMethodRecoveryCode {
		t.Fatalf("methods = %v; want TOTP and recovery code", c.Methods)
	}
	if got := f.generation(t); got != before {
		t.Fatalf("generation %d -> %d after the password step; the owner's session was ended", before, got)
	}
	if len(f.tokens.issued) != issued {
		t.Fatal("a token was issued for the password step")
	}
}

func TestTheRightCodeSignsInAndCannotBeUsedTwice(t *testing.T) {
	f := newMFAFixture(t)
	secret, _ := f.enrolTOTP(t)
	before := f.generation(t)
	ctx := context.Background()

	code := domain.TOTPCode(secret, *f.clock)
	res, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, TOTPCode: code})
	if err != nil || res.AccessToken == "" {
		t.Fatalf("Verify = %+v, %v; want a session", res, err)
	}
	if f.generation(t) != before+1 {
		t.Fatalf("generation = %d; want %d: signing in ends the previous session (D1)", f.generation(t), before+1)
	}

	// The same code, on a fresh ticket, within its 30 seconds: refused.
	if _, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, TOTPCode: code}); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("replayed code = %v; want ErrInvalidSecondFactor", err)
	}
}

func TestATicketIsSingleUseAndBounded(t *testing.T) {
	f := newMFAFixture(t)
	secret, _ := f.enrolTOTP(t)
	ctx := context.Background()

	ticket := f.loginChallenge(t).Ticket
	if _, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: ticket, TOTPCode: domain.TOTPCode(secret, *f.clock)}); err != nil {
		t.Fatal(err)
	}
	*f.clock = f.clock.Add(domain.TOTPPeriod)
	if _, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: ticket, TOTPCode: domain.TOTPCode(secret, *f.clock)}); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("an answered ticket was accepted again: %v", err)
	}

	ticket = f.loginChallenge(t).Ticket
	for i := 0; i < f.tickets.max; i++ {
		_, _ = f.mfa.Verify(ctx, app.VerifyCommand{Ticket: ticket, TOTPCode: "000000"})
	}
	if _, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: ticket, TOTPCode: domain.TOTPCode(secret, *f.clock)}); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("a right code after %d wrong ones on the same ticket = %v; want refused", f.tickets.max, err)
	}
}

func TestARecoveryCodeWorksOnce(t *testing.T) {
	f := newMFAFixture(t)
	_, codes := f.enrolTOTP(t)
	ctx := context.Background()

	if _, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, RecoveryCode: codes[3]}); err != nil {
		t.Fatalf("a recovery code was refused: %v", err)
	}
	if _, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, RecoveryCode: codes[3]}); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("a used recovery code = %v; want ErrInvalidSecondFactor", err)
	}
	s, _ := f.mfa.Status(ctx, f.id())
	if s.RecoveryCodesRemaining != domain.RecoveryCodeCount-1 {
		t.Fatalf("remaining = %d; want %d", s.RecoveryCodesRemaining, domain.RecoveryCodeCount-1)
	}
}

// Failures count per account across tickets, and they are kept even though
// the answer is an error.
func TestTenFailuresLockTheAccount(t *testing.T) {
	f := newMFAFixture(t)
	secret, _ := f.enrolTOTP(t)
	ctx := context.Background()

	for i := 0; i < domain.MFALockAfter; i++ {
		_, _ = f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, TOTPCode: "000000"})
	}
	_, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, TOTPCode: domain.TOTPCode(secret, *f.clock)})
	if !errors.Is(err, domain.ErrMFALocked) {
		t.Fatalf("the right code during the lock = %v; want ErrMFALocked", err)
	}
	*f.clock = f.clock.Add(domain.MFALockFor)
	if _, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, TOTPCode: domain.TOTPCode(secret, *f.clock)}); err != nil {
		t.Fatalf("the right code after the lock = %v", err)
	}
}

func TestAHundredFailuresDisableTOTP(t *testing.T) {
	f := newMFAFixture(t)
	f.enrolTOTP(t)
	ctx := context.Background()
	for i := 0; i < domain.MFADisableTOTPAfter; i++ {
		// Wait out each lock, which starts after every tenth failure;
		// attempts during a lock are refused without counting.
		if i > 0 && i%domain.MFALockAfter == 0 {
			*f.clock = f.clock.Add(domain.MFALockFor)
		}
		_, _ = f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, TOTPCode: "000000"})
	}
	s, _ := f.mfa.Status(ctx, f.id())
	if s.TOTPEnabled {
		t.Fatalf("TOTP still enabled after %d consecutive failures", domain.MFADisableTOTPAfter)
	}
	if s.RecoveryCodesRemaining != domain.RecoveryCodeCount {
		t.Fatal("the recovery codes went with TOTP; the owner has no way back in")
	}
}

func TestChangesNeedAFreshProof(t *testing.T) {
	f := newMFAFixture(t)
	secret, codes := f.enrolTOTP(t)
	ctx := context.Background()

	if err := f.mfa.DisableTOTP(ctx, f.id(), app.Reauth{}); !errors.Is(err, app.ErrReauthenticationRequired) {
		t.Fatalf("DisableTOTP without proof = %v", err)
	}
	if err := f.mfa.DisableTOTP(ctx, f.id(), app.Reauth{Password: "wrong-password-1"}); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("DisableTOTP with a wrong password = %v", err)
	}
	if _, err := f.mfa.RegenerateRecoveryCodes(ctx, f.id(), app.Reauth{RecoveryCode: codes[0]}); err != nil {
		t.Fatalf("RegenerateRecoveryCodes with a recovery code: %v", err)
	}
	// The old codes stopped working.
	if _, err := f.mfa.Verify(ctx, app.VerifyCommand{Ticket: f.loginChallenge(t).Ticket, RecoveryCode: codes[1]}); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("an old code after regeneration = %v", err)
	}
	if err := f.mfa.DisableTOTP(ctx, f.id(), app.Reauth{TOTPCode: domain.TOTPCode(secret, *f.clock)}); err != nil {
		t.Fatalf("DisableTOTP with a code: %v", err)
	}
	s, _ := f.mfa.Status(ctx, f.id())
	if s.MFARequired() || s.RecoveryCodesRemaining != 0 {
		t.Fatalf("status after disabling the only factor = %+v; want no factor and no codes", s)
	}
	res, err := f.login.Execute(ctx, app.LoginCommand{Email: "ani@example.com", Password: mfaPassword})
	if err != nil || res.Challenge != nil || res.AccessToken == "" {
		t.Fatalf("Login without factors = %+v, %v; want a session", res, err)
	}
}

func TestASecondEnrolmentIsRefused(t *testing.T) {
	f := newMFAFixture(t)
	f.enrolTOTP(t)
	if _, _, err := f.mfa.StartTOTP(context.Background(), f.id(), app.Reauth{Password: mfaPassword}); !errors.Is(err, app.ErrTOTPAlreadyEnabled) {
		t.Fatalf("StartTOTP over an enabled one = %v", err)
	}
}

// Without the MFA dependencies an account with a factor cannot sign in at
// all, rather than signing in with the password alone.
func TestAnAccountWithAFactorFailsClosedWithoutMFA(t *testing.T) {
	f := newMFAFixture(t)
	f.enrolTOTP(t)
	f.login.WithMFA(nil)
	res, err := f.login.Execute(context.Background(), app.LoginCommand{Email: "ani@example.com", Password: mfaPassword})
	if err == nil || res.AccessToken != "" {
		t.Fatalf("Login without MFA configured = %+v, %v; want an error and no token", res, err)
	}
}
