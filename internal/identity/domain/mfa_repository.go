package domain

import (
	"context"
	"errors"
	"time"
)

// ErrNoTOTP is returned when the account has no TOTP row, pending or
// confirmed.
var ErrNoTOTP = errors.New("no TOTP enrolled")

// TOTPFactor is an account's authenticator-app secret, sealed. The domain
// never holds it open longer than one verification.
type TOTPFactor struct {
	UserID           UserID
	SecretCiphertext []byte
	KeyVersion       int64

	// ConfirmedAt is nil while enrolment is pending: the secret was shown but
	// never proven with a code, and it is not a factor yet.
	ConfirmedAt *time.Time

	// LastUsedStep is the last accepted time step; VerifyTOTP refuses it and
	// every earlier one.
	LastUsedStep int64
}

// Confirmed reports whether the secret counts as a factor.
func (f TOTPFactor) Confirmed() bool { return f.ConfirmedAt != nil }

// StoredRecoveryCode is a recovery code as stored: its lookup and the hash of
// its secret part.
type StoredRecoveryCode struct {
	Lookup string
	Hash   PasswordHash
}

// MFARepository is the storage port for second factors (ADR-032). Like the
// user repository it is used inside a unit of work: verifying a code, moving
// its time step forward and counting the attempt must commit together.
type MFARepository interface {
	// FindTOTP returns ErrNoTOTP when there is no row.
	FindTOTP(ctx context.Context, id UserID) (TOTPFactor, error)
	// SaveTOTP inserts or replaces the account's row.
	SaveTOTP(ctx context.Context, f TOTPFactor) error
	DeleteTOTP(ctx context.Context, id UserID) error

	// ReplaceRecoveryCodes deletes every code of the account and stores codes.
	ReplaceRecoveryCodes(ctx context.Context, id UserID, codes []StoredRecoveryCode) error
	DeleteRecoveryCodes(ctx context.Context, id UserID) error
	// FindUnusedRecoveryCode returns ErrInvalidSecondFactor when no unused
	// code has this lookup.
	FindUnusedRecoveryCode(ctx context.Context, id UserID, lookup string) (StoredRecoveryCode, error)
	// MarkRecoveryCodeUsed returns ErrInvalidSecondFactor when the code was
	// used meanwhile: a code is accepted once (NIST SP 800-63B 3.1.2.2).
	MarkRecoveryCodeUsed(ctx context.Context, id UserID, lookup string, at time.Time) error
	CountUnusedRecoveryCodes(ctx context.Context, id UserID) (int, error)

	CountPasskeys(ctx context.Context, id UserID) (int, error)

	// LockAttempts reads the failure count and holds it until the unit of
	// work ends, so two verifications in parallel cannot both read the same
	// count. An account without a row has the zero value.
	LockAttempts(ctx context.Context, id UserID) (MFAAttempts, error)
	SaveAttempts(ctx context.Context, id UserID, a MFAAttempts) error
}
