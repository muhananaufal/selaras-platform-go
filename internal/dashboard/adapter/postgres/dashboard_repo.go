// Package postgres menyimpan read-model dasbor di Postgres.
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

// Repository memenuhi domain.Repository.
type Repository struct {
	db pg.Querier
}

func NewRepository(db pg.Querier) *Repository { return &Repository{db: db} }

var _ domain.Repository = (*Repository)(nil)

// Find membaca satu dasbor beserta riwayatnya.
//
// Ringkasannya - penilaian terbaru, penilaian sebelumnya, dan jumlahnya -
// DITURUNKAN dari riwayat, bukan dibaca dari kolom yang diperbarui tiap event.
// Versi pertama menyimpannya sebagai kolom, dan itu meninggalkan "penilaian
// sebelumnya" kosong selamanya ketika dua event tiba terbalik. Yang diturunkan
// saat dibaca benar untuk urutan kedatangan apa pun.
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

	err := r.db.QueryRow(ctx, q, userID.String()).Scan(
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

	// Terbaru dan sebelumnya adalah dua baris teratas riwayat, yang sudah
	// terurut menurut WAKTU PENILAIAN - bukan menurut urutan kedatangannya.
	if len(history) > 0 {
		dash.Latest = history[0]
	}
	if len(history) > 1 {
		previous := history[1].RiskPercentage
		dash.Previous = &previous
	}

	return &dash, nil
}

// history membaca riwayat penilaian, terbaru lebih dulu.
func (r *Repository) history(ctx context.Context, userID domain.UserID) ([]*domain.Assessment, error) {
	const q = `
		SELECT slug, assessed_at, risk_percentage, risk_category, model_used
		FROM dashboard_assessments
		WHERE user_id = $1
		ORDER BY assessed_at DESC, slug DESC`

	rows, err := r.db.Query(ctx, q, userID.String())
	if err != nil {
		return nil, fmt.Errorf("querying the assessment history: %w", err)
	}
	defer rows.Close()

	// Slice kosong, bukan nil: nil menjadi `null` di JSON.
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

// ApplyAssessment memasukkan satu penilaian ke dalam proyeksi.
//
// Dua penulisan, keduanya IDEMPOTEN, dan keduanya harus jadi atau batal
// bersama - pemanggilnya menjalankan keduanya di dalam satu transaksi.
func (r *Repository) ApplyAssessment(
	ctx context.Context, userID domain.UserID, a *domain.Assessment, occurredAt time.Time,
) error {
	if a == nil {
		return errors.New("nil assessment")
	}

	// Riwayat lebih dulu. ON CONFLICT DO NOTHING: pengiriman kedua dari relay
	// at-least-once tidak menambah baris apa pun.
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

	// Baris yang sudah ada berarti event ini SUDAH pernah diterapkan.
	//
	// Waktu proyeksinya tetap dimajukan di bawah - pengiriman ulang tetap
	// peristiwa yang terjadi - tetapi tidak ada satu angka pun yang bisa
	// bergeser, karena tidak ada angka yang disimpan. Gerbangnya ditegakkan
	// basis data lewat kunci primer (user_id, slug), bukan dengan SELECT lalu
	// INSERT yang di antara keduanya ada celah tempat dua proses membaca
	// "belum ada".
	if tag.RowsAffected() == 0 {
		return nil
	}

	// Lalu barisnya disentuh supaya waktu proyeksinya maju - dan itu SEMUA.
	//
	// Tidak ada ringkasan yang perlu diperbarui: penilaian terbaru, penilaian
	// sebelumnya, dan jumlahnya diturunkan dari riwayat saat DIBACA. Versi
	// pertama menyimpan ketiganya sebagai kolom dan memperbaruinya lewat
	// serangkaian CASE yang membandingkan waktu; itu meninggalkan "penilaian
	// sebelumnya" kosong selamanya ketika dua event tiba terbalik - keadaan
	// biasa, karena Kafka menjamin urutan per kunci partisi dan penilaian
	// dikunci pada id penilaiannya, bukan pada penggunanya.
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

// ApplyProgram menyalin keadaan program coaching.
func (r *Repository) ApplyProgram(
	ctx context.Context, userID domain.UserID, p *domain.Program, occurredAt time.Time,
) error {
	if p == nil {
		return errors.New("nil program")
	}

	// COALESCE pada completion: nilai baru dipakai bila ada, dan yang lama
	// DIPERTAHANKAN bila tidak. Itulah yang membuat program yang dijeda tidak
	// melompat kembali ke nol persen.
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

// Forget menghapus proyeksi seorang pengguna.
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
