// Package postgres menyimpan preferensi kuliner dan panduan menu di Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// PreferencesRepository memenuhi domain.PreferencesRepository.
type PreferencesRepository struct {
	db pg.Querier
}

func NewPreferencesRepository(db pg.Querier) *PreferencesRepository {
	return &PreferencesRepository{db: db}
}

var _ domain.PreferencesRepository = (*PreferencesRepository)(nil)

const preferenceColumns = `
	id, user_id, coalesce(allergies, ''), coalesce(budget_level, ''),
	coalesce(cooking_style, ''), taste_profiles, kitchen_equipment,
	created_at, updated_at`

// FindByUser membaca preferensi seorang pengguna.
func (r *PreferencesRepository) FindByUser(
	ctx context.Context, userID domain.UserID,
) (*domain.Preferences, error) {
	const q = `SELECT ` + preferenceColumns + ` FROM culinary_preferences WHERE user_id = $1`

	p, err := scanPreferences(r.db.QueryRow(ctx, q, userID.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrPreferencesNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying culinary preferences: %w", err)
	}
	return p, nil
}

func (r *PreferencesRepository) Create(ctx context.Context, p *domain.Preferences) error {
	const q = `
		INSERT INTO culinary_preferences
			(id, user_id, allergies, budget_level, cooking_style,
			 taste_profiles, kitchen_equipment, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	if _, err := r.db.Exec(ctx, q,
		p.ID.String(), p.UserID.String(),
		nullIfEmpty(p.Allergies),
		nullIfEmpty(string(p.BudgetLevel)),
		nullIfEmpty(string(p.CookingStyle)),
		p.TasteProfiles, p.KitchenEquipment,
		p.CreatedAt, p.UpdatedAt,
	); err != nil {
		return fmt.Errorf("creating culinary preferences: %w", err)
	}
	return nil
}

func (r *PreferencesRepository) Update(ctx context.Context, p *domain.Preferences) error {
	const q = `
		UPDATE culinary_preferences SET
			allergies = $2, budget_level = $3, cooking_style = $4,
			taste_profiles = $5, kitchen_equipment = $6, updated_at = $7
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, q,
		p.ID.String(),
		nullIfEmpty(p.Allergies),
		nullIfEmpty(string(p.BudgetLevel)),
		nullIfEmpty(string(p.CookingStyle)),
		p.TasteProfiles, p.KitchenEquipment,
		p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("updating culinary preferences: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPreferencesNotFound
	}
	return nil
}

// nullIfEmpty menyimpan kosong sebagai NULL.
//
// Kolom enum-nya punya CHECK yang tidak memuat string kosong, jadi "belum
// dipilih" HARUS menjadi NULL - menuliskannya sebagai dua petik tunggal akan ditolak basis
// data. Alergi ikut pola yang sama supaya "tidak ada catatan" hanya punya satu
// bentuk di penyimpanan, bukan dua yang harus dibedakan setiap pembaca.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// scanPreferences membaca satu baris preferensi.
//
// coalesce di daftar kolom membuat NULL kembali sebagai string kosong, sehingga
// tidak ada pointer yang perlu dibongkar di sini. Arah sebaliknya - kosong
// menjadi NULL saat menulis - ditangani nullIfEmpty.
func scanPreferences(row pgx.Row) (*domain.Preferences, error) {
	var (
		p                          domain.Preferences
		rawID, rawUser             string
		budget, cooking, allergies string
		tastes, equipment          []string
	)

	if err := row.Scan(
		&rawID, &rawUser, &allergies, &budget, &cooking,
		&tastes, &equipment, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}

	id, err := domain.ParseID(rawID)
	if err != nil {
		return nil, fmt.Errorf("reading the preferences id: %w", err)
	}
	userID, err := domain.ParseUserID(rawUser)
	if err != nil {
		return nil, fmt.Errorf("reading the preferences owner: %w", err)
	}

	// Nilai yang tersimpan DIPERIKSA saat dibaca, tidak hanya saat ditulis.
	//
	// Basis data punya CHECK, tetapi baris bisa berasal dari skrip pindah data
	// atau dari perbaikan manual. Membaca nilai asing diam-diam berarti
	// menyebarkannya ke prompt dan ke klien; menolaknya di sini membuat baris
	// yang rusak terlihat, bukan menular.
	if p.BudgetLevel, err = domain.ParseBudgetLevel(budget); err != nil {
		return nil, fmt.Errorf("reading the stored budget level: %w", err)
	}
	if p.CookingStyle, err = domain.ParseCookingStyle(cooking); err != nil {
		return nil, fmt.Errorf("reading the stored cooking style: %w", err)
	}

	p.ID = id
	p.UserID = userID
	p.Allergies = allergies

	// Slice kosong, bukan nil: pembacanya menyerahkannya langsung ke JSON, dan
	// nil menjadi `null` alih-alih `[]`.
	p.TasteProfiles = orEmpty(tastes)
	p.KitchenEquipment = orEmpty(equipment)

	return &p, nil
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
