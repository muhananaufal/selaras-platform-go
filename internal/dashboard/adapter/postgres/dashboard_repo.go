// Package postgres stores the dashboard read-model in Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Repository implements domain.Repository.
//
// Two connections: db for writing and reader for reading. The dashboard
// read-model is the most read and least written table in the whole system,
// so it is the first candidate for a read replica (F9-32). Projections
// (ApplyAssessment, ApplyProgram, Forget) ALWAYS go to db: writing to a
// replica is impossible, and reading then writing across connections would
// make the ordering guard compare against a state that lags behind.
type Repository struct {
	db     pg.Querier
	reader pg.Querier
}

// NewRepository reads and writes through one connection.
func NewRepository(db pg.Querier) *Repository { return &Repository{db: db, reader: db} }

// NewRepositoryWithReader reads through reader (the replica) and writes
// through db (the primary).
//
// Reads from the replica may lag a few hundred milliseconds behind the
// primary - the lag is measured and stated in docs/db-connections.md. For the
// dashboard that is acceptable: the projection itself already lags hundreds
// of milliseconds behind the events (F7), and clients were never promised a
// read immediately after a write.
func NewRepositoryWithReader(db, reader pg.Querier) *Repository {
	if reader == nil {
		reader = db
	}
	return &Repository{db: db, reader: reader}
}

var _ domain.Repository = (*Repository)(nil)

// Find reads one dashboard together with its history.
//
// Its summary - the latest assessment, the previous one, and the count - is
// DERIVED from the history, not read from columns updated on every event. The
// first version stored them as columns, and that left "previous assessment"
// empty forever when two events arrived reversed. What is derived on read is
// correct for any order of arrival.
func (r *Repository) Find(ctx context.Context, userID domain.UserID) (*domain.Dashboard, error) {
	const q = `
		SELECT
			program_slug, program_title, program_status,
			program_current_day, program_total_days, program_completion_percentage,
			projected_at
		FROM dashboards
		WHERE user_id = $1`

	var (
		dash                      domain.Dashboard
		programSlug, programTitle *string
		programStatus             *string
		currentDay, totalDays     *int
		completion                *float64
	)

	err := r.reader.QueryRow(ctx, q, userID.String()).Scan(
		&programSlug, &programTitle, &programStatus,
		&currentDay, &totalDays, &completion,
		&dash.ProjectedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNoDashboard
	}
	if err != nil {
		return nil, fmt.Errorf("querying the dashboard: %w", err)
	}

	dash.UserID = userID

	if programSlug != nil {
		dash.Program = &domain.Program{
			Slug:       *programSlug,
			Title:      deref(programTitle),
			Status:     deref(programStatus),
			CurrentDay: derefInt(currentDay),
			TotalDays:  derefInt(totalDays),
			Completion: completion,
		}
	}

	history, err := r.history(ctx, userID)
	if err != nil {
		return nil, err
	}
	dash.History = history
	dash.Total = len(history)

	// Latest and previous are the top two rows of the history, which is
	// already ordered by ASSESSMENT TIME - not by order of arrival.
	if len(history) > 0 {
		dash.Latest = history[0]
	}
	if len(history) > 1 {
		previous := history[1].RiskPercentage
		dash.Previous = &previous
	}

	return &dash, nil
}

