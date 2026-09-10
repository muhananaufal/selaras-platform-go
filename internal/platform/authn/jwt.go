package authn

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// ErrInvalidToken covers every reason a token is refused. The reason lives
// in the wrapped error for the log; the caller only gets "unauthenticated" -
// distinguishing a bad signature from an expired token tells an attacker the
// signature was right.
var ErrInvalidToken = errors.New("invalid access token")

// claims is the slice of identity-svc's claims needed here. The field names
// MUST match what internal/identity/adapter/token writes; a cross-package
// test keeps the two aligned.
type claims struct {
	jwt.RegisteredClaims
	Generation int64 `json:"gen"`
}

// Verifier checks tokens with identity-svc's public key.
//
// It deliberately does not use internal/identity/adapter/token: that
// package returns identity's domain.Claims, and importing it from every
// service would make every service depend on the identity domain. All that
// is needed here is sub and gen.
type Verifier struct {
	key    ed25519.PublicKey
	parser *jwt.Parser
}

// NewVerifier takes an Ed25519 public key and the expected issuer name.
// Both are checked at start-up, not on the first request.
func NewVerifier(key ed25519.PublicKey, issuer string) (*Verifier, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes; want %d", len(key), ed25519.PublicKeySize)
	}
	if issuer == "" {
		return nil, errors.New("empty issuer name")
	}
	return &Verifier{
		key: key,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
			jwt.WithIssuer(issuer),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		),
	}, nil
}

// Verify returns the Principal of a valid token.
func (v *Verifier) Verify(raw string) (Principal, error) {
	var c claims
	if _, err := v.parser.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) {
		return v.key, nil
	}); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if _, err := uuid.Parse(c.Subject); err != nil {
		return Principal{}, fmt.Errorf("%w: subject is not a user id", ErrInvalidToken)
	}
	// A generation of zero means the claim is missing; accepting it would let
	// a token without a generation survive every revocation (ADR-020).
	if c.Generation < 1 {
		return Principal{}, fmt.Errorf("%w: missing token generation", ErrInvalidToken)
	}
	return Principal{UserID: c.Subject, Generation: c.Generation}, nil
}
