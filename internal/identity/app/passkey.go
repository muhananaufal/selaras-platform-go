package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// WebAuthnUser is an account as a WebAuthn ceremony sees it.
type WebAuthnUser struct {
	Handle      []byte
	Name        string
	Credentials [][]byte // the stored credential records
}

// VerifiedCredential is a credential that just passed a ceremony: its id and
// its record to store (public key, sign count, flags).
type VerifiedCredential struct {
	ID     []byte
	Record []byte
}

// WebAuthn runs the ceremonies (go-webauthn in production, behind an adapter
// because the library brings net/http). Every Finish* refuses with
// ErrPasskeyRejected, and requires user verification on the response itself.
type WebAuthn interface {
	BeginRegistration(u WebAuthnUser) (options, session []byte, err error)
	FinishRegistration(u WebAuthnUser, session, response []byte) (VerifiedCredential, error)
	BeginDiscoverableLogin() (options, session []byte, err error)
	FinishDiscoverableLogin(session, response []byte, lookup func(handle []byte) (WebAuthnUser, error)) (handle []byte, c VerifiedCredential, err error)
	BeginLogin(u WebAuthnUser) (options, session []byte, err error)
	FinishLogin(u WebAuthnUser, session, response []byte) (VerifiedCredential, error)
}

// StoredPasskey is a credential as stored, plus what the owner sees.
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
	Session []byte
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

// Passkeys holds the WebAuthn flows.
type Passkeys struct {
	w          WebAuthn
	uow        UnitOfWork
	ceremonies CeremonyStore
	mfa        *MFA
	now        func() time.Time
}

