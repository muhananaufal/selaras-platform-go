// Package postgres stores the identity aggregates in Postgres.
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

// Constraint names from the migrations. Used to translate index collisions
// into domain errors, so callers can tell "email already taken" from "the
// database is having trouble" without looking at SQLSTATE.
const (
	constraintEmailUnique    = "users_email_unique_alive"
	constraintGoogleIDUnique = "users_google_id_unique_alive"
)

// Columns are named one by one, never SELECT *. The column order of SELECT
// * is decided by the database, so adding a column through a migration
// could shift the Scan results without a single compile-time error.
const userColumns = `id, email, role, password_hash, google_id,
	email_verified_at, token_generation, created_at, updated_at, deleted_at`

// UserRepository implements domain.UserRepository.
type UserRepository struct {
	db pg.Querier
}

func NewUserRepository(db pg.Querier) *UserRepository { return &UserRepository{db: db} }

var _ domain.UserRepository = (*UserRepository)(nil)

// WithQuerier returns the same repository on top of a running transaction.
//
// This is what makes the outbox pattern possible: the use case starts a
// transaction, and the user row and the event row are written through the
// same Querier, so both survive together or not at all.
func (r *UserRepository) WithQuerier(q pg.Querier) *UserRepository {
	return &UserRepository{db: q}
}

func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	s := u.State()

	const q = `
		INSERT INTO users (` + userColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.db.Exec(ctx, q,
		s.ID.String(), s.Email.String(), s.Role.String(),
		nullable(string(s.PasswordHash)), nullable(s.GoogleID),
		s.EmailVerifiedAt, s.TokenGeneration, s.CreatedAt, s.UpdatedAt, s.DeletedAt,
	)
	if err != nil {
		return translate(err)
	}
	return nil
}

func (r *UserRepository) Update(ctx context.Context, u *domain.User) error {
	s := u.State()

	const q = `
		UPDATE users SET
			email = $2, role = $3, password_hash = $4, google_id = $5,
			email_verified_at = $6, token_generation = $7, updated_at = $8, deleted_at = $9
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, q,
		s.ID.String(), s.Email.String(), s.Role.String(),
		nullable(string(s.PasswordHash)), nullable(s.GoogleID),
		s.EmailVerifiedAt, s.TokenGeneration, s.UpdatedAt, s.DeletedAt,
	)
	if err != nil {
		return translate(err)
	}
	// Zero rows means the row does not exist. Without this check, saving an
	// already deleted user would succeed while changing nothing.
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// All three lookups filter on deleted_at IS NULL. Soft-deleted accounts are
// kept for audit, not for authentication, and forgetting even one of those
// filters means a deleted account could sign back in.

func (r *UserRepository) FindByID(ctx context.Context, id domain.UserID) (*domain.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE id = $1 AND deleted_at IS NULL`
	return r.one(ctx, q, id.String())
}

func (r *UserRepository) FindByEmail(ctx context.Context, email domain.Email) (*domain.User, error) {
	// The email has already been lowercased by domain.NewEmail, so the
	// comparison is direct and the index is used. Without normalisation in the
	// domain, this would need LOWER(email), and a plain unique index would no
	// longer prevent the same address in different casing.
	const q = `SELECT ` + userColumns + ` FROM users WHERE email = $1 AND deleted_at IS NULL`
	return r.one(ctx, q, email.String())
}

func (r *UserRepository) FindByGoogleID(ctx context.Context, googleID string) (*domain.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE google_id = $1 AND deleted_at IS NULL`
	return r.one(ctx, q, googleID)
}

func (r *UserRepository) one(ctx context.Context, query string, arg any) (*domain.User, error) {
	var (
		id, email, role string
		hash, googleID  *string
		verifiedAt      *time.Time
		generation      int64
		createdAt       time.Time
		updatedAt       time.Time
		deletedAt       *time.Time
	)

	err := r.db.QueryRow(ctx, query, arg).Scan(
		&id, &email, &role, &hash, &googleID,
		&verifiedAt, &generation, &createdAt, &updatedAt, &deletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying user: %w", err)
	}

	return hydrate(id, email, role, hash, googleID, verifiedAt, generation, createdAt, updatedAt, deletedAt)
}

func hydrate(
	id, email, role string,
	hash, googleID *string,
	verifiedAt *time.Time,
	generation int64,
	createdAt, updatedAt time.Time,
	deletedAt *time.Time,
) (*domain.User, error) {
	// A stored row is a fact, not a request - but it can still be corrupt, for
	// instance after a manual migration. Whatever fails to parse is reported,
	// not silently replaced with a default.
	parsedID, err := domain.ParseUserID(id)
	if err != nil {
		return nil, fmt.Errorf("stored user id is not a uuid: %w", err)
	}
	parsedEmail, err := domain.NewEmail(email)
	if err != nil {
		return nil, fmt.Errorf("stored email is invalid: %w", err)
	}
	parsedRole, err := domain.NewRole(role)
	if err != nil {
		return nil, fmt.Errorf("stored role is invalid: %w", err)
	}

	return domain.Hydrate(domain.UserState{
		ID:              parsedID,
		Email:           parsedEmail,
		Role:            parsedRole,
		PasswordHash:    domain.PasswordHash(deref(hash)),
		GoogleID:        deref(googleID),
		EmailVerifiedAt: verifiedAt,
		TokenGeneration: generation,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
		DeletedAt:       deletedAt,
	}), nil
}

// nullable maps an empty string to NULL.
//
// Absence is stored as absence. Otherwise the partial unique index on
// google_id would treat every non-Google user as holding the same google id
// - the empty string - and refuse the second registration. NULL never
// collides with NULL.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func translate(err error) error {
	switch {
	case pg.IsUniqueViolation(err, constraintEmailUnique):
		return domain.ErrEmailTaken
	case pg.IsUniqueViolation(err, constraintGoogleIDUnique):
		return domain.ErrGoogleIDTaken
	default:
		return fmt.Errorf("writing user: %w", err)
	}
}

// Delete removes the account permanently.
//
// DELETE, not a soft-delete mark. The deleted_at column used elsewhere serves
// audit, and audit is not reason enough to keep the email address and
// password hash of someone who asked for their account to be deleted.
//
// A missing row is NOT an error: the saga can repeat its last step after the
// process died, and the second run finds the account already gone.
func (r *UserRepository) Delete(ctx context.Context, id domain.UserID) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id.String()); err != nil {
		return fmt.Errorf("deleting the account: %w", err)
	}
	return nil
}
