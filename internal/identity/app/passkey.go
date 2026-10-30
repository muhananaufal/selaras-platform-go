package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Passkeys (ADR-032): WebAuthn credentials with user verification REQUIRED,
// usable to sign in without a password and as the second factor.

var (
	// ErrPasskeyRejected is the one answer for an assertion or attestation
	// that does not verify: wrong ceremony, wrong origin, bad signature, no
	// user verification, or a counter that went backwards.
	ErrPasskeyRejected = errors.New("the passkey could not be verified")

	// ErrPasskeyNotFound answers a delete of a passkey the caller does not
	// have.
	ErrPasskeyNotFound = errors.New("no such passkey")

	errPasskeysNotConfigured = errors.New("passkeys are not configured in this process")
)

// StoredPasskey is a credential as stored: the go-webauthn record as JSON
// plus what the owner sees.
type StoredPasskey struct {
	ID           string
	Name         string
	CredentialID []byte
	Credential   []byte
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

// PasskeyRepository stores WebAuthn credentials and user handles.
type PasskeyRepository interface {
	// Handle returns the account's WebAuthn user handle, creating it with
	// fresh when it has none.
	Handle(ctx context.Context, id domain.UserID, fresh []byte) ([]byte, error)
	// UserByHandle returns domain.ErrUserNotFound for an unknown handle.
	UserByHandle(ctx context.Context, handle []byte) (domain.UserID, error)
	List(ctx context.Context, id domain.UserID) ([]StoredPasskey, error)
	Add(ctx context.Context, id domain.UserID, p StoredPasskey) error
	// Touch stores the credential after a sign-in (new sign count).
	Touch(ctx context.Context, credentialID, credential []byte, at time.Time) error
	// Delete returns ErrPasskeyNotFound when id is not one of the account's.
	Delete(ctx context.Context, id domain.UserID, passkeyID string) error
}

// Ceremony is the server half of one WebAuthn ceremony.
type Ceremony struct {
	Kind    CeremonyKind
	UserID  string // empty for a passwordless sign-in: the user is not known yet
	Ticket  string // set for a ceremony answering an MFA challenge
	Session []byte // webauthn.SessionData as JSON
}

type CeremonyKind string

const (
	CeremonyRegister CeremonyKind = "register"
	CeremonyLogin    CeremonyKind = "login"
	CeremonyMFA      CeremonyKind = "mfa"
)

// CeremonyStore keeps ceremonies for a few minutes; Take returns each once.
type CeremonyStore interface {
	Put(ctx context.Context, c Ceremony) (string, error)
	// Take returns ErrPasskeyRejected for an unknown, expired or used id.
	Take(ctx context.Context, id string) (Ceremony, error)
}

// passkeyUser is webauthn.User for one account.
type passkeyUser struct {
	handle []byte
	name   string
	creds  []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u passkeyUser) WebAuthnName() string                       { return u.name }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// Passkeys holds the WebAuthn flows.
type Passkeys struct {
	w          *webauthn.WebAuthn
	uow        UnitOfWork
	ceremonies CeremonyStore
	mfa        *MFA
	now        func() time.Time
}

func NewPasskeys(w *webauthn.WebAuthn, uow UnitOfWork, ceremonies CeremonyStore, mfa *MFA, now func() time.Time) (*Passkeys, error) {
	switch {
	case w == nil:
		return nil, errors.New("nil webauthn")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case ceremonies == nil:
		return nil, errors.New("nil ceremony store")
	case mfa == nil:
		return nil, errors.New("nil MFA")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	p := &Passkeys{w: w, uow: uow, ceremonies: ceremonies, mfa: mfa, now: now}
	mfa.passkeys = p
	return p, nil
}

// loadUser reads the account as webauthn sees it, creating its handle on
// first use.
func (p *Passkeys) loadUser(ctx context.Context, repos Repositories, id domain.UserID) (passkeyUser, []StoredPasskey, error) {
	user, err := repos.Users().FindByID(ctx, id)
	if err != nil {
		return passkeyUser{}, nil, err
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return passkeyUser{}, nil, fmt.Errorf("generating a user handle: %w", err)
	}
	handle, err := repos.Passkeys().Handle(ctx, id, fresh)
	if err != nil {
		return passkeyUser{}, nil, err
	}
	stored, err := repos.Passkeys().List(ctx, id)
	if err != nil {
		return passkeyUser{}, nil, err
	}
	u := passkeyUser{handle: handle, name: user.Email().String()}
	for _, s := range stored {
		var c webauthn.Credential
		if err := json.Unmarshal(s.Credential, &c); err != nil {
			return passkeyUser{}, nil, fmt.Errorf("reading passkey %s: %w", s.ID, err)
		}
		u.creds = append(u.creds, c)
	}
	return u, stored, nil
}

func (p *Passkeys) put(ctx context.Context, kind CeremonyKind, userID, ticket string, session *webauthn.SessionData, options any) (string, string, error) {
	raw, err := json.Marshal(session)
	if err != nil {
		return "", "", err
	}
	id, err := p.ceremonies.Put(ctx, Ceremony{Kind: kind, UserID: userID, Ticket: ticket, Session: raw})
	if err != nil {
		return "", "", fmt.Errorf("storing the ceremony: %w", err)
	}
	opts, err := json.Marshal(options)
	if err != nil {
		return "", "", err
	}
	return id, string(opts), nil
}

// take returns the ceremony id names if it is of kind, for userID when one
// is given.
func (p *Passkeys) take(ctx context.Context, id string, kind CeremonyKind, userID string) (Ceremony, webauthn.SessionData, error) {
	c, err := p.ceremonies.Take(ctx, id)
	if err != nil {
		return Ceremony{}, webauthn.SessionData{}, err
	}
	if c.Kind != kind || (userID != "" && c.UserID != userID) {
		return Ceremony{}, webauthn.SessionData{}, ErrPasskeyRejected
	}
	var s webauthn.SessionData
	if err := json.Unmarshal(c.Session, &s); err != nil {
		return Ceremony{}, webauthn.SessionData{}, fmt.Errorf("reading the ceremony: %w", err)
	}
	return c, s, nil
}

var requireUV = protocol.AuthenticatorSelection{
	ResidentKey:      protocol.ResidentKeyRequirementRequired,
	UserVerification: protocol.VerificationRequired,
}

// BeginRegistration starts adding a passkey; proof is checked as for any
// change to a factor.
func (p *Passkeys) BeginRegistration(ctx context.Context, rawID string, proof Reauth) (string, string, error) {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return "", "", err
	}
	if err := p.mfa.reauthenticate(ctx, id, proof); err != nil {
		return "", "", err
	}
	var user passkeyUser
	if err := p.uow.Do(ctx, func(repos Repositories) error {
		user, _, err = p.loadUser(ctx, repos, id)
		return err
	}); err != nil {
		return "", "", err
	}
	creation, session, err := p.w.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(requireUV),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(user.creds).CredentialDescriptors()),
	)
	if err != nil {
		return "", "", fmt.Errorf("starting the registration: %w", err)
	}
	return p.put(ctx, CeremonyRegister, rawID, "", session, creation)
}

