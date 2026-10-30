package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Second factors (ADR-032).

var (
	// ErrReauthenticationRequired answers a change to a factor that came
	// without a proof, from an account that has something to prove with.
	ErrReauthenticationRequired = errors.New("this change needs your password, an authenticator code or a recovery code")

	// ErrTOTPAlreadyEnabled answers a second enrolment: replacing a working
	// secret silently would lock the owner out of the old one mid-flight.
	ErrTOTPAlreadyEnabled = errors.New("an authenticator app is already enrolled; disable it first")

	// ErrNoPendingTOTP answers a confirmation without a started enrolment.
	ErrNoPendingTOTP = errors.New("no authenticator enrolment is in progress")

	// ErrMFANotEnabled answers a recovery-code request from an account with
	// no second factor: the codes would bypass nothing.
	ErrMFANotEnabled = errors.New("no second factor is enabled")

	// errMFANotConfigured is the fail-closed answer when an account has a
	// factor but this process was started without the MFA dependencies.
	errMFANotConfigured = errors.New("the account requires a second factor, but MFA is not configured in this process")
)

// SecretSealer seals TOTP secrets at rest (AES-256-GCM in production) with
// the user id as associated data, so a sealed secret copied to another
// account does not open.
type SecretSealer interface {
	Seal(userID domain.UserID, plaintext []byte) (ciphertext []byte, keyVersion int64, err error)
	Open(userID domain.UserID, ciphertext []byte, keyVersion int64) ([]byte, error)
}

// TicketStore keeps the tickets of the second sign-in step.
type TicketStore interface {
	// Create issues a ticket for userID.
	Create(ctx context.Context, userID domain.UserID) (string, error)
	// Use counts one attempt and returns the ticket's user. An unknown,
	// expired or exhausted ticket is domain.ErrInvalidSecondFactor.
	Use(ctx context.Context, ticket string) (domain.UserID, error)
	// Delete ends a ticket once it has been answered.
	Delete(ctx context.Context, ticket string) error
	// TTL is how long a ticket lives, announced to the client.
	TTL() time.Duration
}

// MFAMethod is a way to answer a challenge.
type MFAMethod int

const (
	MFAMethodTOTP MFAMethod = iota + 1
	MFAMethodRecoveryCode
	MFAMethodPasskey
)

// Challenge is the second step of a sign-in.
type Challenge struct {
	Ticket    string
	Methods   []MFAMethod
	ExpiresIn time.Duration
}

// Reauth is the proof a change to a factor carries; at most one field is
// set.
type Reauth struct {
	Password     string
	TOTPCode     string
	RecoveryCode string
}

func (r Reauth) empty() bool { return r.Password == "" && r.TOTPCode == "" && r.RecoveryCode == "" }

// SecurityStatus is what GetSecurityStatus reports.
type SecurityStatus struct {
	TOTPEnabled            bool
	PasskeyCount           int
	RecoveryCodesRemaining int
}

// MFARequired reports whether signing in asks for a second factor.
func (s SecurityStatus) MFARequired() bool { return s.TOTPEnabled || s.PasskeyCount > 0 }

// factorsOf reads an account's factors inside a unit of work.
func factorsOf(ctx context.Context, mfa domain.MFARepository, id domain.UserID) (SecurityStatus, error) {
	var s SecurityStatus
	totp, err := mfa.FindTOTP(ctx, id)
	switch {
	case err == nil:
		s.TOTPEnabled = totp.Confirmed()
	case !errors.Is(err, domain.ErrNoTOTP):
		return SecurityStatus{}, err
	}
	if s.PasskeyCount, err = mfa.CountPasskeys(ctx, id); err != nil {
		return SecurityStatus{}, err
	}
	if s.RecoveryCodesRemaining, err = mfa.CountUnusedRecoveryCodes(ctx, id); err != nil {
		return SecurityStatus{}, err
	}
	return s, nil
}

// SessionIssuer ends the account's previous session and issues a new token:
// the last step of every successful sign-in (D1), shared so a password, a
// second factor and a passkey all sign in the same way.
type SessionIssuer struct {
	uow         UnitOfWork
	tokens      domain.TokenIssuer
	profiles    ProfileFinder
	revocations domain.RevocationPublisher
	now         func() time.Time
}

