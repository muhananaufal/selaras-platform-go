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

// StateRepository implements domain.StateRepository.
type StateRepository struct {
	db pg.Querier
}

func NewStateRepository(db pg.Querier) *StateRepository {
	return &StateRepository{db: db}
}

var _ domain.StateRepository = (*StateRepository)(nil)

// Get reads the projection position.
//
// A projection that has never run returns an EMPTY state, not an error:
// never having projected anything is a valid state, and the rebuild command
// starts precisely from there.
func (s *StateRepository) Get(ctx context.Context, name string) (domain.ProjectionState, error) {
	const q = `
		SELECT name, last_event_at, events_applied, updated_at
		FROM projection_state
		WHERE name = $1`

	var (
		state       domain.ProjectionState
		lastEventAt *time.Time
	)

	err := s.db.QueryRow(ctx, q, name).Scan(
		&state.Name, &lastEventAt, &state.EventsApplied, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ProjectionState{Name: name}, nil
	}
	if err != nil {
		return domain.ProjectionState{}, fmt.Errorf("reading the projection state: %w", err)
	}

	if lastEventAt != nil {
		state.LastEventAt = *lastEventAt
	}
	return state, nil
}

// Advance records that one event has been projected.
//
// last_event_at only moves FORWARD, never back. Events can arrive out of
// order, and a position moving backwards would make the lag measurement report
// a bigger delay than the real one - and then someone spends an afternoon
// hunting a slowdown that does not exist.
func (s *StateRepository) Advance(ctx context.Context, name string, eventAt time.Time) error {
	const q = `
		INSERT INTO projection_state (name, last_event_at, events_applied, updated_at)
		VALUES ($1, $2, 1, now())
		ON CONFLICT (name) DO UPDATE SET
			last_event_at  = GREATEST(projection_state.last_event_at, EXCLUDED.last_event_at),
			events_applied = projection_state.events_applied + 1,
			updated_at     = now()`

	if _, err := s.db.Exec(ctx, q, name, eventAt); err != nil {
		return fmt.Errorf("advancing the projection state: %w", err)
	}
	return nil
}

// Reset returns the projection to the never-run state.
//
// Used by the rebuild command. It deletes the row rather than writing zeros: an
// existing row with zero events reads as "has run and found nothing", which
// means something different from "has never run".
func (s *StateRepository) Reset(ctx context.Context, name string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM projection_state WHERE name = $1`, name); err != nil {
		return fmt.Errorf("resetting the projection state: %w", err)
	}
	return nil
}
