package domain

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrInvalidToken covers every reason a token is refused: wrong signature,
	// expired, algorithm not allowed, incomplete claims. Callers must not tell
	// them apart in the response to the client - telling an attacker the
	// signature was right but expired is telling them the key has leaked.
	ErrInvalidToken = errors.New("invalid token")

	// ErrTokenRevoked is separate because it is used only inside the system,
	// to decide whether the user has to be asked to sign in again.
	ErrTokenRevoked = errors.New("token revoked")
)

// Claims are the contents of an access token.
//
// UserProfileID is carried because of ADR-007: without it, every unit that
// needs the profile would have to ask identity-svc first, and that is one
// mandatory network call on every authenticated request.
//
// Generation carries the user's token generation at issue time. It is what
// makes revocation possible without keeping a list of tokens.
type Claims struct {
	UserID        UserID
	UserProfileID string

	// Email is carried because the REST contract promises it in two places,
	// and the alternative is asking identity-svc on every request - exactly
	// what ADR-007 removed.
	//
	// It is no secret to the token's holder: it is their own address. The cost
	// is a slightly larger token, and an address that changes is only
	// reflected once the token is renewed.
	Email      string
	Role       Role
	Generation int64
	IssuedAt   time.Time
	ExpiresAt  time.Time
}

// TokenIssuer signs claims into an access token. Its implementation holds
// the private key, and only identity-svc may have it.
type TokenIssuer interface {
	Issue(c Claims) (string, error)
}

// TokenVerifier checks a token's signature and validity period.
//
// It deliberately does NOT check revocation. Signature verification is pure
// and can be done by anyone holding the public key; a revocation check
// needs shared state and can fail. Combining the two would force every
// consumer to carry a storage connection just to read a claim.
type TokenVerifier interface {
	Verify(raw string) (Claims, error)
}

// RevocationChecker answers whether the generation carried by a token is
// still the current one for that user.
//
// This is a separate port, and that is what keeps ADR-012 meaningful: a
// claims-bearing token removes the call to identity-svc, while the revocation
// check can be served by a far cheaper shared store.
//
// Implementations MUST fail closed. A store that cannot be reached means
// revocation cannot be proven, and accepting the token in that state turns
// every outage into a window in which logout does not apply.
type RevocationChecker interface {
	// IsCurrent is true when the generation is still the current one for that
	// user.
	IsCurrent(ctx context.Context, userID UserID, generation int64) (bool, error)
}

// RevocationPublisher mengumumkan generasi baru seorang pengguna setelah
// perubahannya tersimpan.
type RevocationPublisher interface {
	PublishGeneration(ctx context.Context, userID UserID, generation int64) error
}