// history reads the assessment history, newest first.
func (r *Repository) history(ctx context.Context, userID domain.UserID) ([]*domain.Assessment, error) {
	const q = `
		SELECT slug, assessed_at, risk_percentage, risk_category, model_used
		FROM dashboard_assessments
		WHERE user_id = $1
		ORDER BY assessed_at DESC, slug DESC`

	rows, err := r.reader.Query(ctx, q, userID.String())
	if err != nil {
		return nil, fmt.Errorf("querying the assessment history: %w", err)
	}
	defer rows.Close()

	// An empty slice, not nil: nil becomes `null` in JSON.
	out := make([]*domain.Assessment, 0, 8)
	for rows.Next() {
		var a domain.Assessment
		if err := rows.Scan(&a.Slug, &a.AssessedAt, &a.RiskPercentage,
			&a.RiskCategory, &a.ModelUsed); err != nil {
			return nil, fmt.Errorf("reading an assessment: %w", err)
		}
		out = append(out, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating the assessment history: %w", err)
	}
	return out, nil
}

// ApplyAssessment enters one assessment into the projection.
//
// Two writes, both IDEMPOTENT, and both have to succeed or fail together -
// the caller runs both inside one transaction.
func (r *Repository) ApplyAssessment(
	ctx context.Context, userID domain.UserID, a *domain.Assessment, occurredAt time.Time,
) error {
	if a == nil {
		return errors.New("nil assessment")
	}

	// The history first. ON CONFLICT DO NOTHING: a second delivery from the
	// at-least-once relay adds no row at all.
	const insertHistory = `
		INSERT INTO dashboard_assessments
			(user_id, slug, assessed_at, risk_percentage, risk_category, model_used)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, slug) DO NOTHING`

	tag, err := r.db.Exec(ctx, insertHistory,
		userID.String(), a.Slug, a.AssessedAt, a.RiskPercentage, a.RiskCategory, a.ModelUsed)
	if err != nil {
		return fmt.Errorf("projecting the assessment: %w", err)
	}

	// An existing row means this event HAS already been applied.
	//
	// The projection time is still advanced below - a redelivery is still an
	// occurrence - but not a single number can shift, because no number is
	// stored. The gate is enforced by the database through the primary key
	// (user_id, slug), not by a SELECT followed by an INSERT with a gap
	// between them where two processes both read "not there yet".
	if tag.RowsAffected() == 0 {
		return nil
	}

	// Then the row is touched so its projection time advances - and that is
	// ALL.
	//
	// There is no summary to update: the latest assessment, the previous one,
	// and the count are derived from the history when READ. The first version
	// stored the three as columns and updated them through a series of CASE
	// expressions comparing times; that left "previous assessment" empty
	// forever when two events arrived reversed - an ordinary state, since
	// Kafka guarantees order per partition key and assessments are keyed on
	// their assessment id, not their user.
	const touch = `
		INSERT INTO dashboards (user_id, projected_at, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE SET
			projected_at = GREATEST(dashboards.projected_at, EXCLUDED.projected_at),
			updated_at = now()`

	if _, err := r.db.Exec(ctx, touch, userID.String(), occurredAt); err != nil {
		return fmt.Errorf("advancing the projection time: %w", err)
	}
	return nil
}

// ApplyProgram copies the state of a coaching program.
func (r *Repository) ApplyProgram(
	ctx context.Context, userID domain.UserID, p *domain.Program, occurredAt time.Time,
) error {
	if p == nil {
		return errors.New("nil program")
	}

	// COALESCE on completion: the new value is used when present, and the old
	// one is KEPT when not. That is what keeps a paused program from jumping
	// back to zero percent.
	const q = `
		INSERT INTO dashboards (
			user_id, program_slug, program_title, program_status,
			program_current_day, program_total_days, program_completion_percentage,
			projected_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (user_id) DO UPDATE SET
			program_slug        = EXCLUDED.program_slug,
			program_title       = EXCLUDED.program_title,
			program_status      = EXCLUDED.program_status,
			program_current_day = EXCLUDED.program_current_day,
			program_total_days  = EXCLUDED.program_total_days,
			program_completion_percentage = COALESCE(
				EXCLUDED.program_completion_percentage,
				dashboards.program_completion_percentage),
			projected_at = GREATEST(dashboards.projected_at, EXCLUDED.projected_at),
			updated_at = now()`

	if _, err := r.db.Exec(ctx, q,
		userID.String(), p.Slug, p.Title, p.Status,
		p.CurrentDay, p.TotalDays, p.Completion, occurredAt); err != nil {
		return fmt.Errorf("projecting the program: %w", err)
	}
	return nil
}

// Forget removes a user's projection.
func (r *Repository) Forget(ctx context.Context, userID domain.UserID) error {
	if _, err := r.db.Exec(ctx,
		`DELETE FROM dashboard_assessments WHERE user_id = $1`, userID.String()); err != nil {
		return fmt.Errorf("forgetting the assessment history: %w", err)
	}
	if _, err := r.db.Exec(ctx,
		`DELETE FROM dashboards WHERE user_id = $1`, userID.String()); err != nil {
		return fmt.Errorf("forgetting the dashboard: %w", err)
	}
	return nil
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
