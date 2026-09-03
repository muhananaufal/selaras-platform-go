// Package postgres menyimpan penilaian risiko di Postgres.
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

// Repository memenuhi domain.Repository.
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
	const q = `SELECT ` + assessmentColumns + ` FROM risk_assessments WHERE slug = $1`

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
	// Urutan dan batasnya ada di kueri, bukan di Go. Membaca seluruh riwayat
	// lalu memotongnya di memori memindahkan pekerjaan basis data ke service,
	// dan indeks gabungan di migrasi memang dibuat untuk kueri ini.
	const q = `
		SELECT ` + assessmentColumns + `
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
	// Galat iterasi diperiksa terpisah dari galat Scan. Baris yang habis di
	// tengah karena koneksi putus terlihat seperti hasil yang lengkap kalau
	// ini dilewati - daftar pendek yang tampak benar.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading assessments: %w", err)
	}
	return out, nil
}

// scanner menyatukan pgx.Row dan pgx.Rows, yang keduanya bisa di-Scan.
type scanner interface {
	Scan(dest ...any) error
}

func scanAssessment(s scanner) (*domain.Assessment, error) {
	var (
		id, profileID, slug, model string
		risk                       float64
		inputs, generated, details []byte
		createdAt, updatedAt       time.Time
	)

	if err := s.Scan(&id, &profileID, &slug, &model, &risk,
		&inputs, &generated, &details, &createdAt, &updatedAt); err != nil {
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
		ID:             parsedID,
		UserProfileID:  parsedProfile,
		Slug:           slug,
		ModelUsed:      model,
		RiskPercentage: risk,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
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

// orEmpty menjaga kolom NOT NULL tetap terisi.
//
// Map Go yang nil menjadi "null" di JSON, dan kolomnya menolaknya. Objek
// kosong lebih jujur daripada penilaian yang gagal disimpan karena
// jawabannya kebetulan kosong.
func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// nullableJSON menyimpan NULL untuk yang belum ada, bukan objek kosong.
//
// result_details yang kosong dan yang belum diisi adalah dua hal berbeda:
// yang pertama berarti llm-worker sudah menjawab dan tidak menemukan apa
// pun, yang kedua berarti ia belum menjawab.
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

// SetResultDetails menyimpan laporan personalisasi.
//
// Syarat `result_details IS NULL` ada di WHERE, bukan diperiksa lebih dulu
// dengan SELECT. Pemeriksaan pendahuluan punya celah di antara membaca dan
// menulis, dan dua event yang tiba serempak akan sama-sama membaca "belum ada"
// lalu sama-sama menulis - yang kedua menimpa yang pertama.
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
