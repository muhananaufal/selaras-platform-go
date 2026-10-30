package main

import (
	"crypto/rand"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/identity"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/crypto"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/mfastore"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/webauthn"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

const (
	// A sign-in's second step and a WebAuthn ceremony both last five minutes
	// (ADR-032): long enough to find a phone, short enough that a stolen
	// ticket is useless soon.
	mfaTicketTTL      = 5 * time.Minute
	mfaTicketAttempts = 5
	ceremonyTTL       = 5 * time.Minute
)

// buildMFA assembles the second-factor flows on the same unit of work,
// hasher and token issuer as the other use cases.
func buildMFA(
	cfg identity.MFAConfig,
	uow app.UnitOfWork,
	hasher domain.PasswordHasher,
	issuer domain.TokenIssuer,
	profiles app.ProfileFinder,
	revocations domain.RevocationPublisher,
	redisClient *goredis.Client,
	now func() time.Time,
) (*app.MFA, *app.Passkeys, error) {
	sealer, err := crypto.ParseSealer(cfg.EncryptionKeys)
	if err != nil {
		return nil, nil, err
	}
	tickets, err := mfastore.NewTickets(redisClient, mfaTicketTTL, mfaTicketAttempts)
	if err != nil {
		return nil, nil, err
	}
	ceremonies, err := mfastore.NewCeremonies(redisClient, ceremonyTTL)
	if err != nil {
		return nil, nil, err
	}
	sessions, err := app.NewSessionIssuer(uow, issuer, profiles, revocations, now)
	if err != nil {
		return nil, nil, err
	}
	mfa, err := app.NewMFA(uow, hasher, sealer, tickets, sessions, rand.Reader, now)
	if err != nil {
		return nil, nil, err
	}
	w, err := webauthn.New(cfg.RPID, cfg.RPOrigins, ceremonyTTL)
	if err != nil {
		return nil, nil, err
	}
	passkeys, err := app.NewPasskeys(w, uow, ceremonies, mfa, now)
	if err != nil {
		return nil, nil, err
	}
	return mfa, passkeys, nil
}
