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

// StateRepository memenuhi domain.StateRepository.
type StateRepository struct {
	db pg.Querier
}

func NewStateRepository(db pg.Querier) *StateRepository {
	return &StateRepository{db: db}
}

var _ domain.StateRepository = (*StateRepository)(nil)

// Get membaca posisi proyeksi.
//
// Proyeksi yang belum pernah berjalan mengembalikan keadaan KOSONG, bukan
// galat: belum pernah memproyeksikan apa pun adalah keadaan yang sah, dan
// perintah rebuild memulai justru dari sana.
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

// Advance mencatat bahwa satu event sudah diproyeksikan.
//
// last_event_at hanya MAJU, tidak pernah mundur. Event bisa tiba tidak
// berurutan, dan posisi yang mundur akan membuat pengukuran lag melaporkan
// jeda yang lebih besar daripada yang sebenarnya - lalu seseorang menghabiskan
// sore mencari perlambatan yang tidak ada.
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

// Reset mengembalikan proyeksi ke keadaan belum pernah berjalan.
//
// Dipakai perintah rebuild. Ia menghapus barisnya, bukan menulis nol: baris
// yang ada dengan nol event terbaca sebagai "sudah berjalan dan tidak menemukan
// apa-apa", yang berbeda artinya dari "belum pernah berjalan".
func (s *StateRepository) Reset(ctx context.Context, name string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM projection_state WHERE name = $1`, name); err != nil {
		return fmt.Errorf("resetting the projection state: %w", err)
	}
	return nil
}