func NewSessionIssuer(
	uow UnitOfWork, tokens domain.TokenIssuer, profiles ProfileFinder,
	revocations domain.RevocationPublisher, now func() time.Time,
) (*SessionIssuer, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case tokens == nil:
		return nil, errors.New("nil token issuer")
	case profiles == nil:
		return nil, errors.New("nil profile finder")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &SessionIssuer{uow: uow, tokens: tokens, profiles: profiles, revocations: revocations, now: now}, nil
}

// Issue bumps the generation and signs id in. Reading and bumping happen in
// one unit of work, so two sign-ins at once cannot both mint tokens of the
// same generation.
func (s *SessionIssuer) Issue(ctx context.Context, id domain.UserID) (AuthResult, error) {
	var user *domain.User
	if err := s.uow.Do(ctx, func(repos Repositories) error {
		found, err := repos.Users().FindByID(ctx, id)
		if err != nil {
			return err
		}
		found.RevokeAllTokens(s.now())
		if err := repos.Users().Update(ctx, found); err != nil {
			return fmt.Errorf("ending previous sessions: %w", err)
		}
		user = found
		return nil
	}); err != nil {
		return AuthResult{}, err
	}

	profileID, err := s.profiles.FindProfileID(ctx, user.ID())
	if err != nil {
		slog.WarnContext(ctx, "could not resolve the profile id; the claim stays empty",
			"user_id", user.ID().String(), "error", err)
		profileID = ""
	}
	token, err := s.tokens.Issue(domain.Claims{
		UserID:        user.ID(),
		UserProfileID: profileID,
		Email:         user.Email().String(),
		Role:          user.Role(),
		Generation:    user.TokenGeneration(),
	})
	if err != nil {
		return AuthResult{}, fmt.Errorf("issuing token: %w", err)
	}
	publishGenerationBestEffort(ctx, s.revocations, user.ID(), user.TokenGeneration())
	return AuthResult{UserID: user.ID().String(), UserProfileID: profileID, AccessToken: token}, nil
}

// MFA holds the second-factor flows.
type MFA struct {
	uow      UnitOfWork
	hasher   domain.PasswordHasher
	sealer   SecretSealer
	tickets  TicketStore
	sessions *SessionIssuer
	random   io.Reader
	now      func() time.Time
}

func NewMFA(
	uow UnitOfWork, hasher domain.PasswordHasher, sealer SecretSealer, tickets TicketStore,
	sessions *SessionIssuer, random io.Reader, now func() time.Time,
) (*MFA, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case sealer == nil:
		return nil, errors.New("nil secret sealer")
	case tickets == nil:
		return nil, errors.New("nil ticket store")
	case sessions == nil:
		return nil, errors.New("nil session issuer")
	case random == nil:
		return nil, errors.New("nil random source")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &MFA{uow: uow, hasher: hasher, sealer: sealer, tickets: tickets, sessions: sessions, random: random, now: now}, nil
}

// challenge issues the ticket for an account whose factors are f.
func (m *MFA) challenge(ctx context.Context, id domain.UserID, f SecurityStatus) (*Challenge, error) {
	ticket, err := m.tickets.Create(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("issuing the MFA ticket: %w", err)
	}
	c := &Challenge{Ticket: ticket, ExpiresIn: m.tickets.TTL()}
	if f.TOTPEnabled {
		c.Methods = append(c.Methods, MFAMethodTOTP)
	}
	if f.RecoveryCodesRemaining > 0 {
		c.Methods = append(c.Methods, MFAMethodRecoveryCode)
	}
	if f.PasskeyCount > 0 {
		c.Methods = append(c.Methods, MFAMethodPasskey)
	}
	return c, nil
}

// secondStep is how a first factor (password, social sign-in) ends for an
// account with a second factor: a challenge, no token. m may be nil only in
// a process started without MFA, and then the sign-in fails closed.
func secondStep(ctx context.Context, m *MFA, id domain.UserID, f SecurityStatus) (AuthResult, error) {
	if m == nil {
		return AuthResult{}, errMFANotConfigured
	}
	c, err := m.challenge(ctx, id, f)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{UserID: id.String(), Challenge: c}, nil
}