// FinishRegistration verifies the browser's attestation and stores the
// passkey; the account's first factor also gets its recovery codes.
func (p *Passkeys) FinishRegistration(ctx context.Context, rawID, ceremonyID, credentialJSON, name string) (StoredPasskey, []string, error) {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return StoredPasskey{}, nil, err
	}
	_, session, err := p.take(ctx, ceremonyID, CeremonyRegister, rawID)
	if err != nil {
		return StoredPasskey{}, nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes([]byte(credentialJSON))
	if err != nil {
		return StoredPasskey{}, nil, ErrPasskeyRejected
	}
	if !parsed.Response.AttestationObject.AuthData.Flags.HasUserVerified() {
		return StoredPasskey{}, nil, ErrPasskeyRejected
	}

	var (
		stored StoredPasskey
		first  bool
	)
	err = p.uow.Do(ctx, func(repos Repositories) error {
		user, _, err := p.loadUser(ctx, repos, id)
		if err != nil {
			return err
		}
		cred, err := p.w.CreateCredential(user, session, parsed)
		if err != nil {
			slog.InfoContext(ctx, "passkey registration rejected", "error", err)
			return ErrPasskeyRejected
		}
		raw, err := json.Marshal(cred)
		if err != nil {
			return err
		}
		before, err := factorsOf(ctx, repos.MFA(), id)
		if err != nil {
			return err
		}
		first = !before.MFARequired()
		stored = StoredPasskey{ID: uuid.NewString(), Name: name, CredentialID: cred.ID, Credential: raw, CreatedAt: p.now()}
		return repos.Passkeys().Add(ctx, id, stored)
	})
	if err != nil {
		return StoredPasskey{}, nil, err
	}
	if !first {
		return stored, nil, nil
	}
	codes, err := p.mfa.issueRecoveryCodes(ctx, id)
	return stored, codes, err
}

// BeginLogin starts a passwordless sign-in: discoverable, so the browser
// offers the passkeys it has for this site and the user is learned from the
// answer.
func (p *Passkeys) BeginLogin(ctx context.Context) (string, string, error) {
	assertion, session, err := p.w.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", "", fmt.Errorf("starting the passkey sign-in: %w", err)
	}
	return p.put(ctx, CeremonyLogin, "", "", session, assertion)
}

