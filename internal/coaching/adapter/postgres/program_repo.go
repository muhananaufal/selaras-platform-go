// Package postgres menyimpan program coaching di Postgres.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Nama indeks unik, dipakai untuk menerjemahkan pelanggarannya menjadi galat
// domain yang bisa dibaca.
//
// String ini HARUS sama dengan yang ada di migrasi. Kalau tidak, pelanggaran
// yang seharusnya menjadi ErrActiveProgramExists akan lolos sebagai galat
// internal - dan pemanggil menjawab 500 untuk keadaan yang sepenuhnya normal.
const (
	indexOneActivePerUser = "coaching_programs_one_active_per_user"
	indexOnePerAssessment = "coaching_programs_one_per_assessment"
)

// programColumns SELALU dikualifikasi dengan alias tabel "p".
//
// Tanpa kualifikasi, daftar ini tidak bisa dipakai di kueri yang mengandung
// JOIN: "id" ada di coaching_programs, coaching_weeks, dan coaching_tasks,
// dan PostgreSQL menolaknya sebagai ambigu. Ini benar-benar terjadi pada
// ProgramOfTask, dan test integrasinya yang menemukannya.
//
// Setiap kueri yang memakainya WAJIB memberi alias "p" pada coaching_programs.
const programColumns = `p.id, p.user_id, p.slug, p.risk_assessment_id, p.assessment_snapshot,
	p.title, p.description, p.status, p.difficulty, p.start_date, p.end_date,
	p.curriculum_status, coalesce(p.curriculum_error, ''),
	p.graduation_report, p.graduation_status, coalesce(p.graduation_error, ''),
	p.created_at, p.updated_at`

// ProgramRepository memenuhi domain.ProgramRepository.
type ProgramRepository struct {
	db pg.Querier
}

func NewProgramRepository(db pg.Querier) *ProgramRepository {
	return &ProgramRepository{db: db}
}

var _ domain.ProgramRepository = (*ProgramRepository)(nil)

// Create menyimpan program baru.
func (r *ProgramRepository) Create(ctx context.Context, p *domain.Program) error {
	if err := p.Validate(); err != nil {
		return err
	}

	snapshot, err := encodeJSON(p.AssessmentSnapshot)
	if err != nil {
		return fmt.Errorf("encoding the assessment snapshot: %w", err)
	}

	const q = `
		INSERT INTO coaching_programs
			(id, user_id, slug, risk_assessment_id, assessment_snapshot,
			 title, description, status, difficulty, start_date, end_date,
			 curriculum_status, graduation_status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`

	_, err = r.db.Exec(ctx, q,
		p.ID.String(), p.UserID.String(), p.Slug,
		nullableString(p.RiskAssessmentID), snapshot,
		p.Title, p.Description, string(p.Status), string(p.Difficulty),
		p.StartDate, p.EndDate,
		string(p.CurriculumStatus), string(p.GraduationStatus),
		p.CreatedAt, p.UpdatedAt)

	switch {
	case err == nil:
		return nil

	// Kedua pelanggaran ini adalah aturan domain yang ditegakkan basis data
	// (D2 dan D3), bukan kerusakan. Menerjemahkannya di sini membuat pemanggil
	// bisa menjawab 409 alih-alih 500.
	case pg.IsUniqueViolation(err, indexOneActivePerUser):
		return domain.ErrActiveProgramExists
	case pg.IsUniqueViolation(err, indexOnePerAssessment):
		return domain.ErrAssessmentUsed

	default:
		return fmt.Errorf("creating the program: %w", err)
	}
}

// FindBySlug mencari lewat id publiknya.
func (r *ProgramRepository) FindBySlug(ctx context.Context, slug string) (*domain.Program, error) {
	const q = `SELECT ` + programColumns + ` FROM coaching_programs p WHERE p.slug = $1`

	p, err := scanProgram(r.db.QueryRow(ctx, q, domain.NormaliseSlug(slug)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrProgramNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying the program: %w", err)
	}
	return p, nil
}

// FindActiveForUser mencari program yang sedang berjalan.
func (r *ProgramRepository) FindActiveForUser(
	ctx context.Context, userID domain.UserID,
) (*domain.Program, bool, error) {
	const q = `
		SELECT ` + programColumns + `
		FROM coaching_programs p
		WHERE p.user_id = $1 AND p.status = 'active'`

	p, err := scanProgram(r.db.QueryRow(ctx, q, userID.String()))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Bukan galat. Pengguna baru belum punya program, dan memaksa
		// pemanggil membedakan "tidak ada" dari "gagal" lewat pemeriksaan
		// galat akan membuat keduanya mudah tertukar.
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("querying the active program: %w", err)
	}
	return p, true, nil
}