// verifier checks one proof inside a unit of work and says whether it was
// right; an error is a failure of the check itself, not a wrong answer.
type verifier func(ctx context.Context, repos Repositories) (bool, error)

// attempt runs verify under the account's attempt count. A wrong answer is
// COMMITTED as a failure before ErrInvalidSecondFactor is returned: a count
// rolled back with the error would count nothing.
func (m *MFA) attempt(ctx context.Context, id domain.UserID, verify verifier) error {
	ok := false
	if err := m.uow.Do(ctx, func(repos Repositories) error {
		mfa := repos.MFA()
		att, err := mfa.LockAttempts(ctx, id)
		if err != nil {
			return err
		}
		now := m.now()
		if att.Locked(now) {
			return domain.ErrMFALocked
		}
		if ok, err = verify(ctx, repos); err != nil {
			return err
		}
		if ok {
			att.Succeed()
		} else if att.Fail(now) {
			// NIST SP 800-63B 3.2.2: disable the authenticator. The owner
			// still has recovery codes or a passkey to get back in with.
			slog.WarnContext(ctx, "TOTP disabled after consecutive failures", "user_id", id.String())
			if err := mfa.DeleteTOTP(ctx, id); err != nil {
				return err
			}
		}
		return mfa.SaveAttempts(ctx, id, att)
	}); err != nil {
		return err
	}
	if !ok {
		return domain.ErrInvalidSecondFactor
	}
	return nil
}

// codeVerifier checks a TOTP code or a recovery code.
func (m *MFA) codeVerifier(id domain.UserID, totpCode, recoveryCode string) verifier {
	return func(ctx context.Context, repos Repositories) (bool, error) {
		switch {
		case totpCode != "":
			return m.checkTOTP(ctx, repos.MFA(), id, totpCode, true)
		case recoveryCode != "":
			return m.useRecoveryCode(ctx, repos.MFA(), id, recoveryCode)
		default:
			return false, nil
		}
	}
}

// checkTOTP verifies code against the account's secret and, when right,
// stores its step so the same code is never accepted again. confirmedOnly
// refuses a pending secret, which is what everything except the enrolment
// confirmation needs.
func (m *MFA) checkTOTP(ctx context.Context, mfa domain.MFARepository, id domain.UserID, code string, confirmedOnly bool) (bool, error) {
	f, err := mfa.FindTOTP(ctx, id)
	if errors.Is(err, domain.ErrNoTOTP) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if confirmedOnly && !f.Confirmed() {
		return false, nil
	}
	secret, err := m.sealer.Open(id, f.SecretCiphertext, f.KeyVersion)
	if err != nil {
		return false, fmt.Errorf("opening the TOTP secret: %w", err)
	}
	step, ok := domain.VerifyTOTP(secret, code, m.now(), f.LastUsedStep)
	if !ok {
		return false, nil
	}
	f.LastUsedStep = step
	if !f.Confirmed() {
		now := m.now()
		f.ConfirmedAt = &now
	}
	if err := mfa.SaveTOTP(ctx, f); err != nil {
		return false, err
	}
	return true, nil
}

