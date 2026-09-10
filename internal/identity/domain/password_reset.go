package domain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrResetTokenInvalid covers every reason a reset token is refused:
	// missing, already used, expired, malformed. Callers MUST NOT tell them
	// apart - "this token existed but was already used" tells an attacker
	// their guess was right.
	ErrResetTokenInvalid = errors.New("invalid password reset token")

	ErrResetTokenExpired = errors.New("password reset token expired")
	ErrResetTokenUsed    = errors.New("password reset token already used")
)

// resetTokenBytes is 32 bytes, that is 256 bits of randomness.
//
// So much that guessing it is not a threat worth countering with rate
// limiting - unlike a password, which is chosen by a person and can
// therefore be guessed.
const resetTokenBytes = 32

// resetTokenLifetime is deliberately short. A reset token is a full
// credential: whoever holds it can take over the account. The longer it
// lives, the longer it sits in an inbox that may no longer be safe.
const resetTokenLifetime = time.Hour

// ResetToken is the secret sent to the user. Like Password, it cannot print
// itself.
type ResetToken struct {
	value string
}

// NewResetToken generates a new random token.
func NewResetToken() (ResetToken, error) {
	raw := make([]byte, resetTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return ResetToken{}, fmt.Errorf("generating reset token: %w", err)
	}
	// base64url so it is safe to paste into a link without re-encoding.
	return ResetToken{value: base64.RawURLEncoding.EncodeToString(raw)}, nil
}

// ParseResetToken accepts a token sent back by the user.
//
// It only checks the shape, not the validity. What decides valid or not is
// its row in storage.
func ParseResetToken(raw string) (ResetToken, error) {
	if raw == "" {
		return ResetToken{}, ErrResetTokenInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != resetTokenBytes {
		return ResetToken{}, ErrResetTokenInvalid
	}
	return ResetToken{value: raw}, nil
}

func (ResetToken) String() string   { return "[REDACTED]" }
func (ResetToken) GoString() string { return "domain.ResetToken{[REDACTED]}" }

// Expose releases the token. It has only two callers: the one composing the
// link to send, and the one computing its hash.
func (t ResetToken) Expose() string { return t.value }

// ResetTokenHash is what gets stored.
type ResetTokenHash [sha256.Size]byte

// HashResetToken computes the hash that is stored.
//
// SHA-256, not argon2, and that is deliberate. Argon2 resists guessing of
// passwords chosen by people; this token is 256 random bits, so nobody
// guesses it. Only one thing is defended against here: a leaked database
// MUST NOT directly mean the ability to take over accounts.
func HashResetToken(t ResetToken) ResetTokenHash {
	return sha256.Sum256([]byte(t.value))
}

// Equal compares two hashes in constant time.
func (h ResetTokenHash) Equal(other ResetTokenHash) bool {
	return subtle.ConstantTimeCompare(h[:], other[:]) == 1
}

// PasswordReset is one outstanding reset request.
type PasswordReset struct {
	TokenHash ResetTokenHash
	UserID    UserID
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// NewPasswordReset creates a new request together with its token.
//
// The token is returned separately and never stored inside the struct: once
// this return value has been used, the only remaining copy lives in the
// user's inbox.
func NewPasswordReset(userID UserID, now time.Time) (PasswordReset, ResetToken, error) {
	token, err := NewResetToken()
	if err != nil {
		return PasswordReset{}, ResetToken{}, err
	}
	return PasswordReset{
		TokenHash: HashResetToken(token),
		UserID:    userID,
		ExpiresAt: now.Add(resetTokenLifetime),
		CreatedAt: now,
	}, token, nil
}

// Redeem checks whether this token may still be used, and marks it used if
// so.
//
// The check and the marking sit in one place on purpose. If they were
// separate, there would always be a path that checks and then forgets to
// mark - and a single-use token that is never marked is a token that can be
// used many times.
func (r *PasswordReset) Redeem(now time.Time) error {
	if r.UsedAt != nil {
		return ErrResetTokenUsed
	}
	if !now.Before(r.ExpiresAt) {
		return ErrResetTokenExpired
	}
	at := now
	r.UsedAt = &at
	return nil
}

// PasswordResetRepository is the storage port for reset requests.
type PasswordResetRepository interface {
	Create(ctx context.Context, r PasswordReset) error

	// FindByTokenHash looks up by hash, not by email. A lookup by email would
	// let anyone's token be paired with anyone's address.
	FindByTokenHash(ctx context.Context, hash ResetTokenHash) (PasswordReset, error)

	// MarkUsed stores the used mark.
	MarkUsed(ctx context.Context, hash ResetTokenHash, usedAt time.Time) error

	// InvalidateAllFor cancels every outstanding request belonging to a user.
	//
	// Called once the password has actually changed: any other live request is
	// a still-valid credential for an account that was just secured, and the
	// most likely issuer of it is the person trying to seize that account.
	InvalidateAllFor(ctx context.Context, userID UserID, at time.Time) error
}
