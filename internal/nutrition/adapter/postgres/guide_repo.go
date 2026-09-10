package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// GuideRepository implements domain.GuideRepository.
type GuideRepository struct {
	db pg.Querier
}

func NewGuideRepository(db pg.Querier) *GuideRepository {
	return &GuideRepository{db: db}
}

var _ domain.GuideRepository = (*GuideRepository)(nil)

const guideColumns = `
	id, user_id, guide_date, meal_time, status,
	generation_context, guide_data, chosen, created_at, updated_at`

func (r *GuideRepository) Create(ctx context.Context, g *domain.Guide) error {
	const q = `
		INSERT INTO daily_meal_guides
			(id, user_id, guide_date, meal_time, status,
			 generation_context, guide_data, chosen, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	if _, err := r.db.Exec(ctx, q,
		g.ID.String(), g.UserID.String(), g.Date, string(g.MealTime), string(g.Status),
		[]byte(g.Context), nullIfNoJSON(g.Data), g.Chosen,
		g.CreatedAt, g.UpdatedAt,
	); err != nil {
		return fmt.Errorf("creating the meal guide: %w", err)
	}
	return nil
}

func (r *GuideRepository) FindByID(ctx context.Context, id domain.ID) (*domain.Guide, error) {
	const q = `SELECT ` + guideColumns + ` FROM daily_meal_guides WHERE id = $1`

	g, err := scanGuide(r.db.QueryRow(ctx, q, id.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrGuideNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying the meal guide: %w", err)
	}
	return g, nil
}

// ListForUser returns the guide history, newest first.
func (r *GuideRepository) ListForUser(
	ctx context.Context, userID domain.UserID, page domain.Page,
) ([]*domain.Guide, int, error) {
	page = page.Normalise()

	// The count is computed separately: a window function would recompute it
	// for every row, and two queries are cheaper and easier to read at the
	// same time.
	var total int
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM daily_meal_guides WHERE user_id = $1`, userID.String(),
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting meal guides: %w", err)
	}

	// created_at joins the ORDER BY as a tie-breaker, and id after it.
	//
	// Several guides on one day share the same guide_date; without a
	// tie-breaker column, PostgreSQL orders tied rows however it likes, and
	// the second page can repeat a row that already appeared on the first page
	// while skipping another entirely.
	const q = `
		SELECT ` + guideColumns + `
		FROM daily_meal_guides
		WHERE user_id = $1
		ORDER BY guide_date DESC, created_at DESC, id DESC
		LIMIT $2 OFFSET $3`

	rows, err := r.db.Query(ctx, q, userID.String(), page.Size, page.Offset())
	if err != nil {
		return nil, 0, fmt.Errorf("querying meal guides: %w", err)
	}
	defer rows.Close()

	out, err := collectGuides(rows, page.Size)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ListChosen returns the guides the user actually chose.
func (r *GuideRepository) ListChosen(
	ctx context.Context, userID domain.UserID, limit int,
) ([]*domain.Guide, error) {
	if limit < 1 {
		limit = 5
	}

	// chosen AND ready: a guide marked before its content arrived has nothing
	// to learn from.
	const q = `
		SELECT ` + guideColumns + `
		FROM daily_meal_guides
		WHERE user_id = $1 AND chosen AND status = 'ready'
		ORDER BY created_at DESC, id DESC
		LIMIT $2`

	rows, err := r.db.Query(ctx, q, userID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("querying chosen meal guides: %w", err)
	}
	defer rows.Close()

	return collectGuides(rows, limit)
}

func (r *GuideRepository) Update(ctx context.Context, g *domain.Guide) error {
	const q = `
		UPDATE daily_meal_guides SET
			status = $2, guide_data = $3, chosen = $4, updated_at = $5
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, q,
		g.ID.String(), string(g.Status), nullIfNoJSON(g.Data), g.Chosen, g.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating the meal guide: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrGuideNotFound
	}
	return nil
}

// nullIfNoJSON stores empty content as NULL.
//
// The column is JSONB with a CHECK binding status and content: a guide that is
// not yet ready MUST have a NULL guide_data. Writing an empty []byte to JSONB
// is a JSON syntax error, and writing JSON 'null' would PASS an IS NULL check
// - so a pending guide would look as if it had content.
func nullIfNoJSON(v json.RawMessage) []byte {
	if len(v) == 0 {
		return nil
	}
	return []byte(v)
}

func collectGuides(rows pgx.Rows, capacity int) ([]*domain.Guide, error) {
	// An empty slice, not nil: nil becomes `null` in JSON, and a client
	// iterating the history fails instead of showing an empty history.
	out := make([]*domain.Guide, 0, capacity)

	for rows.Next() {
		g, err := scanGuide(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating meal guides: %w", err)
	}
	return out, nil
}

func scanGuide(row pgx.Row) (*domain.Guide, error) {
	var (
		g                domain.Guide
		rawID, rawUser   string
		mealTime, status string
		context, data    []byte
	)

	if err := row.Scan(
		&rawID, &rawUser, &g.Date, &mealTime, &status,
		&context, &data, &g.Chosen, &g.CreatedAt, &g.UpdatedAt,
	); err != nil {
		return nil, err
	}

	id, err := domain.ParseID(rawID)
	if err != nil {
		return nil, fmt.Errorf("reading the guide id: %w", err)
	}
	userID, err := domain.ParseUserID(rawUser)
	if err != nil {
		return nil, fmt.Errorf("reading the guide owner: %w", err)
	}

	g.ID = id
	g.UserID = userID
	g.MealTime = domain.MealTime(mealTime)
	g.Status = domain.GuideStatus(status)
	g.Context = json.RawMessage(context)

	// Empty content stays empty, not a zero-length RawMessage that is not nil:
	// len() already treats the two alike, but a reader comparing against nil
	// does not.
	if len(data) > 0 {
		g.Data = json.RawMessage(data)
	}

	// The daily input lives inside generation_context, and is read back from
	// there. It is NOT copied into a separate column: two copies of one answer
	// would drift, and which one is right would never be answerable.
	g.Input = inputFromContext(context)

	return &g, nil
}

// generationContext is the JSON shape of the generation context.
//
// It deliberately does not use map[string]any: the fields are known, and a
// map makes every reader guess the types at the point of use.
type generationContext struct {
	Input struct {
		PlanType          string `json:"plan_type"`
		TimeAvailability  string `json:"time_availability"`
		EnergyLevel       string `json:"energy_level"`
		CuisinePreference string `json:"cuisine_preference"`
		CravingType       string `json:"craving_type"`
		SocialContext     string `json:"social_context"`
	} `json:"input"`
}

// inputFromContext reads the daily input back from its context.
//
// An unreadable context yields an empty input, not an error: a history that
// cannot be displayed at all is too high a price for one old row with a
// different shape.
func inputFromContext(raw []byte) domain.GuideInput {
	var parsed generationContext
	if len(raw) == 0 || json.Unmarshal(raw, &parsed) != nil {
		return domain.GuideInput{}
	}

	return domain.GuideInput{
		PlanType:          domain.PlanType(parsed.Input.PlanType),
		TimeAvailability:  domain.TimeAvailability(parsed.Input.TimeAvailability),
		EnergyLevel:       domain.EnergyLevel(parsed.Input.EnergyLevel),
		CuisinePreference: parsed.Input.CuisinePreference,
		CravingType:       domain.CravingType(parsed.Input.CravingType),
		SocialContext:     domain.SocialContext(parsed.Input.SocialContext),
	}
}
