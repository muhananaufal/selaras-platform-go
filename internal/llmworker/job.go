// Package llmworker performs the LLM requests that arrive through Kafka.
//
// It stands between three things that can each fail on their own: the broker,
// the model provider, and the database. What keeps the three consistent is two
// mechanisms that already exist - idempotency on the way in, the outbox on the
// way out - and this package is what wires them together.
package llmworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Job is one stored job.
type Job struct {
	ID            uuid.UUID
	CreatedAt     time.Time
	Key           string
	Kind          string
	AggregateType string
	AggregateID   string
	Status        string
	PromptVersion string
	Model         string
	Result        []byte
	Attempts      int
}

// The possible statuses. The values are enforced by a CHECK constraint in
// the database, and named here so no literal drifts from it.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusDead      = "dead"
)

// Job kinds.
const (
	KindPersonalization = "personalization"
)

// Repository stores jobs.
//
// It takes a Querier in every method rather than holding a connection pool,
// because every write here has to be able to join the same transaction as its
// idempotency claim and its outbox row.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

// Create stores a new job in the pending state.
func (r *Repository) Create(ctx context.Context, q pg.Querier, job *Job) error {
	if job == nil {
		return errors.New("nil job")
	}
	if job.Key == "" {
		return errors.New("a job without an idempotency key cannot be deduplicated")
	}
	if job.Kind == "" || job.AggregateType == "" || job.AggregateID == "" {
		return errors.New("a job needs a kind and an aggregate to answer to")
	}

	if job.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generating a job id: %w", err)
		}
		job.ID = id
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}

	const query = `
		INSERT INTO llm_jobs (id, created_at, idempotency_key, kind, aggregate_type, aggregate_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	if _, err := q.Exec(ctx, query,
		job.ID, job.CreatedAt, job.Key, job.Kind,
		job.AggregateType, job.AggregateID, StatusPending,
	); err != nil {
		return fmt.Errorf("creating the job: %w", err)
	}
	job.Status = StatusPending
	return nil
}

// Complete stores the result.
//
// It refuses an empty result. The CHECK constraint in the database refuses it
// too, and both are deliberate: the one here gives a readable message, the one
// there guarantees no other path can get past it.
func (r *Repository) Complete(
	ctx context.Context, q pg.Querier,
	id uuid.UUID, createdAt time.Time,
	result []byte, promptVersion, model string,
) error {
	if len(result) == 0 {
		return errors.New("a completed job must carry its result")
	}
	if promptVersion == "" || model == "" {
		return errors.New("a completed job must record which prompt and model produced it")
	}

	const query = `
		UPDATE llm_jobs
		SET status = $3, result = $4, prompt_version = $5, model = $6, finished_at = now()
		WHERE id = $1 AND created_at = $2`

	tag, err := q.Exec(ctx, query, id, createdAt, StatusCompleted, result, promptVersion, model)
	if err != nil {
		return fmt.Errorf("completing the job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no job %s exists to complete", id)
	}
	return nil
}

// Fail records a failed attempt.
//
// dead marks a job that will not be tried again. Telling it apart from failed
// matters: the first waits for a human, the second waits for the next attempt,
// and conflating them means one of the two is handled wrongly.
func (r *Repository) Fail(
	ctx context.Context, q pg.Querier,
	id uuid.UUID, createdAt time.Time,
	cause string, dead bool,
) error {
	status := StatusFailed
	if dead {
		status = StatusDead
	}

	const query = `
		UPDATE llm_jobs
		SET status = $3, attempts = attempts + 1, last_error = $4, finished_at = now()
		WHERE id = $1 AND created_at = $2`

	if _, err := q.Exec(ctx, query, id, createdAt, status, truncate(cause, 1000)); err != nil {
		return fmt.Errorf("recording the job failure: %w", err)
	}
	return nil
}

// ByKey looks a job up by its idempotency key.
//
// found is false if there is none yet. It is used to answer a repeated
// request with exactly the same result, instead of merely "already seen".
func (r *Repository) ByKey(ctx context.Context, q pg.Querier, key string) (*Job, bool, error) {
	const query = `
		SELECT id, created_at, idempotency_key, kind, aggregate_type, aggregate_id,
		       status, coalesce(prompt_version, ''), coalesce(model, ''), result, attempts
		FROM llm_jobs
		WHERE idempotency_key = $1
		ORDER BY created_at DESC
		LIMIT 1`

	var job Job
	err := q.QueryRow(ctx, query, key).Scan(
		&job.ID, &job.CreatedAt, &job.Key, &job.Kind, &job.AggregateType,
		&job.AggregateID, &job.Status, &job.PromptVersion, &job.Model,
		&job.Result, &job.Attempts,
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("looking up the job: %w", err)
	}
	return &job, true, nil
}

// truncate keeps an error message at a sensible size.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
