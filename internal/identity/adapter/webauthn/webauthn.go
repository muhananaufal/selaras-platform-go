// Package webauthn is app.WebAuthn on go-webauthn (ADR-032). The library
// brings net/http with it (its Finish* helpers read requests), which the use
// case layer may not depend on (test/architecture), so every call to it lives
// here and the use cases see bytes.
package webauthn

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	lib "github.com/go-webauthn/webauthn/webauthn"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
)

// Ceremonies verifies WebAuthn ceremonies with user verification REQUIRED.
type Ceremonies struct {
	w *lib.WebAuthn
}

var _ app.WebAuthn = (*Ceremonies)(nil)

// New configures the relying party: its id, the exact origins a ceremony may
// come from, and how long a ceremony lives (enforced by the server too).
func New(rpID string, origins []string, timeout time.Duration) (*Ceremonies, error) {
	w, err := lib.New(&lib.Config{
		RPDisplayName: "Selaras",
		RPID:          rpID,
		RPOrigins:     origins,
		Timeouts: lib.TimeoutsConfig{
			Login:        lib.TimeoutConfig{Enforce: true, Timeout: timeout, TimeoutUVD: timeout},
			Registration: lib.TimeoutConfig{Enforce: true, Timeout: timeout, TimeoutUVD: timeout},
		},
	})
	if err != nil {
		return nil, err
	}
	return &Ceremonies{w: w}, nil
}

type user struct {
	handle []byte
	name   string
	creds  []lib.Credential
}

func (u user) WebAuthnID() []byte                    { return u.handle }
func (u user) WebAuthnName() string                  { return u.name }
func (u user) WebAuthnDisplayName() string           { return u.name }
func (u user) WebAuthnCredentials() []lib.Credential { return u.creds }

func toUser(u app.WebAuthnUser) (user, error) {
	out := user{handle: u.Handle, name: u.Name}
	for _, raw := range u.Credentials {
		var c lib.Credential
		if err := json.Unmarshal(raw, &c); err != nil {
			return user{}, fmt.Errorf("reading a stored passkey: %w", err)
		}
		out.creds = append(out.creds, c)
	}
	return out, nil
}

var requireUV = protocol.AuthenticatorSelection{
	ResidentKey:      protocol.ResidentKeyRequirementRequired,
	UserVerification: protocol.VerificationRequired,
}

func encode(options any, session *lib.SessionData) ([]byte, []byte, error) {
	o, err := json.Marshal(options)
	if err != nil {
		return nil, nil, err
	}
	s, err := json.Marshal(session)
	if err != nil {
		return nil, nil, err
	}
	return o, s, nil
}

func decodeSession(raw []byte) (lib.SessionData, error) {
	var s lib.SessionData
	if err := json.Unmarshal(raw, &s); err != nil {
		return lib.SessionData{}, fmt.Errorf("reading the ceremony: %w", err)
	}
	return s, nil
}

func (c *Ceremonies) BeginRegistration(u app.WebAuthnUser) ([]byte, []byte, error) {
	wu, err := toUser(u)
	if err != nil {
		return nil, nil, err
	}
	creation, session, err := c.w.BeginRegistration(wu,
		lib.WithAuthenticatorSelection(requireUV),
		lib.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		lib.WithExclusions(lib.Credentials(wu.creds).CredentialDescriptors()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("starting the registration: %w", err)
	}
	return encode(creation, session)
}

func (c *Ceremonies) FinishRegistration(u app.WebAuthnUser, session, response []byte) (app.VerifiedCredential, error) {
	wu, err := toUser(u)
	if err != nil {
		return app.VerifiedCredential{}, err
	}
	s, err := decodeSession(session)
	if err != nil {
		return app.VerifiedCredential{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return app.VerifiedCredential{}, rejected(err)
	}
	// The library checks user verification when the ceremony asked for it;
	// checking the flag here too keeps "required" from depending on one place.
	if !parsed.Response.AttestationObject.AuthData.Flags.HasUserVerified() {
		return app.VerifiedCredential{}, rejected(errors.New("no user verification"))
	}
	cred, err := c.w.CreateCredential(wu, s, parsed)
	if err != nil {
		return app.VerifiedCredential{}, rejected(err)
	}
	return verified(cred)
}

func (c *Ceremonies) BeginDiscoverableLogin() ([]byte, []byte, error) {
	assertion, session, err := c.w.BeginDiscoverableLogin(lib.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, nil, fmt.Errorf("starting the passkey sign-in: %w", err)
	}
	return encode(assertion, session)
}

func (c *Ceremonies) FinishDiscoverableLogin(
	session, response []byte, lookup func(handle []byte) (app.WebAuthnUser, error),
) ([]byte, app.VerifiedCredential, error) {
	s, err := decodeSession(session)
	if err != nil {
		return nil, app.VerifiedCredential{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, app.VerifiedCredential{}, rejected(err)
	}
	var handle []byte
	handler := func(_, userHandle []byte) (lib.User, error) {
		u, err := lookup(userHandle)
		if err != nil {
			return nil, err
		}
		handle = userHandle
		return toUser(u)
	}
	_, cred, err := c.w.ValidatePasskeyLogin(handler, s, parsed)
	if err != nil {
		return nil, app.VerifiedCredential{}, rejected(err)
	}
	vc, err := checkAssertion(parsed, cred)
	return handle, vc, err
}

func (c *Ceremonies) BeginLogin(u app.WebAuthnUser) ([]byte, []byte, error) {
	wu, err := toUser(u)
	if err != nil {
		return nil, nil, err
	}
	assertion, session, err := c.w.BeginLogin(wu, lib.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, nil, fmt.Errorf("starting the passkey check: %w", err)
	}
	return encode(assertion, session)
}

func (c *Ceremonies) FinishLogin(u app.WebAuthnUser, session, response []byte) (app.VerifiedCredential, error) {
	wu, err := toUser(u)
	if err != nil {
		return app.VerifiedCredential{}, err
	}
	s, err := decodeSession(session)
	if err != nil {
		return app.VerifiedCredential{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return app.VerifiedCredential{}, rejected(err)
	}
	cred, err := c.w.ValidateLogin(wu, s, parsed)
	if err != nil {
		return app.VerifiedCredential{}, rejected(err)
	}
	return checkAssertion(parsed, cred)
}

// checkAssertion refuses an assertion without user verification - the
// stored credential's UV flag is latched by the library, so it is read from
// this assertion - and a counter that went backwards (a cloned
// authenticator).
func checkAssertion(parsed *protocol.ParsedCredentialAssertionData, cred *lib.Credential) (app.VerifiedCredential, error) {
	if !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
		return app.VerifiedCredential{}, rejected(errors.New("no user verification"))
	}
	if cred.Authenticator.CloneWarning {
		return app.VerifiedCredential{}, rejected(errors.New("the sign count went backwards"))
	}
	return verified(cred)
}

func verified(cred *lib.Credential) (app.VerifiedCredential, error) {
	raw, err := json.Marshal(cred)
	if err != nil {
		return app.VerifiedCredential{}, err
	}
	return app.VerifiedCredential{ID: cred.ID, Record: raw}, nil
}

// rejected keeps the library's reason in the chain for the log, under the one
// answer the client gets.
func rejected(cause error) error {
	return fmt.Errorf("%w: %w", app.ErrPasskeyRejected, cause)
}