// useRecoveryCode verifies and spends one recovery code. A code whose
// lookup does not exist still pays for one hash verification, so the time
// taken does not tell a real lookup from an invented one.
func (m *MFA) useRecoveryCode(ctx context.Context, mfa domain.MFARepository, id domain.UserID, raw string) (bool, error) {
	code, err := domain.ParseRecoveryCode(raw)
	if err != nil {
		return false, nil
	}
	stored, err := mfa.FindUnusedRecoveryCode(ctx, id, code.Lookup())
	if errors.Is(err, domain.ErrInvalidSecondFactor) {
		if _, _, err := m.hasher.Verify(decoyHash, code.Secret()); err != nil {
			slog.DebugContext(ctx, "decoy verification failed", "error", err)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ok, _, err := m.hasher.Verify(stored.Hash, code.Secret())
	if err != nil {
		return false, fmt.Errorf("verifying recovery code: %w", err)
	}
	if !ok {
		return false, nil
	}
	if err := mfa.MarkRecoveryCodeUsed(ctx, id, code.Lookup(), m.now()); err != nil {
		if errors.Is(err, domain.ErrInvalidSecondFactor) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// VerifyCommand answers a challenge with one proof.
type VerifyCommand struct {
	Ticket       string
	TOTPCode     string
	RecoveryCode string
}

// Verify completes a sign-in. The ticket counts one attempt whatever the
// outcome; the account counts a failure across tickets.
func (m *MFA) Verify(ctx context.Context, cmd VerifyCommand) (AuthResult, error) {
	id, err := m.tickets.Use(ctx, cmd.Ticket)
	if err != nil {
		return AuthResult{}, err
	}
	if err := m.attempt(ctx, id, m.codeVerifier(id, cmd.TOTPCode, cmd.RecoveryCode)); err != nil {
		return AuthResult{}, err
	}
	return m.finish(ctx, cmd.Ticket, id)
}

// finish ends the ticket and signs the account in.
func (m *MFA) finish(ctx context.Context, ticket string, id domain.UserID) (AuthResult, error) {
	if err := m.tickets.Delete(ctx, ticket); err != nil {
		// The ticket still expires on its own and has at most its remaining
		// attempts; failing a correct sign-in over it would be worse.
		slog.WarnContext(ctx, "could not delete an answered MFA ticket", "error", err)
	}
	return m.sessions.Issue(ctx, id)
}

// Status reports the account's factors.
func (m *MFA) Status(ctx context.Context, rawID string) (SecurityStatus, error) {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return SecurityStatus{}, err
	}
	var s SecurityStatus
	err = m.uow.Do(ctx, func(repos Repositories) error {
		s, err = factorsOf(ctx, repos.MFA(), id)
		return err
	})
	return s, err
}

// reauthenticate checks a change's proof. An account with neither a password
// nor a factor - social only - has nothing to prove with, and only then is
// an empty proof accepted.
func (m *MFA) reauthenticate(ctx context.Context, id domain.UserID, proof Reauth) error {
	var (
		user *domain.User
		f    SecurityStatus
	)
	if err := m.uow.Do(ctx, func(repos Repositories) error {
		var err error
		if user, err = repos.Users().FindByID(ctx, id); err != nil {
			return err
		}
		f, err = factorsOf(ctx, repos.MFA(), id)
		return err
	}); err != nil {
		return err
	}
	if proof.empty() {
		if !user.CanAuthenticateWithPassword() && !f.MFARequired() {
			return nil
		}
		return ErrReauthenticationRequired
	}
	if proof.Password != "" {
		return m.attempt(ctx, id, func(context.Context, Repositories) (bool, error) {
			if !user.CanAuthenticateWithPassword() {
				return false, nil
			}
			candidate, err := domain.NewPassword(proof.Password)
			if err != nil {
				return false, nil
			}
			ok, _, err := m.hasher.Verify(user.PasswordHash(), candidate)
			return ok, err
		})
	}
	return m.attempt(ctx, id, m.codeVerifier(id, proof.TOTPCode, proof.RecoveryCode))
}

// StartTOTP creates a pending secret and returns it for the authenticator
// app: as text and as an otpauth URI.
func (m *MFA) StartTOTP(ctx context.Context, rawID string, proof Reauth) (secretText, uri string, err error) {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return "", "", err
	}
	if err := m.reauthenticate(ctx, id, proof); err != nil {
		return "", "", err
	}
	secret, err := domain.NewTOTPSecret(m.random)
	if err != nil {
		return "", "", err
	}
	sealed, version, err := m.sealer.Seal(id, secret)
	if err != nil {
		return "", "", fmt.Errorf("sealing the TOTP secret: %w", err)
	}
	var email domain.Email
	err = m.uow.Do(ctx, func(repos Repositories) error {
		user, err := repos.Users().FindByID(ctx, id)
		if err != nil {
			return err
		}
		email = user.Email()
		existing, err := repos.MFA().FindTOTP(ctx, id)
		if err == nil && existing.Confirmed() {
			return ErrTOTPAlreadyEnabled
		}
		if err != nil && !errors.Is(err, domain.ErrNoTOTP) {
			return err
		}
		return repos.MFA().SaveTOTP(ctx, domain.TOTPFactor{UserID: id, SecretCiphertext: sealed, KeyVersion: version})
	})
	if err != nil {
		return "", "", err
	}
	return domain.TOTPSecretText(secret), domain.OTPAuthURI(secret, email), nil
}

// ConfirmTOTP proves the pending secret with a code and makes it a factor.
// When it is the account's first factor, the recovery codes are issued and
// returned - shown once, here.
func (m *MFA) ConfirmTOTP(ctx context.Context, rawID, code string) ([]string, error) {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return nil, err
	}
	var first bool
	err = m.attempt(ctx, id, func(ctx context.Context, repos Repositories) (bool, error) {
		f, err := repos.MFA().FindTOTP(ctx, id)
		if errors.Is(err, domain.ErrNoTOTP) {
			return false, ErrNoPendingTOTP
		}
		if err != nil {
			return false, err
		}
		if f.Confirmed() {
			return false, ErrTOTPAlreadyEnabled
		}
		before, err := factorsOf(ctx, repos.MFA(), id)
		if err != nil {
			return false, err
		}
		first = !before.MFARequired()
		return m.checkTOTP(ctx, repos.MFA(), id, code, false)
	})
	if err != nil {
		return nil, err
	}
	if !first {
		return nil, nil
	}
	return m.issueRecoveryCodes(ctx, id)
}

// DisableTOTP removes the authenticator app. Without any factor left the
// recovery codes go too: they would bypass nothing.
func (m *MFA) DisableTOTP(ctx context.Context, rawID string, proof Reauth) error {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return err
	}
	if proof.empty() {
		return ErrReauthenticationRequired
	}
	if err := m.reauthenticate(ctx, id, proof); err != nil {
		return err
	}
	return m.uow.Do(ctx, func(repos Repositories) error {
		if err := repos.MFA().DeleteTOTP(ctx, id); err != nil {
			return err
		}
		return m.dropCodesWithoutFactors(ctx, repos, id)
	})
}

