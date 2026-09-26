// Package postgres stores risk assessments in Postgres.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

const constraintSlugUnique = "risk_assessments_slug_unique"

const assessmentColumns = `id, user_profile_id, slug, model_used, final_risk_percentage,
	inputs, generated_values, result_details, created_at, updated_at`

// readColumns adds the columns assessmentColumns does not list: the ones not
// written at creation, and user_id, which the INSERT appends on its own.
//
// It is separate from assessmentColumns because the latter is also used by the
// INSERT, and adding columns there would make its placeholder count stop
// matching - a mistake only visible at runtime.
const readColumns = assessmentColumns + `, personalization_status, coalesce(personalization_error, ''),
	coalesce(user_id::text, '')`

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
		INSERT INTO risk_assessments (` + assessmentColumns + `, user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	var userID any
	if a.UserID != "" {
		userID = a.UserID
	}
	_, err = r.db.Exec(ctx, q,
		a.ID.String(), a.UserProfileID.String(), a.Slug, a.ModelUsed, a.RiskPercentage,
		inputs, generated, nullableJSON(a.ResultDetails), a.CreatedAt, a.UpdatedAt, userID,
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
	after *domain.HistoryCursor,
) ([]*domain.Assessment, error) {
	return r.list(ctx, byProfile, profileID.String(), limit, after)
}

// ListForUser reads by the owning user's id (migration 0008), for a
// clinician's read (ADR-030): it needs no profile lookup, so it cannot lag
// behind the profile cache.
func (r *Repository) ListForUser(
	ctx context.Context,
	userID string,
	limit int,
	after *domain.HistoryCursor,
) ([]*domain.Assessment, error) {
	return r.list(ctx, byUser, userID, limit, after)
}

// historyQuery is one keyset-paged history query: its first page and every
// page after a cursor.
type historyQuery struct{ first, next string }

// The ordering and the limit are in the query, not in Go. Reading the whole
// history and cutting it in memory moves database work into the service, and
// the composite indexes (migrations 0006 and 0009) were made for exactly
// these queries: equality on the key, then (created_at, id) in the index's
// own order, so a page is a range scan that stops after limit rows however
// deep into the history it starts.
//
// Two statements rather than one with "$2 IS NULL OR ...": a generic plan
// cannot use the row comparison as an index bound when it may be switched off
// at run time.
var (
	byProfile = historyQuery{
		first: `SELECT ` + readColumns + ` FROM risk_assessments WHERE user_profile_id = $1
			ORDER BY created_at DESC, id DESC LIMIT $2`,
		next: `SELECT ` + readColumns + ` FROM risk_assessments WHERE user_profile_id = $1
			AND (created_at, id) < ($3, $4) ORDER BY created_at DESC, id DESC LIMIT $2`,
	}
	byUser = historyQuery{
		first: `SELECT ` + readColumns + ` FROM risk_assessments WHERE user_id = $1
			ORDER BY created_at DESC, id DESC LIMIT $2`,
		next: `SELECT ` + readColumns + ` FROM risk_assessments WHERE user_id = $1
			AND (created_at, id) < ($3, $4) ORDER BY created_at DESC, id DESC LIMIT $2`,
	}
)

func (r *Repository) list(
	ctx context.Context, hq historyQuery, key string, limit int, after *domain.HistoryCursor,
) ([]*domain.Assessment, error) {
	q, args := hq.first, []any{key, limit}
	if after != nil {
		q, args = hq.next, append(args, after.CreatedAt, after.ID.String())
	}

	rows, err := r.db.Query(ctx, q, args...)
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
		userID                     string
	)

	if err := s.Scan(&id, &profileID, &slug, &model, &risk,
		&inputs, &generated, &details, &createdAt, &updatedAt,
		&personalization, &failure, &userID); err != nil {
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
		UserID:                userID,
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

// OwnerBackfill is one batch of BackfillOwners.
type OwnerBackfill struct {
	// Last is the last profile-cache user id the batch covered: the cursor
	// for the next batch. Empty when the batch covered nobody - the end.
	Last string

	// Updated counts the assessments that got their owner in this batch.
	Updated int64
}

// BackfillOwners fills user_id on assessments written before migration 0008,
// for the next batch of users in the profile cache after afterUserID.
//
// The batch walks the cache by its primary key (keyset, not OFFSET, and not
// "WHERE user_id IS NULL LIMIT n", which rescans the rows it could not fill
// on every batch), and each user's rows are found through the history index
// of migration 0006. Each batch is its own short statement, outside any
// migration's transaction (runbook migrations: backfill in small batches).
//
// It is idempotent: only rows still without an owner are written, so a run
// that stops halfway is resumed by running it again.
func (r *Repository) BackfillOwners(ctx context.Context, afterUserID string, batch int) (OwnerBackfill, error) {
	const q = `
		WITH users AS (
			SELECT user_id, user_profile_id FROM profile_snapshots
			WHERE user_id > $1 ORDER BY user_id LIMIT $2
		), filled AS (
			UPDATE risk_assessments r SET user_id = u.user_id
			FROM users u
			WHERE r.user_profile_id = u.user_profile_id AND r.user_id IS NULL
			RETURNING 1
		)
		SELECT coalesce((SELECT user_id::text FROM users ORDER BY user_id DESC LIMIT 1), ''),
		       (SELECT count(*) FROM filled)`

	// The first batch starts below every id: the nil uuid is no user's id.
	if afterUserID == "" {
		afterUserID = uuid.Nil.String()
	}
	var out OwnerBackfill
	if err := r.db.QueryRow(ctx, q, afterUserID, batch).Scan(&out.Last, &out.Updated); err != nil {
		return OwnerBackfill{}, fmt.Errorf("backfilling assessment owners: %w", err)
	}
	return out, nil
}

// CountWithoutOwner counts the assessments that still have no user_id: rows
// whose profile the cache does not know yet. Clinicians' reads do not see
// them until they are filled.
func (r *Repository) CountWithoutOwner(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM risk_assessments WHERE user_id IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting assessments without an owner: %w", err)
	}
	return n, nil
}