func NewPasskeys(w WebAuthn, uow UnitOfWork, ceremonies CeremonyStore, mfa *MFA, now func() time.Time) (*Passkeys, error) {
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

// loadUser reads the account as a ceremony sees it, creating its handle on
// first use. The handle is random, not the user id: it is stored on the
// authenticator and must carry no personal information (WebAuthn L3 5.4.3).
func (p *Passkeys) loadUser(ctx context.Context, repos Repositories, id domain.UserID) (WebAuthnUser, error) {
	user, err := repos.Users().FindByID(ctx, id)
	if err != nil {
		return WebAuthnUser{}, err
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return WebAuthnUser{}, fmt.Errorf("generating a user handle: %w", err)
	}
	handle, err := repos.Passkeys().Handle(ctx, id, fresh)
	if err != nil {
		return WebAuthnUser{}, err
	}
	stored, err := repos.Passkeys().List(ctx, id)
	if err != nil {
		return WebAuthnUser{}, err
	}
	u := WebAuthnUser{Handle: handle, Name: user.Email().String()}
	for _, s := range stored {
		u.Credentials = append(u.Credentials, s.Credential)
	}
	return u, nil
}

func (p *Passkeys) put(ctx context.Context, kind CeremonyKind, userID, ticket string, options, session []byte) (string, string, error) {
	id, err := p.ceremonies.Put(ctx, Ceremony{Kind: kind, UserID: userID, Ticket: ticket, Session: session})
	if err != nil {
		return "", "", fmt.Errorf("storing the ceremony: %w", err)
	}
	return id, string(options), nil
}

// take returns the ceremony id names if it is of kind, for userID when one
// is given.
func (p *Passkeys) take(ctx context.Context, id string, kind CeremonyKind, userID string) (Ceremony, error) {
	c, err := p.ceremonies.Take(ctx, id)
	if err != nil {
		return Ceremony{}, err
	}
	if c.Kind != kind || (userID != "" && c.UserID != userID) {
		return Ceremony{}, ErrPasskeyRejected
	}
	return c, nil
}

// loggedRejection logs the reason a ceremony failed and returns the one
// answer the client gets.
func loggedRejection(ctx context.Context, what string, err error) error {
	if errors.Is(err, ErrPasskeyRejected) {
		slog.InfoContext(ctx, what+" rejected", "error", err)
		return ErrPasskeyRejected
	}
	return err
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
	var user WebAuthnUser
	if err := p.uow.Do(ctx, func(repos Repositories) error {
		user, err = p.loadUser(ctx, repos, id)
		return err
	}); err != nil {
		return "", "", err
	}
	options, session, err := p.w.BeginRegistration(user)
	if err != nil {
		return "", "", err
	}
	return p.put(ctx, CeremonyRegister, rawID, "", options, session)
}

// FinishRegistration verifies the browser's attestation and stores the
// passkey; the account's first factor also gets its recovery codes.
func (p *Passkeys) FinishRegistration(ctx context.Context, rawID, ceremonyID, credentialJSON, name string) (StoredPasskey, []string, error) {
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return StoredPasskey{}, nil, err
	}
	c, err := p.take(ctx, ceremonyID, CeremonyRegister, rawID)
	if err != nil {
		return StoredPasskey{}, nil, err
	}
	var (
		stored StoredPasskey
		first  bool
	)
	err = p.uow.Do(ctx, func(repos Repositories) error {
		user, err := p.loadUser(ctx, repos, id)
		if err != nil {
			return err
		}
		cred, err := p.w.FinishRegistration(user, c.Session, []byte(credentialJSON))
		if err != nil {
			return loggedRejection(ctx, "passkey registration", err)
		}
		before, err := factorsOf(ctx, repos.MFA(), id)
		if err != nil {
			return err
		}
		first = !before.MFARequired()
		stored = StoredPasskey{ID: uuid.NewString(), Name: name, CredentialID: cred.ID, Credential: cred.Record, CreatedAt: p.now()}
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
	options, session, err := p.w.BeginDiscoverableLogin()
	if err != nil {
		return "", "", err
	}
	return p.put(ctx, CeremonyLogin, "", "", options, session)
}

// FinishLogin verifies the assertion and signs its owner in. A passkey with
// user verification is two factors at once: no challenge follows.
func (p *Passkeys) FinishLogin(ctx context.Context, ceremonyID, credentialJSON string) (AuthResult, error) {
	c, err := p.take(ctx, ceremonyID, CeremonyLogin, "")
	if err != nil {
		return AuthResult{}, err
	}
	var owner domain.UserID
	err = p.uow.Do(ctx, func(repos Repositories) error {
		lookup := func(handle []byte) (WebAuthnUser, error) {
			id, err := repos.Passkeys().UserByHandle(ctx, handle)
			if err != nil {
				return WebAuthnUser{}, err
			}
			owner = id
			return p.loadUser(ctx, repos, id)
		}
		_, cred, err := p.w.FinishDiscoverableLogin(c.Session, []byte(credentialJSON), lookup)
		if err != nil {
			return loggedRejection(ctx, "passkey sign-in", err)
		}
		return repos.Passkeys().Touch(ctx, cred.ID, cred.Record, p.now())
	})
	if err != nil {
		return AuthResult{}, err
	}
	return p.mfa.sessions.Issue(ctx, owner)
}

// BeginMFA starts the passkey ceremony that answers a challenge. The
// ticket's budget pays for it like any other attempt.
func (p *Passkeys) BeginMFA(ctx context.Context, ticket string) (string, string, error) {
	id, err := p.mfa.tickets.Use(ctx, ticket)
	if err != nil {
		return "", "", err
	}
	var user WebAuthnUser
	if err := p.uow.Do(ctx, func(repos Repositories) error {
		user, err = p.loadUser(ctx, repos, id)
		return err
	}); err != nil {
		return "", "", err
	}
	if len(user.Credentials) == 0 {
		return "", "", ErrPasskeyRejected
	}
	options, session, err := p.w.BeginLogin(user)
	if err != nil {
		return "", "", err
	}
	return p.put(ctx, CeremonyMFA, id.String(), ticket, options, session)
}

// verifier checks a passkey answer to the challenge of ticket, inside
// MFA.attempt.
func (p *Passkeys) verifier(id domain.UserID, ticket, ceremonyID, credentialJSON string) verifier {
	return func(ctx context.Context, repos Repositories) (bool, error) {
		c, err := p.take(ctx, ceremonyID, CeremonyMFA, id.String())
		if errors.Is(err, ErrPasskeyRejected) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if c.Ticket != ticket {
			return false, nil
		}
		user, err := p.loadUser(ctx, repos, id)
		if err != nil {
			return false, err
		}
		cred, err := p.w.FinishLogin(user, c.Session, []byte(credentialJSON))
		if errors.Is(err, ErrPasskeyRejected) {
			slog.InfoContext(ctx, "passkey second factor rejected", "error", err)
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if err := repos.Passkeys().Touch(ctx, cred.ID, cred.Record, p.now()); err != nil {
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
