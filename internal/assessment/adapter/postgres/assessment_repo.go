// Package postgres stores risk assessments in Postgres.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

const constraintSlugUnique = "risk_assessments_slug_unique"

const assessmentColumns = `id, user_profile_id, slug, model_used, final_risk_percentage,
	inputs, generated_values, result_details, created_at, updated_at`

// readColumns adds the columns that are NOT written at creation.
//
// It is separate from assessmentColumns because the latter is also used by the
// INSERT, and adding columns there would make its placeholder count stop
// matching - a mistake only visible at runtime.
const readColumns = assessmentColumns + `, personalization_status, coalesce(personalization_error, '')`

// Repository implements domain.Repository.
type Repository struct {
	db pg.Querier
}

func NewRepository(db pg.Querier) *Repository { return &Repository{db: db} }

var _ domain.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, a *domain.Assessment) error {
	inputs, err := json.Marshal(orEmpty(a.Inputs))
	if err != nil {
		return fmt.Errorf("encoding inputs: %w", err)
	}
	generated, err := json.Marshal(orEmpty(a.GeneratedValues))
	if err != nil {
		return fmt.Errorf("encoding generated values: %w", err)
	}

	const q = `
		INSERT INTO risk_assessments (` + assessmentColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err = r.db.Exec(ctx, q,
		a.ID.String(), a.UserProfileID.String(), a.Slug, a.ModelUsed, a.RiskPercentage,
		inputs, generated, nullableJSON(a.ResultDetails), a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		if pg.IsUniqueViolation(err, constraintSlugUnique) {
			return domain.ErrSlugTaken
		}
		return fmt.Errorf("storing assessment: %w", err)
	}
	return nil
}

func (r *Repository) FindBySlug(ctx context.Context, slug string) (*domain.Assessment, error) {
	const q = `SELECT ` + readColumns + ` FROM risk_assessments WHERE slug = $1`

	row := r.db.QueryRow(ctx, q, slug)
	a, err := scanAssessment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAssessmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying assessment: %w", err)
	}
	return a, nil
}

func (r *Repository) ListForProfile(
	ctx context.Context,
	profileID domain.ProfileID,
	limit int,
) ([]*domain.Assessment, error) {
	// The ordering and the limit are in the query, not in Go. Reading the
	// whole history and cutting it in memory moves database work into the
	// service, and the composite index in the migration was made for exactly
	// this query.
	const q = `
		SELECT ` + readColumns + `
		FROM risk_assessments
		WHERE user_profile_id = $1
		ORDER BY created_at DESC
		LIMIT $2`

	rows, err := r.db.Query(ctx, q, profileID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("querying assessments: %w", err)
	}
	defer rows.Close()

	var out []*domain.Assessment
	for rows.Next() {
		a, err := scanAssessment(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning assessment: %w", err)
		}
		out = append(out, a)
	}
	// The iteration error is checked separately from the Scan error. Rows that
	// run out halfway because the connection dropped look like a complete
	// result if this is skipped - a short list that looks right.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading assessments: %w", err)
	}
	return out, nil
}

// scanner unifies pgx.Row and pgx.Rows, both of which can be Scanned.
type scanner interface {
	Scan(dest ...any) error
}

func scanAssessment(s scanner) (*domain.Assessment, error) {
	var (
		id, profileID, slug, model string
		risk                       float64
		inputs, generated, details []byte
		createdAt, updatedAt       time.Time
		personalization, failure   string
	)

	if err := s.Scan(&id, &profileID, &slug, &model, &risk,
		&inputs, &generated, &details, &createdAt, &updatedAt,
		&personalization, &failure); err != nil {
		return nil, err
	}

	parsedID, err := domain.ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("stored assessment id is not a uuid: %w", err)
	}
	parsedProfile, err := domain.ParseProfileID(profileID)
	if err != nil {
		return nil, fmt.Errorf("stored profile id is not a uuid: %w", err)
	}

	a := &domain.Assessment{
		ID:                    parsedID,
		UserProfileID:         parsedProfile,
		Slug:                  slug,
		PersonalizationStatus: domain.PersonalizationStatus(personalization),
		PersonalizationError:  failure,
		ModelUsed:             model,
		RiskPercentage:        risk,
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
	}

	if a.Inputs, err = decode(inputs); err != nil {
		return nil, fmt.Errorf("decoding inputs: %w", err)
	}
	if a.GeneratedValues, err = decode(generated); err != nil {
		return nil, fmt.Errorf("decoding generated values: %w", err)
	}
	if a.ResultDetails, err = decode(details); err != nil {
		return nil, fmt.Errorf("decoding result details: %w", err)
	}

	return a, nil
}

func decode(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// orEmpty keeps a NOT NULL column filled.
//
// A nil Go map becomes "null" in JSON, and the column refuses it. An empty
// object is more honest than an assessment that fails to save because its
// answers happen to be empty.
func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// nullableJSON stores NULL for what is not there yet, not an empty object.
//
// An empty result_details and an unfilled one are two different things: the
// first means llm-worker answered and found nothing, the second means it
// has not answered yet.
func nullableJSON(m map[string]any) any {
	if m == nil {
		return nil
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return encoded
}

// SetResultDetails stores the personalisation report.
//
// The `result_details IS NULL` condition sits in the WHERE, not checked first
// with a SELECT. A preliminary check has a gap between reading and writing,
// and two events arriving together would both read "nothing yet" and both
// write - the second overwriting the first.
func (r *Repository) SetResultDetails(
	ctx context.Context, id domain.ID, report map[string]any,
) (bool, error) {
	encoded, err := json.Marshal(report)
	if err != nil {
		return false, fmt.Errorf("encoding the personalisation report: %w", err)
	}

	const q = `
		UPDATE risk_assessments
		SET result_details = $2, updated_at = now()
		WHERE id = $1 AND result_details IS NULL`

	tag, err := r.db.Exec(ctx, q, id.String(), encoded)
	if err != nil {
		return false, fmt.Errorf("storing the personalisation report: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// SetPersonalizationStatus records the state of the personalisation job.
//
// The allowed transitions are enforced inside the WHERE, not checked
// beforehand. A preliminary check has a gap between reading and writing: two
// events arriving together would both read the old state, and the late one
// would overwrite the newer.
func (r *Repository) SetPersonalizationStatus(
	ctx context.Context,
	id domain.ID,
	to domain.PersonalizationStatus,
	from []domain.PersonalizationStatus,
	failure string,
) (bool, error) {
	q := `
		UPDATE risk_assessments
		SET personalization_status = $2, personalization_error = $3, updated_at = now()
		WHERE id = $1`

	args := []any{id.String(), string(to), nullable(failure)}
	if len(from) > 0 {
		allowed := make([]string, 0, len(from))
		for _, s := range from {
			allowed = append(allowed, string(s))
		}
		q += ` AND personalization_status = ANY($4)`
		args = append(args, allowed)
	}

	tag, err := r.db.Exec(ctx, q, args...)
	if err != nil {
		return false, fmt.Errorf("recording the personalisation status: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// nullable turns an empty string into NULL.
//
// The column stores the failure reason, and a stored empty string would look
// like "failed for no reason" - which differs from "did not fail".
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
