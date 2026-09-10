// Package postgres stores culinary preferences and menu guides in Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// PreferencesRepository implements domain.PreferencesRepository.
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

// FindByUser reads a user's preferences.
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

// nullIfEmpty stores empty as NULL.
//
// The enum columns have a CHECK that does not include the empty string, so "not chosen
// yet" MUST become NULL - writing it as two single quotes would be refused by the
// database. Allergies follow the same pattern so "no note" has only one shape in storage,
// not two that every reader has to tell apart.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// scanPreferences reads one preferences row.
//
// The coalesce in the column list makes NULL come back as an empty string, so
// no pointer has to be unwrapped here. The opposite direction - empty becoming
// NULL on write - is handled by nullIfEmpty.
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

	// Stored values are CHECKED on read, not only on write.
	//
	// The database has a CHECK, but rows can come from a data migration script
	// or from a manual fix. Reading a foreign value silently means spreading
	// it into prompts and to clients; refusing it here makes the corrupt row
	// visible rather than contagious.
	if p.BudgetLevel, err = domain.ParseBudgetLevel(budget); err != nil {
		return nil, fmt.Errorf("reading the stored budget level: %w", err)
	}
	if p.CookingStyle, err = domain.ParseCookingStyle(cooking); err != nil {
		return nil, fmt.Errorf("reading the stored cooking style: %w", err)
	}

	p.ID = id
	p.UserID = userID
	p.Allergies = allergies

	// An empty slice, not nil: its reader hands it straight to JSON, and nil
	// becomes `null` instead of `[]`.
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
