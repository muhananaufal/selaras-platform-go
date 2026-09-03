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
func (r *Repository) Find(ctx context.Context, userID domain.UserID) (*domain.Dashboard, error) {
	const q = `
		SELECT
			latest_assessment_slug, latest_assessment_at, latest_risk_percentage,
			latest_risk_category, latest_model_used,
			previous_risk_percentage, total_assessments,
			program_slug, program_title, program_status,
			program_current_day, program_total_days, program_completion_percentage,
			projected_at
		FROM dashboards
		WHERE user_id = $1`

	var (
		dash                      domain.Dashboard
		slug, category, model     *string
		assessedAt                *time.Time
		latestRisk, previousRisk  *float64
		programSlug, programTitle *string
		programStatus             *string
		currentDay, totalDays     *int
		completion                *float64
	)

	err := r.db.QueryRow(ctx, q, userID.String()).Scan(
		&slug, &assessedAt, &latestRisk, &category, &model,
		&previousRisk, &dash.Total,
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
	dash.Previous = previousRisk

	if slug != nil && assessedAt != nil && latestRisk != nil {
		dash.Latest = &domain.Assessment{
			Slug:           *slug,
			AssessedAt:     *assessedAt,
			RiskPercentage: *latestRisk,
			RiskCategory:   deref(category),
			ModelUsed:      deref(model),
		}
	}

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

	// Baris yang sudah ada berarti event ini SUDAH pernah diterapkan. Berhenti
	// di sini, karena melanjutkan akan menaikkan total_assessments untuk kedua
	// kalinya dan menggeser previous_risk_percentage dengan angka yang sama.
	//
	// Inilah gerbang idempotensinya, dan ia ditegakkan basis data lewat kunci
	// primer (user_id, slug) - bukan dengan SELECT lalu INSERT, yang di antara
	// keduanya ada celah tempat dua proses membaca "belum ada".
	if tag.RowsAffected() == 0 {
		return nil
	}

	// Lalu ringkasannya. Yang menarik ada di ELSE-nya: penilaian yang datang
	// TERLAMBAT - occurred_at-nya lebih tua daripada yang sudah tersimpan -
	// tetap masuk riwayat dan tetap menaikkan jumlah, tetapi TIDAK menggeser
	// "terbaru". Event bisa tiba tidak berurutan, dan proyeksi yang menerima
	// yang terakhir tiba sebagai yang terbaru akan menampilkan angka lama.
	const upsertSummary = `
		INSERT INTO dashboards (
			user_id, latest_assessment_slug, latest_assessment_at,
			latest_risk_percentage, latest_risk_category, latest_model_used,
			total_assessments, projected_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, 1, $7, now())
		ON CONFLICT (user_id) DO UPDATE SET
			previous_risk_percentage = CASE
				WHEN dashboards.latest_assessment_at IS NULL
				  OR EXCLUDED.latest_assessment_at > dashboards.latest_assessment_at
				THEN dashboards.latest_risk_percentage
				ELSE dashboards.previous_risk_percentage
			END,
			latest_assessment_slug = CASE
				WHEN dashboards.latest_assessment_at IS NULL
				  OR EXCLUDED.latest_assessment_at > dashboards.latest_assessment_at
				THEN EXCLUDED.latest_assessment_slug
				ELSE dashboards.latest_assessment_slug
			END,
			latest_risk_percentage = CASE
				WHEN dashboards.latest_assessment_at IS NULL
				  OR EXCLUDED.latest_assessment_at > dashboards.latest_assessment_at
				THEN EXCLUDED.latest_risk_percentage
				ELSE dashboards.latest_risk_percentage
			END,
			latest_risk_category = CASE
				WHEN dashboards.latest_assessment_at IS NULL
				  OR EXCLUDED.latest_assessment_at > dashboards.latest_assessment_at
				THEN EXCLUDED.latest_risk_category
				ELSE dashboards.latest_risk_category
			END,
			latest_model_used = CASE
				WHEN dashboards.latest_assessment_at IS NULL
				  OR EXCLUDED.latest_assessment_at > dashboards.latest_assessment_at
				THEN EXCLUDED.latest_model_used
				ELSE dashboards.latest_model_used
			END,
			latest_assessment_at = GREATEST(
				dashboards.latest_assessment_at, EXCLUDED.latest_assessment_at),
			total_assessments = dashboards.total_assessments + 1,
			projected_at = GREATEST(dashboards.projected_at, EXCLUDED.projected_at),
			updated_at = now()`

	if _, err := r.db.Exec(ctx, upsertSummary,
		userID.String(), a.Slug, a.AssessedAt, a.RiskPercentage,
		a.RiskCategory, a.ModelUsed, occurredAt); err != nil {
		return fmt.Errorf("projecting the assessment summary: %w", err)
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