// Update menyimpan perubahan program.
func (r *ProgramRepository) Update(ctx context.Context, p *domain.Program) error {
	if err := p.Validate(); err != nil {
		return err
	}

	snapshot, err := encodeJSON(p.AssessmentSnapshot)
	if err != nil {
		return fmt.Errorf("encoding the assessment snapshot: %w", err)
	}
	report, err := encodeJSON(p.GraduationReport)
	if err != nil {
		return fmt.Errorf("encoding the graduation report: %w", err)
	}

	const q = `
		UPDATE coaching_programs SET
			title = $2, description = $3, status = $4,
			start_date = $5, end_date = $6,
			assessment_snapshot = $7,
			curriculum_status = $8, curriculum_error = $9,
			graduation_report = $10, graduation_status = $11, graduation_error = $12,
			updated_at = $13
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, q,
		p.ID.String(), p.Title, p.Description, string(p.Status),
		p.StartDate, p.EndDate, snapshot,
		string(p.CurriculumStatus), nullableString(p.CurriculumError),
		report, string(p.GraduationStatus), nullableString(p.GraduationError),
		p.UpdatedAt)

	switch {
	case pg.IsUniqueViolation(err, indexOneActivePerUser):
		// Melanjutkan program yang dijeda saat sudah ada program aktif lain.
		return domain.ErrActiveProgramExists
	case err != nil:
		return fmt.Errorf("updating the program: %w", err)
	case tag.RowsAffected() == 0:
		return domain.ErrProgramNotFound
	}
	return nil
}

// Delete menghapus program beserta seluruh isinya.
func (r *ProgramRepository) Delete(ctx context.Context, id domain.ID) error {
	// Pekan, tugas, thread, dan pesan ikut terhapus lewat ON DELETE CASCADE.
	// Satu pernyataan, satu transaksi implisit - tidak ada keadaan di tengah
	// yang bisa ditinggalkan proses yang mati.
	const q = `DELETE FROM coaching_programs WHERE id = $1`

	tag, err := r.db.Exec(ctx, q, id.String())
	if err != nil {
		return fmt.Errorf("deleting the program: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrProgramNotFound
	}
	return nil
}

// scanProgram membaca satu baris menjadi program.
func scanProgram(row pgx.Row) (*domain.Program, error) {
	var (
		id, userID, slug             string
		assessmentID                 *string
		snapshot, report             []byte
		title, description           string
		status, difficulty           string
		startDate, endDate           time.Time
		curriculumStatus, curriErr   string
		graduationStatus, gradErrMsg string
		createdAt, updatedAt         time.Time
	)

	if err := row.Scan(&id, &userID, &slug, &assessmentID, &snapshot,
		&title, &description, &status, &difficulty, &startDate, &endDate,
		&curriculumStatus, &curriErr,
		&report, &graduationStatus, &gradErrMsg,
		&createdAt, &updatedAt); err != nil {
		return nil, err
	}

	parsedID, err := domain.ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("stored program id is not a uuid: %w", err)
	}
	parsedUser, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, fmt.Errorf("stored owner id is not a uuid: %w", err)
	}

	p := &domain.Program{
		ID:               parsedID,
		UserID:           parsedUser,
		Slug:             slug,
		Title:            title,
		Description:      description,
		Status:           domain.Status(status),
		Difficulty:       domain.Difficulty(difficulty),
		StartDate:        startDate,
		EndDate:          endDate,
		CurriculumStatus: domain.CurriculumStatus(curriculumStatus),
		CurriculumError:  curriErr,
		GraduationStatus: domain.GraduationStatus(graduationStatus),
		GraduationError:  gradErrMsg,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}
	if assessmentID != nil {
		p.RiskAssessmentID = *assessmentID
	}
	if p.AssessmentSnapshot, err = decodeJSON(snapshot); err != nil {
		return nil, fmt.Errorf("stored assessment snapshot is not readable: %w", err)
	}
	if p.GraduationReport, err = decodeJSON(report); err != nil {
		return nil, fmt.Errorf("stored graduation report is not readable: %w", err)
	}
	return p, nil
}

// encodeJSON mengubah map menjadi nilai JSONB, atau NULL bila kosong.
//
// NULL, bukan "{}": keduanya berbeda artinya. Yang pertama berarti "belum ada",
// yang kedua "ada dan kosong" - dan laporan kelulusan yang kosong akan terlihat
// sudah dibuat.
func encodeJSON(v map[string]any) (any, error) {
	if len(v) == 0 {
		return nil, nil
	}
	return json.Marshal(v)
}

// decodeJSON membaca nilai JSONB menjadi map.
func decodeJSON(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// nullableString mengubah string kosong menjadi NULL.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// FindByID mencari lewat id internalnya.
func (r *ProgramRepository) FindByID(ctx context.Context, id domain.ID) (*domain.Program, error) {
	const q = `SELECT ` + programColumns + ` FROM coaching_programs p WHERE p.id = $1`

	p, err := scanProgram(r.db.QueryRow(ctx, q, id.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrProgramNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying the program: %w", err)
	}
	return p, nil
}
