// Package postgres menyimpan agregat profile di Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

const constraintUserUnique = "user_profiles_user_id_unique"

// Kolom disebutkan satu per satu, tidak pernah SELECT *: urutan kolom pada
// SELECT * ditentukan basis data, jadi menambah kolom lewat migrasi bisa
// menggeser hasil Scan tanpa satu pun error saat kompilasi.
const profileColumns = `id, user_id, first_name, last_name, date_of_birth,
	sex, country_of_residence, language, created_at, updated_at`

// ProfileRepository memenuhi domain.ProfileRepository.
type ProfileRepository struct {
	db pg.Querier
}

func NewProfileRepository(db pg.Querier) *ProfileRepository {
	return &ProfileRepository{db: db}
}

var _ domain.ProfileRepository = (*ProfileRepository)(nil)

func (r *ProfileRepository) Create(ctx context.Context, p *domain.Profile) error {
	s := p.State()

	const q = `
		INSERT INTO user_profiles (` + profileColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.db.Exec(ctx, q,
		s.ID.String(), s.UserID.String(),
		nullable(s.FirstName), nullable(s.LastName), s.DateOfBirth,
		nullable(s.Sex), nullable(s.CountryOfResidence), s.Language,
		s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		if pg.IsUniqueViolation(err, constraintUserUnique) {
			return domain.ErrProfileExists
		}
		return fmt.Errorf("storing profile: %w", err)
	}
	return nil
}

func (r *ProfileRepository) Update(ctx context.Context, p *domain.Profile) error {
	s := p.State()

	const q = `
		UPDATE user_profiles SET
			first_name = $2, last_name = $3, date_of_birth = $4,
			sex = $5, country_of_residence = $6, language = $7, updated_at = $8
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, q,
		s.ID.String(),
		nullable(s.FirstName), nullable(s.LastName), s.DateOfBirth,
		nullable(s.Sex), nullable(s.CountryOfResidence), s.Language, s.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("updating profile: %w", err)
	}
	// Nol baris berarti barisnya tidak ada. Tanpa pemeriksaan ini, menyimpan
	// profil yang sudah terhapus akan sukses tanpa mengubah apa pun.
	if tag.RowsAffected() == 0 {
		return domain.ErrProfileNotFound
	}
	return nil
}

func (r *ProfileRepository) FindByID(ctx context.Context, id domain.ProfileID) (*domain.Profile, error) {
	const q = `SELECT ` + profileColumns + ` FROM user_profiles WHERE id = $1`
	return r.one(ctx, q, id.String())
}

func (r *ProfileRepository) FindByUserID(ctx context.Context, userID domain.UserID) (*domain.Profile, error) {
	const q = `SELECT ` + profileColumns + ` FROM user_profiles WHERE user_id = $1`
	return r.one(ctx, q, userID.String())
}

func (r *ProfileRepository) one(ctx context.Context, query string, arg any) (*domain.Profile, error) {
	var (
		id, userID              string
		firstName, lastName     *string
		dateOfBirth             *time.Time
		sex, countryOfResidence *string
		language                string
		createdAt, updatedAt    time.Time
	)

	err := r.db.QueryRow(ctx, query, arg).Scan(
		&id, &userID, &firstName, &lastName, &dateOfBirth,
		&sex, &countryOfResidence, &language, &createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrProfileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying profile: %w", err)
	}

	parsedID, err := domain.ParseProfileID(id)
	if err != nil {
		return nil, fmt.Errorf("stored profile id is not a uuid: %w", err)
	}
	parsedUserID, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, fmt.Errorf("stored user id is not a uuid: %w", err)
	}

	return domain.Hydrate(domain.ProfileState{
		ID:     parsedID,
		UserID: parsedUserID,
		// Nilai yang tidak ada dibaca kembali sebagai tidak ada. Mengubah
		// NULL menjadi sesuatu di sini adalah persis cara B6 lahir di sistem
		// lama, hanya satu lapisan lebih rendah.
		FirstName:          deref(firstName),
		LastName:           deref(lastName),
		DateOfBirth:        dateOfBirth,
		Sex:                deref(sex),
		CountryOfResidence: deref(countryOfResidence),
		Language:           language,
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
	}), nil
}

// nullable memetakan string kosong ke NULL, supaya ketiadaan disimpan sebagai
// ketiadaan dan bukan sebagai string kosong yang menyamar sebagai nilai.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