// FinishLogin verifies the assertion and signs its owner in. A passkey with
// user verification is two factors at once: no challenge follows.
func (p *Passkeys) FinishLogin(ctx context.Context, ceremonyID, credentialJSON string) (AuthResult, error) {
	_, session, err := p.take(ctx, ceremonyID, CeremonyLogin, "")
	if err != nil {
		return AuthResult{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(credentialJSON))
	if err != nil {
		return AuthResult{}, ErrPasskeyRejected
	}
	var owner domain.UserID
	err = p.uow.Do(ctx, func(repos Repositories) error {
		handler := func(_, userHandle []byte) (webauthn.User, error) {
			id, err := repos.Passkeys().UserByHandle(ctx, userHandle)
			if err != nil {
				return nil, err
			}
			owner = id
			u, _, err := p.loadUser(ctx, repos, id)
			return u, err
		}
		_, cred, err := p.w.ValidatePasskeyLogin(handler, session, parsed)
		if err != nil {
			slog.InfoContext(ctx, "passkey sign-in rejected", "error", err)
			return ErrPasskeyRejected
		}
		return p.touch(ctx, repos, parsed, cred)
	})
	if err != nil {
		return AuthResult{}, err
	}
	return p.mfa.sessions.Issue(ctx, owner)
}

// touch refuses a credential whose counter went backwards (a cloned
// authenticator) or whose assertion carries no user verification, and
// stores the new sign count otherwise.
func (p *Passkeys) touch(ctx context.Context, repos Repositories, parsed *protocol.ParsedCredentialAssertionData, cred *webauthn.Credential) error {
	if !parsed.Response.AuthenticatorData.Flags.HasUserVerified() || cred.Authenticator.CloneWarning {
		return ErrPasskeyRejected
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	return repos.Passkeys().Touch(ctx, cred.ID, raw, p.now())
}

// BeginMFA starts the passkey ceremony that answers a challenge. The
// ticket's budget pays for it like any other attempt.
func (p *Passkeys) BeginMFA(ctx context.Context, ticket string) (string, string, error) {
	id, err := p.mfa.tickets.Use(ctx, ticket)
	if err != nil {
		return "", "", err
	}
	var user passkeyUser
	if err := p.uow.Do(ctx, func(repos Repositories) error {
		user, _, err = p.loadUser(ctx, repos, id)
		return err
	}); err != nil {
		return "", "", err
	}
	if len(user.creds) == 0 {
		return "", "", ErrPasskeyRejected
	}
	assertion, session, err := p.w.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", "", fmt.Errorf("starting the passkey check: %w", err)
	}
	return p.put(ctx, CeremonyMFA, id.String(), ticket, session, assertion)
}

// verifier checks a passkey answer to the challenge of ticket, inside
// MFA.attempt.
func (p *Passkeys) verifier(id domain.UserID, ticket, ceremonyID, credentialJSON string) verifier {
	return func(ctx context.Context, repos Repositories) (bool, error) {
		c, session, err := p.take(ctx, ceremonyID, CeremonyMFA, id.String())
		if errors.Is(err, ErrPasskeyRejected) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if c.Ticket != ticket {
			return false, nil
		}
		parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(credentialJSON))
		if err != nil {
			return false, nil
		}
		user, _, err := p.loadUser(ctx, repos, id)
		if err != nil {
			return false, err
		}
		cred, err := p.w.ValidateLogin(user, session, parsed)
		if err != nil {
			slog.InfoContext(ctx, "passkey second factor rejected", "error", err)
			return false, nil
		}
		if err := p.touch(ctx, repos, parsed, cred); err != nil {
			if errors.Is(err, ErrPasskeyRejected) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
}

// List returns the caller's passkeys.
func (p *Passkeys) List(ctx context.Context, rawID string) ([]StoredPasskey, error) {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return nil, err
	}
	var out []StoredPasskey
	err = p.uow.Do(ctx, func(repos Repositories) error {
		out, err = repos.Passkeys().List(ctx, id)
		return err
	})
	return out, err
}

// Delete removes one passkey after a fresh proof. Without any factor left
// the recovery codes go too.
func (p *Passkeys) Delete(ctx context.Context, rawID, passkeyID string, proof Reauth) error {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return err
	}
	if proof.empty() {
		return ErrReauthenticationRequired
	}
	if err := p.mfa.reauthenticate(ctx, id, proof); err != nil {
		return err
	}
	return p.uow.Do(ctx, func(repos Repositories) error {
		if err := repos.Passkeys().Delete(ctx, id, passkeyID); err != nil {
			return err
		}
		return p.mfa.dropCodesWithoutFactors(ctx, repos, id)
	})
}
