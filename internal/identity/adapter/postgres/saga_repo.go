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

// SagaRepository menyimpan saga penghapusan akun.
type SagaRepository struct {
	db pg.Querier
}

func NewSagaRepository(db pg.Querier) *SagaRepository { return &SagaRepository{db: db} }

var _ app.SagaRepository = (*SagaRepository)(nil)

func (r *SagaRepository) Create(ctx context.Context, s *domain.DeletionSaga) error {
	if s == nil {
		return errors.New("nil saga")
	}

	const q = `
		INSERT INTO deletion_sagas (id, user_id, user_profile_id, status, requested_at)
		VALUES ($1, $2, $3, $4, $5)`

	if _, err := r.db.Exec(ctx, q,
		s.ID.String(), s.UserID.String(), nullIfBlank(s.UserProfileID),
		string(s.Status), s.RequestedAt); err != nil {
		return fmt.Errorf("recording the deletion saga: %w", err)
	}
	return nil
}

func (r *SagaRepository) Find(ctx context.Context, id domain.SagaID) (*domain.DeletionSaga, error) {
	const q = `
		SELECT id, user_id, user_profile_id, status, requested_at, finished_at
		FROM deletion_sagas
		WHERE id = $1`

	saga, err := scanSaga(r.db.QueryRow(ctx, q, id.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrSagaNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying the deletion saga: %w", err)
	}

	if saga.Confirmations, err = r.confirmations(ctx, saga.ID); err != nil {
		return nil, err
	}
	return saga, nil
}

// FindOutstandingForUser mencari saga yang sedang berjalan.
func (r *SagaRepository) FindOutstandingForUser(
	ctx context.Context, userID domain.UserID,
) (*domain.DeletionSaga, error) {
	const q = `
		SELECT id, user_id, user_profile_id, status, requested_at, finished_at
		FROM deletion_sagas
		WHERE user_id = $1 AND status = 'requested'`

	saga, err := scanSaga(r.db.QueryRow(ctx, q, userID.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrSagaNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying the outstanding deletion saga: %w", err)
	}

	if saga.Confirmations, err = r.confirmations(ctx, saga.ID); err != nil {
		return nil, err
	}
	return saga, nil
}

// Confirm mencatat jawaban satu unit.
//
// ON CONFLICT DO NOTHING: relay outbox bersifat at-least-once, dan jawaban yang
// sama bisa tiba dua kali. Yang kedua tidak boleh membuat saga mengira ada
// tujuh unit menjawab.
func (r *SagaRepository) Confirm(
	ctx context.Context, id domain.SagaID, c domain.Confirmation,
) error {
	const q = `
		INSERT INTO deletion_confirmations
			(saga_id, service, succeeded, failure_reason, confirmed_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (saga_id, service) DO NOTHING`

	if _, err := r.db.Exec(ctx, q,
		id.String(), c.Service, c.Succeeded,
		nullIfBlank(c.FailureReason), c.ConfirmedAt); err != nil {
		return fmt.Errorf("recording the confirmation: %w", err)
	}
	return nil
}

// Close menutup saga.
//
// Syarat status = 'requested' ada di WHERE, bukan diperiksa lebih dulu lalu
// ditulis: dua konfirmasi terakhir yang tiba bersamaan akan sama-sama membaca
// "masih berjalan", dan yang kedua menutupnya untuk kedua kalinya - menghapus
// akun dua kali, atau menimpa 'failed' dengan 'completed'.
func (r *SagaRepository) Close(
	ctx context.Context, id domain.SagaID, status domain.SagaStatus, at time.Time,
) error {
	const q = `
		UPDATE deletion_sagas
		SET status = $2, finished_at = $3
		WHERE id = $1 AND status = 'requested'`

	if _, err := r.db.Exec(ctx, q, id.String(), string(status), at); err != nil {
		return fmt.Errorf("closing the deletion saga: %w", err)
	}
	return nil
}

// Outstanding menyebutkan saga yang belum selesai, TERLAMA lebih dulu.
//
// Terlama lebih dulu karena yang paling lama menggantung adalah yang paling
// mungkin benar-benar macet; yang baru saja diminta mungkin hanya sedang
// berjalan.
func (r *SagaRepository) Outstanding(ctx context.Context, limit int) ([]*domain.DeletionSaga, error) {
	if limit < 1 {
		limit = 50
	}

	const q = `
		SELECT id, user_id, user_profile_id, status, requested_at, finished_at
		FROM deletion_sagas
		WHERE status = 'requested'
		ORDER BY requested_at ASC
		LIMIT $1`

	rows, err := r.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("querying outstanding sagas: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.DeletionSaga, 0, limit)
	for rows.Next() {
		saga, err := scanSaga(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, saga)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating outstanding sagas: %w", err)
	}

	// Konfirmasinya dibaca SETELAH baris sagalnya selesai dibaca.
	//
	// Membacanya di dalam loop di atas akan memakai koneksi yang sedang
	// memegang rows yang belum ditutup - pgx menolaknya, dan penolakannya
	// muncul sebagai galat yang tidak menyebut sebabnya.
	for _, saga := range out {
		if saga.Confirmations, err = r.confirmations(ctx, saga.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *SagaRepository) confirmations(
	ctx context.Context, id domain.SagaID,
) ([]domain.Confirmation, error) {
	const q = `
		SELECT service, succeeded, coalesce(failure_reason, ''), confirmed_at
		FROM deletion_confirmations
		WHERE saga_id = $1
		ORDER BY confirmed_at, service`

	rows, err := r.db.Query(ctx, q, id.String())
	if err != nil {
		return nil, fmt.Errorf("querying the confirmations: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Confirmation, 0, len(domain.DeletionParticipants))
	for rows.Next() {
		var c domain.Confirmation
		if err := rows.Scan(&c.Service, &c.Succeeded, &c.FailureReason, &c.ConfirmedAt); err != nil {
			return nil, fmt.Errorf("reading a confirmation: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating the confirmations: %w", err)
	}
	return out, nil
}

func scanSaga(row pgx.Row) (*domain.DeletionSaga, error) {
	var (
		saga           domain.DeletionSaga
		rawID, rawUser string
		profileID      *string
		status         string
		finishedAt     *time.Time
	)

	if err := row.Scan(&rawID, &rawUser, &profileID, &status,
		&saga.RequestedAt, &finishedAt); err != nil {
		return nil, err
	}

	id, err := domain.ParseSagaID(rawID)
	if err != nil {
		return nil, fmt.Errorf("reading the saga id: %w", err)
	}
	userID, err := domain.ParseUserID(rawUser)
	if err != nil {
		return nil, fmt.Errorf("reading the saga owner: %w", err)
	}

	saga.ID = id
	saga.UserID = userID
	saga.Status = domain.SagaStatus(status)
	saga.Confirmations = []domain.Confirmation{}

	if profileID != nil {
		saga.UserProfileID = *profileID
	}
	if finishedAt != nil {
		saga.FinishedAt = *finishedAt
	}
	return &saga, nil
}

// nullIfBlank menyimpan string kosong sebagai NULL.
//
// user_profile_id kosong berarti profilnya tidak bisa ditemukan saat saga
// dimulai - keadaan yang sah (B7). Menyimpannya sebagai string kosong akan membuat
// pembacanya harus membedakan dua bentuk untuk satu arti.
func nullIfBlank(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
