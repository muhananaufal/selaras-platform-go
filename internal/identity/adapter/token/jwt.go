// Package token issues and verifies JWT access tokens.
package token

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// algorithm is EdDSA (Ed25519), and it is asymmetric on purpose.
//
// With HMAC, every unit that needs to verify tokens also holds the key to
// issue them - nine units all able to mint an admin token. Here only
// identity-svc holds the private key, and the others can only check.
//
// Ed25519 was chosen among the asymmetric options because its keys are
// short, its signatures verify quickly, and there are no parameters that
// can be chosen wrongly as with RSA.
var algorithm = jwt.SigningMethodEdDSA

// claims maps domain.Claims to the JWT shape.
//
// Standard claim names are used where a counterpart exists - sub, iss, exp,
// iat, jti - so any tool can read the token. Those without a counterpart get
// a prefix to avoid colliding with registered claims later.
type claims struct {
	jwt.RegisteredClaims
	UserProfileID string `json:"upid,omitempty"`
	Email         string `json:"email,omitempty"`
	Role          string `json:"role"`
	Generation    int64  `json:"gen"`
}

// Issuer signs claims. It holds the private key, so only identity-svc may
// construct it.
type Issuer struct {
	key      ed25519.PrivateKey
	issuer   string
	lifetime time.Duration
	now      func() time.Time
}

func NewIssuer(key ed25519.PrivateKey, issuerName string, lifetime time.Duration) (*Issuer, error) {
	// Ed25519 accepts a slice of any size without complaint until signing
	// time, where it panics. The size is checked here so a wrong configuration
	// fails start-up, not the first login request.
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key is %d bytes; want %d", len(key), ed25519.PrivateKeySize)
	}
	if issuerName == "" {
		return nil, errors.New("empty issuer name")
	}
	if lifetime == 0 {
		return nil, errors.New("zero token lifetime")
	}
	return &Issuer{key: key, issuer: issuerName, lifetime: lifetime, now: time.Now}, nil
}

var _ domain.TokenIssuer = (*Issuer)(nil)

func (i *Issuer) Issue(c domain.Claims) (string, error) {
	// jti keeps two tokens issued in the same second for the same user
	// distinct, so logs can tell sessions apart.
	jti, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generating token id: %w", err)
	}

	issuedAt := i.now()
	tok := jwt.NewWithClaims(algorithm, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti.String(),
			Subject:   c.UserID.String(),
			Issuer:    i.issuer,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(i.lifetime)),
		},
		UserProfileID: c.UserProfileID,
		Email:         c.Email,
		Role:          c.Role.String(),
		Generation:    c.Generation,
	})

	signed, err := tok.SignedString(i.key)
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	return signed, nil
}

// Verifier checks the signature and validity period. It holds only the
// public key, so it is safe to hand to any unit.
type Verifier struct {
	key    ed25519.PublicKey
	issuer string
	parser *jwt.Parser
}

func NewVerifier(key ed25519.PublicKey, issuerName string) (*Verifier, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes; want %d", len(key), ed25519.PublicKeySize)
	}
	if issuerName == "" {
		return nil, errors.New("empty issuer name")
	}

	return &Verifier{
		key:    key,
		issuer: issuerName,
		parser: jwt.NewParser(
			// Algorithm confusion is already closed today by the key type: HMAC
			// verification demands a bare []byte, while keyfunc returns an
			// ed25519.PublicKey, which is a named type, and the type assertion
			// fails. This list is the second layer - it still refuses if keyfunc is
			// one day changed to return []byte, which would turn the deliberately
			// distributed public key into a valid HMAC secret.
			jwt.WithValidMethods([]string{algorithm.Alg()}),
			jwt.WithIssuer(issuerName),
			// A token without exp never stops being valid. Expiry is required, not
			// merely checked when present.
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		),
	}, nil
}

var _ domain.TokenVerifier = (*Verifier)(nil)

func (v *Verifier) Verify(raw string) (domain.Claims, error) {
	var c claims

	// Every failure is wrapped into the same ErrInvalidToken, and the original
	// error is wrapped inside so the server log can still tell a wrong
	// signature from an expired token.
	//
	// What MUST NOT tell them apart is the answer to the client: telling an
	// attacker the signature was right but expired is telling them the key has
	// leaked. That uniformity is applied in the HTTP error mapping, not by
	// discarding the detail here.
	_, err := v.parser.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) {
		return v.key, nil
	})
	if err != nil {
		return domain.Claims{}, fmt.Errorf("%w: %w", domain.ErrInvalidToken, err)
	}

	userID, err := domain.ParseUserID(c.Subject)
	if err != nil {
		return domain.Claims{}, fmt.Errorf("%w: subject is not a user id", domain.ErrInvalidToken)
	}
	role, err := domain.NewRole(c.Role)
	if err != nil {
		return domain.Claims{}, fmt.Errorf("%w: unknown role %q", domain.ErrInvalidToken, c.Role)
	}
	// A generation of zero means the claim is missing. Accepting it would let
	// a token without a generation survive every revocation.
	if c.Generation < 1 {
		return domain.Claims{}, fmt.Errorf("%w: missing token generation", domain.ErrInvalidToken)
	}
	if c.IssuedAt == nil || c.ExpiresAt == nil {
		return domain.Claims{}, fmt.Errorf("%w: missing iat or exp", domain.ErrInvalidToken)
	}

	return domain.Claims{
		UserID:        userID,
		UserProfileID: c.UserProfileID,
		Email:         c.Email,
		Role:          role,
		Generation:    c.Generation,
		IssuedAt:      c.IssuedAt.Time,
		ExpiresAt:     c.ExpiresAt.Time,
	}, nil
}
