package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// MFARepository stores second factors (ADR-032, migration identity 0005).
type MFARepository struct {
	db pg.Querier
}

func NewMFARepository(db pg.Querier) *MFARepository { return &MFARepository{db: db} }

var _ domain.MFARepository = (*MFARepository)(nil)

func (r *MFARepository) FindTOTP(ctx context.Context, id domain.UserID) (domain.TOTPFactor, error) {
	const q = `
		SELECT secret_ciphertext, key_version, confirmed_at, last_used_step
		FROM mfa_totp WHERE user_id = $1`
	f := domain.TOTPFactor{UserID: id}
	err := r.db.QueryRow(ctx, q, id.String()).Scan(&f.SecretCiphertext, &f.KeyVersion, &f.ConfirmedAt, &f.LastUsedStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TOTPFactor{}, domain.ErrNoTOTP
	}
	if err != nil {
		return domain.TOTPFactor{}, fmt.Errorf("reading TOTP: %w", err)
	}
	return f, nil
}

func (r *MFARepository) SaveTOTP(ctx context.Context, f domain.TOTPFactor) error {
	const q = `
		INSERT INTO mfa_totp (user_id, secret_ciphertext, key_version, confirmed_at, last_used_step)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE SET
			secret_ciphertext = EXCLUDED.secret_ciphertext,
			key_version       = EXCLUDED.key_version,
			confirmed_at      = EXCLUDED.confirmed_at,
			last_used_step    = EXCLUDED.last_used_step`
	if _, err := r.db.Exec(ctx, q, f.UserID.String(), f.SecretCiphertext, f.KeyVersion, f.ConfirmedAt, f.LastUsedStep); err != nil {
		return fmt.Errorf("storing TOTP: %w", err)
	}
	return nil
}

func (r *MFARepository) DeleteTOTP(ctx context.Context, id domain.UserID) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM mfa_totp WHERE user_id = $1`, id.String()); err != nil {
		return fmt.Errorf("deleting TOTP: %w", err)
	}
	return nil
}

func (r *MFARepository) ReplaceRecoveryCodes(ctx context.Context, id domain.UserID, codes []domain.StoredRecoveryCode) error {
	if err := r.DeleteRecoveryCodes(ctx, id); err != nil {
		return err
	}
	lookups := make([]string, 0, len(codes))
	hashes := make([]string, 0, len(codes))
	for _, c := range codes {
		lookups = append(lookups, c.Lookup)
		hashes = append(hashes, string(c.Hash))
	}
	const q = `
		INSERT INTO mfa_recovery_codes (user_id, lookup, code_hash)
		SELECT $1, l, h FROM unnest($2::text[], $3::text[]) AS c(l, h)`
	if _, err := r.db.Exec(ctx, q, id.String(), lookups, hashes); err != nil {
		return fmt.Errorf("storing recovery codes: %w", err)
	}
	return nil
}

func (r *MFARepository) DeleteRecoveryCodes(ctx context.Context, id domain.UserID) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM mfa_recovery_codes WHERE user_id = $1`, id.String()); err != nil {
		return fmt.Errorf("deleting recovery codes: %w", err)
	}
	return nil
}

func (r *MFARepository) FindUnusedRecoveryCode(ctx context.Context, id domain.UserID, lookup string) (domain.StoredRecoveryCode, error) {
	const q = `
		SELECT code_hash FROM mfa_recovery_codes
		WHERE user_id = $1 AND lookup = $2 AND used_at IS NULL`
	var hash string
	err := r.db.QueryRow(ctx, q, id.String(), lookup).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StoredRecoveryCode{}, domain.ErrInvalidSecondFactor
	}
	if err != nil {
		return domain.StoredRecoveryCode{}, fmt.Errorf("reading recovery code: %w", err)
	}
	return domain.StoredRecoveryCode{Lookup: lookup, Hash: domain.PasswordHash(hash)}, nil
}

func (r *MFARepository) MarkRecoveryCodeUsed(ctx context.Context, id domain.UserID, lookup string, at time.Time) error {
	const q = `
		UPDATE mfa_recovery_codes SET used_at = $3
		WHERE user_id = $1 AND lookup = $2 AND used_at IS NULL`
	tag, err := r.db.Exec(ctx, q, id.String(), lookup, at)
	if err != nil {
		return fmt.Errorf("using recovery code: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrInvalidSecondFactor
	}
	return nil
}

func (r *MFARepository) CountUnusedRecoveryCodes(ctx context.Context, id domain.UserID) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM mfa_recovery_codes WHERE user_id = $1 AND used_at IS NULL`, id.String()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting recovery codes: %w", err)
	}
	return n, nil
}

func (r *MFARepository) CountPasskeys(ctx context.Context, id domain.UserID) (int, error) {
	var n int
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM webauthn_credentials WHERE user_id = $1`, id.String()).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting passkeys: %w", err)
	}
	return n, nil
}

// LockAttempts makes sure the row exists, then reads it FOR UPDATE: a second
// verification of the same account waits here until the first commits its
// count, instead of reading the same number and overwriting it.
func (r *MFARepository) LockAttempts(ctx context.Context, id domain.UserID) (domain.MFAAttempts, error) {
	if _, err := r.db.Exec(ctx,
		`INSERT INTO mfa_attempts (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, id.String()); err != nil {
		return domain.MFAAttempts{}, fmt.Errorf("creating the attempts row: %w", err)
	}
	var (
		a           domain.MFAAttempts
		lockedUntil *time.Time
	)
	if err := r.db.QueryRow(ctx,
		`SELECT consecutive_failures, locked_until FROM mfa_attempts WHERE user_id = $1 FOR UPDATE`,
		id.String()).Scan(&a.ConsecutiveFailures, &lockedUntil); err != nil {
		return domain.MFAAttempts{}, fmt.Errorf("locking the attempts row: %w", err)
	}
	if lockedUntil != nil {
		a.LockedUntil = *lockedUntil
	}
	return a, nil
}

func (r *MFARepository) SaveAttempts(ctx context.Context, id domain.UserID, a domain.MFAAttempts) error {
	var lockedUntil *time.Time
	if !a.LockedUntil.IsZero() {
		lockedUntil = &a.LockedUntil
	}
	if _, err := r.db.Exec(ctx,
		`UPDATE mfa_attempts SET consecutive_failures = $2, locked_until = $3 WHERE user_id = $1`,
		id.String(), a.ConsecutiveFailures, lockedUntil); err != nil {
		return fmt.Errorf("storing attempts: %w", err)
	}
	return nil
}
