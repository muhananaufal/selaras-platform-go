package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// AssessmentRepository implements domain.AssessmentRepository.
type AssessmentRepository struct {
	db pg.Querier
}

func NewAssessmentRepository(db pg.Querier) *AssessmentRepository {
	return &AssessmentRepository{db: db}
}

var _ domain.AssessmentRepository = (*AssessmentRepository)(nil)

// Record stores a reference; an id that already exists is left as it is.
func (r *AssessmentRepository) Record(
	ctx context.Context, ref *domain.AssessmentRef,
) (bool, error) {
	// Not encodeJSON: even an empty snapshot is stored as {}, because the column
	// is NOT NULL and "no snapshot" is not a valid state for a reference.
	snapshot, err := json.Marshal(ref.Snapshot)
	if err != nil {
		return false, fmt.Errorf("encoding the assessment snapshot: %w", err)
	}

	const q = `
		INSERT INTO coaching_assessments (id, user_id, slug, snapshot, completed_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING`

	tag, err := r.db.Exec(ctx, q,
		ref.ID, ref.UserID.String(), ref.Slug, snapshot, ref.CompletedAt)
	if err != nil {
		return false, fmt.Errorf("recording the assessment: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// FindBySlug looks a reference up by its public slug.
func (r *AssessmentRepository) FindBySlug(
	ctx context.Context, slug string,
) (*domain.AssessmentRef, error) {
	const q = `
		SELECT id, user_id, slug, snapshot, completed_at
		FROM coaching_assessments
		WHERE slug = $1`

	var (
		ref      domain.AssessmentRef
		userID   string
		snapshot []byte
	)
	err := r.db.QueryRow(ctx, q, slug).Scan(
		&ref.ID, &userID, &ref.Slug, &snapshot, &ref.CompletedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, domain.ErrAssessmentNotFound
	case err != nil:
		return nil, fmt.Errorf("finding the assessment: %w", err)
	}

	if ref.UserID, err = domain.ParseUserID(userID); err != nil {
		return nil, fmt.Errorf("the stored assessment owner is not a user id: %w", err)
	}
	if ref.Snapshot, err = decodeJSON(snapshot); err != nil {
		return nil, fmt.Errorf("decoding the assessment snapshot: %w", err)
	}
	ref.CompletedAt = ref.CompletedAt.In(time.UTC)
	return &ref, nil
}
