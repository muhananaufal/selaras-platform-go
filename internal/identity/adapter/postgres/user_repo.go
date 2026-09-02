// Package postgres menyimpan agregat identity di Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Nama batasan dari migrasi. Dipakai untuk menerjemahkan bentrokan indeks
// menjadi error domain, sehingga pemanggil bisa membedakan "email sudah
// dipakai" dari "basis data sedang bermasalah" tanpa melihat SQLSTATE.
const (
	constraintEmailUnique    = "users_email_unique_alive"
	constraintGoogleIDUnique = "users_google_id_unique_alive"
)

// Kolom disebutkan satu per satu, tidak pernah SELECT *. Urutan kolom pada
// SELECT * ditentukan oleh basis data, jadi menambah satu kolom lewat
// migrasi bisa menggeser hasil Scan tanpa satu pun error saat kompilasi.
const userColumns = `id, email, role, password_hash, google_id,
	email_verified_at, token_generation, created_at, updated_at, deleted_at`

// UserRepository memenuhi domain.UserRepository.
type UserRepository struct {
	db pg.Querier
}

func NewUserRepository(db pg.Querier) *UserRepository { return &UserRepository{db: db} }

var _ domain.UserRepository = (*UserRepository)(nil)

// WithQuerier mengembalikan repository yang sama di atas transaksi berjalan.
//
// Inilah yang membuat pola outbox mungkin: use case memulai transaksi, dan
// baris user serta baris event ditulis lewat Querier yang sama, sehingga
// keduanya selamat bersama atau tidak sama sekali.
func (r *UserRepository) WithQuerier(q pg.Querier) *UserRepository {
	return &UserRepository{db: q}
}

func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	s := u.State()

	const q = `
		INSERT INTO users (` + userColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.db.Exec(ctx, q,
		s.ID.String(), s.Email.String(), s.Role.String(),
		nullable(string(s.PasswordHash)), nullable(s.GoogleID),
		s.EmailVerifiedAt, s.TokenGeneration, s.CreatedAt, s.UpdatedAt, s.DeletedAt,
	)
	if err != nil {
		return translate(err)
	}
	return nil
}

func (r *UserRepository) Update(ctx context.Context, u *domain.User) error {
	s := u.State()

	const q = `
		UPDATE users SET
			email = $2, role = $3, password_hash = $4, google_id = $5,
			email_verified_at = $6, token_generation = $7, updated_at = $8, deleted_at = $9
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, q,
		s.ID.String(), s.Email.String(), s.Role.String(),
		nullable(string(s.PasswordHash)), nullable(s.GoogleID),
		s.EmailVerifiedAt, s.TokenGeneration, s.UpdatedAt, s.DeletedAt,
	)
	if err != nil {
		return translate(err)
	}
	// Nol baris berarti barisnya tidak ada. Tanpa pemeriksaan ini, menyimpan
	// user yang sudah terhapus akan sukses tanpa mengubah apa pun.
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// Ketiga pencarian menyaring deleted_at IS NULL. Akun terhapus lunak disimpan
// untuk audit, bukan untuk autentikasi, dan lupa satu saja penyaring itu
// berarti akun yang sudah dihapus bisa masuk kembali.

func (r *UserRepository) FindByID(ctx context.Context, id domain.UserID) (*domain.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE id = $1 AND deleted_at IS NULL`
	return r.one(ctx, q, id.String())
}

func (r *UserRepository) FindByEmail(ctx context.Context, email domain.Email) (*domain.User, error) {
	// Email sudah dinormalkan menjadi huruf kecil oleh domain.NewEmail, jadi
	// pembandingannya lurus dan indeksnya terpakai. Tanpa normalisasi di
	// domain, di sini terpaksa LOWER(email), dan indeks unik biasa tidak
	// akan lagi mencegah alamat yang sama dengan besar-kecil berbeda.
	const q = `SELECT ` + userColumns + ` FROM users WHERE email = $1 AND deleted_at IS NULL`
	return r.one(ctx, q, email.String())
}

func (r *UserRepository) FindByGoogleID(ctx context.Context, googleID string) (*domain.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE google_id = $1 AND deleted_at IS NULL`
	return r.one(ctx, q, googleID)
}

func (r *UserRepository) one(ctx context.Context, query string, arg any) (*domain.User, error) {
	var (
		id, email, role string
		hash, googleID  *string
		verifiedAt      *time.Time
		generation      int64
		createdAt       time.Time
		updatedAt       time.Time
		deletedAt       *time.Time
	)

	err := r.db.QueryRow(ctx, query, arg).Scan(
		&id, &email, &role, &hash, &googleID,
		&verifiedAt, &generation, &createdAt, &updatedAt, &deletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying user: %w", err)
	}

	return hydrate(id, email, role, hash, googleID, verifiedAt, generation, createdAt, updatedAt, deletedAt)
}

func hydrate(
	id, email, role string,
	hash, googleID *string,
	verifiedAt *time.Time,
	generation int64,
	createdAt, updatedAt time.Time,
	deletedAt *time.Time,
) (*domain.User, error) {
	// Baris yang tersimpan adalah fakta, bukan permintaan - tetapi ia tetap
	// bisa rusak, misalnya karena migrasi manual. Yang gagal diurai
	// dilaporkan, bukan diam-diam diganti nilai bawaan.
	parsedID, err := domain.ParseUserID(id)
	if err != nil {
		return nil, fmt.Errorf("stored user id is not a uuid: %w", err)
	}
	parsedEmail, err := domain.NewEmail(email)
	if err != nil {
		return nil, fmt.Errorf("stored email is invalid: %w", err)
	}
	parsedRole, err := domain.NewRole(role)
	if err != nil {
		return nil, fmt.Errorf("stored role is invalid: %w", err)
	}

	return domain.Hydrate(domain.UserState{
		ID:              parsedID,
		Email:           parsedEmail,
		Role:            parsedRole,
		PasswordHash:    domain.PasswordHash(deref(hash)),
		GoogleID:        deref(googleID),
		EmailVerifiedAt: verifiedAt,
		TokenGeneration: generation,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
		DeletedAt:       deletedAt,
	}), nil
}

// nullable memetakan string kosong ke NULL.
//
// Ketiadaan disimpan sebagai ketiadaan. Kalau tidak, indeks unik parsial
// pada google_id akan memperlakukan seluruh pengguna non-Google sebagai
// pemegang google id yang sama - string kosong - dan menolak pendaftaran
// kedua. NULL tidak pernah bentrok dengan NULL.
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

func translate(err error) error {
	switch {
	case pg.IsUniqueViolation(err, constraintEmailUnique):
		return domain.ErrEmailTaken
	case pg.IsUniqueViolation(err, constraintGoogleIDUnique):
		return domain.ErrGoogleIDTaken
	default:
		return fmt.Errorf("writing user: %w", err)
	}
}
