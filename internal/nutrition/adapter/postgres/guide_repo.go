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

// GuideRepository memenuhi domain.GuideRepository.
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

// ListForUser mengembalikan riwayat panduan, terbaru lebih dulu.
func (r *GuideRepository) ListForUser(
	ctx context.Context, userID domain.UserID, page domain.Page,
) ([]*domain.Guide, int, error) {
	page = page.Normalise()

	// Jumlah dihitung terpisah: window function akan menghitung ulang untuk
	// setiap baris, dan dua kueri lebih murah sekaligus lebih mudah dibaca.
	var total int
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM daily_meal_guides WHERE user_id = $1`, userID.String(),
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting meal guides: %w", err)
	}

	// created_at ikut ke ORDER BY sebagai pemecah seri, dan id sesudahnya.
	//
	// Beberapa panduan dalam satu hari punya guide_date yang sama; tanpa kolom
	// pemecah, urutan baris seri ditentukan PostgreSQL sesukanya, dan halaman
	// kedua bisa mengulang baris yang sudah muncul di halaman pertama sambil
	// melewatkan yang lain sama sekali.
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

// ListChosen mengembalikan panduan yang benar-benar dipilih pengguna.
func (r *GuideRepository) ListChosen(
	ctx context.Context, userID domain.UserID, limit int,
) ([]*domain.Guide, error) {
	if limit < 1 {
		limit = 5
	}

	// chosen DAN ready: panduan yang ditandai sebelum isinya tiba tidak punya
	// apa pun untuk dipelajari.
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

// nullIfNoJSON menyimpan isi kosong sebagai NULL.
//
// Kolomnya JSONB dengan CHECK yang mengikat status dan isi: panduan yang belum
// ready HARUS ber-guide_data NULL. Menulis []byte kosong ke JSONB adalah galat
// sintaks JSON, dan menulis 'null' JSON akan LOLOS dari IS NULL - sehingga
// panduan pending akan terlihat sudah berisi.
func nullIfNoJSON(v json.RawMessage) []byte {
	if len(v) == 0 {
		return nil
	}
	return []byte(v)
}

func collectGuides(rows pgx.Rows, capacity int) ([]*domain.Guide, error) {
	// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
	// mengiterasi riwayat akan gagal alih-alih menampilkan riwayat kosong.
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

	// Isi kosong tetap kosong, bukan RawMessage sepanjang nol yang bukan nil:
	// len() sudah menyamakan keduanya, tetapi pembaca yang membandingkan
	// dengan nil tidak.
	if len(data) > 0 {
		g.Data = json.RawMessage(data)
	}

	// Masukan hariannya ada di dalam generation_context, dan dibaca kembali
	// dari sana. Ia TIDAK disalin ke kolom terpisah: dua salinan dari satu
	// jawaban akan menyimpang, dan yang mana yang benar tidak akan terjawab.
	g.Input = inputFromContext(context)

	return &g, nil
}

// generationContext adalah bentuk JSON konteks pembuatan.
//
// Ia sengaja tidak memakai map[string]any: bidangnya diketahui, dan map
// membuat setiap pembacanya menebak tipe di tempat pemakaian.
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

// inputFromContext membaca masukan harian kembali dari konteksnya.
//
// Konteks yang tidak bisa dibaca menghasilkan masukan kosong, bukan galat:
// riwayat yang tidak bisa ditampilkan sama sekali adalah harga yang terlalu
// mahal untuk satu baris lama yang bentuknya berbeda.
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
