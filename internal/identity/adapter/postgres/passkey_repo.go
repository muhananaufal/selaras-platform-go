package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// PasskeyRepository stores WebAuthn credentials (ADR-032, migration
// identity 0005).
type PasskeyRepository struct {
	db pg.Querier
}

func NewPasskeyRepository(db pg.Querier) *PasskeyRepository { return &PasskeyRepository{db: db} }

var _ app.PasskeyRepository = (*PasskeyRepository)(nil)

// Handle inserts fresh when the account has no handle, then reads whichever
// handle won: two first registrations at once end with one handle.
func (r *PasskeyRepository) Handle(ctx context.Context, id domain.UserID, fresh []byte) ([]byte, error) {
	if _, err := r.db.Exec(ctx,
		`INSERT INTO webauthn_user_handles (user_id, handle) VALUES ($1, $2) ON CONFLICT (user_id) DO NOTHING`,
		id.String(), fresh); err != nil {
		return nil, fmt.Errorf("creating the user handle: %w", err)
	}
	var handle []byte
	if err := r.db.QueryRow(ctx, `SELECT handle FROM webauthn_user_handles WHERE user_id = $1`, id.String()).Scan(&handle); err != nil {
		return nil, fmt.Errorf("reading the user handle: %w", err)
	}
	return handle, nil
}

func (r *PasskeyRepository) UserByHandle(ctx context.Context, handle []byte) (domain.UserID, error) {
	var raw string
	err := r.db.QueryRow(ctx, `SELECT user_id FROM webauthn_user_handles WHERE handle = $1`, handle).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserID{}, domain.ErrUserNotFound
	}
	if err != nil {
		return domain.UserID{}, fmt.Errorf("reading the user handle: %w", err)
	}
	return domain.ParseUserID(raw)
}

func (r *PasskeyRepository) List(ctx context.Context, id domain.UserID) ([]app.StoredPasskey, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, credential_id, credential, created_at, last_used_at
		FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at, id`, id.String())
	if err != nil {
		return nil, fmt.Errorf("listing passkeys: %w", err)
	}
	defer rows.Close()
	var out []app.StoredPasskey
	for rows.Next() {
		var p app.StoredPasskey
		if err := rows.Scan(&p.ID, &p.Name, &p.CredentialID, &p.Credential, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, fmt.Errorf("reading a passkey: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *PasskeyRepository) Add(ctx context.Context, id domain.UserID, p app.StoredPasskey) error {
	if _, err := r.db.Exec(ctx, `
		INSERT INTO webauthn_credentials (id, user_id, credential_id, credential, name, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		p.ID, id.String(), p.CredentialID, p.Credential, p.Name, p.CreatedAt); err != nil {
		return fmt.Errorf("storing a passkey: %w", err)
	}
	return nil
}

func (r *PasskeyRepository) Touch(ctx context.Context, credentialID, credential []byte, at time.Time) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE webauthn_credentials SET credential = $2, last_used_at = $3 WHERE credential_id = $1`,
		credentialID, credential, at)
	if err != nil {
		return fmt.Errorf("updating a passkey: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrPasskeyRejected
	}
	return nil
}

func (r *PasskeyRepository) Delete(ctx context.Context, id domain.UserID, passkeyID string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM webauthn_credentials WHERE user_id = $1 AND id::text = $2`, id.String(), passkeyID)
	if err != nil {
		return fmt.Errorf("deleting a passkey: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrPasskeyNotFound
	}
	return nil
}
