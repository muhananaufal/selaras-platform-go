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

const resetColumns = `token_hash, user_id, expires_at, used_at, created_at`

// PasswordResetRepository menyimpan permintaan reset kata sandi.
type PasswordResetRepository struct {
	db pg.Querier
}

func NewPasswordResetRepository(db pg.Querier) *PasswordResetRepository {
	return &PasswordResetRepository{db: db}
}

var _ domain.PasswordResetRepository = (*PasswordResetRepository)(nil)

func (r *PasswordResetRepository) Create(ctx context.Context, reset domain.PasswordReset) error {
	const q = `
		INSERT INTO password_reset_tokens (` + resetColumns + `)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := r.db.Exec(ctx, q,
		reset.TokenHash[:], reset.UserID.String(),
		reset.ExpiresAt, reset.UsedAt, reset.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("storing password reset request: %w", err)
	}
	return nil
}

// FindByTokenHash mencari berdasarkan hash.
//
// Baris yang tidak ada mengembalikan ErrResetTokenInvalid, bukan galat
// tersendiri: pemanggil tidak boleh bisa membedakan "token ini tidak pernah
// ada" dari "token ini tidak sah lagi".
func (r *PasswordResetRepository) FindByTokenHash(
	ctx context.Context,
	hash domain.ResetTokenHash,
) (domain.PasswordReset, error) {
	const q = `SELECT ` + resetColumns + ` FROM password_reset_tokens WHERE token_hash = $1`

	var (
		storedHash []byte
		userID     string
		expiresAt  time.Time
		usedAt     *time.Time
		createdAt  time.Time
	)

	err := r.db.QueryRow(ctx, q, hash[:]).Scan(&storedHash, &userID, &expiresAt, &usedAt, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PasswordReset{}, domain.ErrResetTokenInvalid
	}
	if err != nil {
		return domain.PasswordReset{}, fmt.Errorf("querying password reset request: %w", err)
	}

	parsedID, err := domain.ParseUserID(userID)
	if err != nil {
		return domain.PasswordReset{}, fmt.Errorf("stored reset owner is not a user id: %w", err)
	}

	// Panjangnya diperiksa sebelum disalin. Baris yang panjangnya keliru
	// hanya bisa datang dari sesuatu yang menulis di luar kode ini, dan
	// menyalin diam-diam akan menghasilkan hash yang tidak cocok dengan apa
	// pun - kegagalan yang muncul jauh dari sebabnya.
	if len(storedHash) != len(domain.ResetTokenHash{}) {
		return domain.PasswordReset{}, fmt.Errorf(
			"stored reset hash is %d bytes; want %d", len(storedHash), len(domain.ResetTokenHash{}))
	}

	reset := domain.PasswordReset{
		UserID:    parsedID,
		ExpiresAt: expiresAt,
		UsedAt:    usedAt,
		CreatedAt: createdAt,
	}
	copy(reset.TokenHash[:], storedHash)
	return reset, nil
}

func (r *PasswordResetRepository) MarkUsed(
	ctx context.Context,
	hash domain.ResetTokenHash,
	usedAt time.Time,
) error {
	// used_at hanya ditulis bila masih kosong. Penandaan kedua DILARANG
	// menggeser waktu pemakaian yang pertama - itulah satu-satunya catatan
	// kapan token benar-benar dipakai.
	const q = `
		UPDATE password_reset_tokens
		SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL`

	tag, err := r.db.Exec(ctx, q, hash[:], usedAt)
	if err != nil {
		return fmt.Errorf("marking password reset request used: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Nol baris berarti tokennya tidak ada, atau sudah ditandai. Keduanya
		// berarti hal yang sama bagi pemanggil: ia tidak sah.
		return domain.ErrResetTokenInvalid
	}
	return nil
}

func (r *PasswordResetRepository) InvalidateAllFor(
	ctx context.Context,
	userID domain.UserID,
	at time.Time,
) error {
	const q = `
		UPDATE password_reset_tokens
		SET used_at = $2
		WHERE user_id = $1 AND used_at IS NULL`

	// Nol baris di sini bukan kegagalan: pengguna yang tidak punya permintaan
	// yang beredar memang tidak perlu dibatalkan apa-apa.
	if _, err := r.db.Exec(ctx, q, userID.String(), at); err != nil {
		return fmt.Errorf("invalidating outstanding reset requests: %w", err)
	}
	return nil
}