// dropCodesWithoutFactors deletes the recovery codes of an account that has
// no factor left.
func (m *MFA) dropCodesWithoutFactors(ctx context.Context, repos Repositories, id domain.UserID) error {
	f, err := factorsOf(ctx, repos.MFA(), id)
	if err != nil {
		return err
	}
	if f.MFARequired() {
		return nil
	}
	return repos.MFA().DeleteRecoveryCodes(ctx, id)
}

// RegenerateRecoveryCodes replaces every code; the old ones stop working at
// once.
func (m *MFA) RegenerateRecoveryCodes(ctx context.Context, rawID string, proof Reauth) ([]string, error) {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return nil, err
	}
	if proof.empty() {
		return nil, ErrReauthenticationRequired
	}
	if err := m.reauthenticate(ctx, id, proof); err != nil {
		return nil, err
	}
	status, err := m.Status(ctx, rawID)
	if err != nil {
		return nil, err
	}
	if !status.MFARequired() {
		return nil, ErrMFANotEnabled
	}
	return m.issueRecoveryCodes(ctx, id)
}

// issueRecoveryCodes generates, hashes and stores a fresh set, replacing
// any earlier one, and returns the codes as shown to the owner.
func (m *MFA) issueRecoveryCodes(ctx context.Context, id domain.UserID) ([]string, error) {
	codes, err := domain.GenerateRecoveryCodes(m.random, domain.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	stored := make([]domain.StoredRecoveryCode, 0, len(codes))
	shown := make([]string, 0, len(codes))
	for _, c := range codes {
		hash, err := m.hasher.Hash(c.Secret())
		if err != nil {
			return nil, fmt.Errorf("hashing a recovery code: %w", err)
		}
		stored = append(stored, domain.StoredRecoveryCode{Lookup: c.Lookup(), Hash: hash})
		shown = append(shown, c.String())
	}
	if err := m.uow.Do(ctx, func(repos Repositories) error {
		return repos.MFA().ReplaceRecoveryCodes(ctx, id, stored)
	}); err != nil {
		return nil, err
	}
	return shown, nil
}
