package domain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrResetTokenInvalid menutupi setiap alasan sebuah token reset ditolak:
	// tidak ada, sudah dipakai, sudah kedaluwarsa, bentuknya salah.
	// Pemanggil DILARANG membedakannya - "token ini pernah ada tapi sudah
	// dipakai" memberi tahu penyerang bahwa tebakannya benar.
	ErrResetTokenInvalid = errors.New("invalid password reset token")

	ErrResetTokenExpired = errors.New("password reset token expired")
	ErrResetTokenUsed    = errors.New("password reset token already used")
)

// resetTokenBytes adalah 32 byte, yaitu 256 bit keacakan.
//
// Segitu banyak sehingga menebaknya bukan ancaman yang perlu dilawan dengan
// pembatasan laju - berbeda dari kata sandi, yang dipilih manusia dan karena
// itu bisa ditebak.
const resetTokenBytes = 32

// resetTokenLifetime sengaja pendek. Token reset adalah kredensial penuh:
// siapa pun yang memegangnya bisa mengambil alih akun. Semakin lama ia hidup,
// semakin lama ia menunggu di kotak masuk yang mungkin sudah tidak aman.
const resetTokenLifetime = time.Hour

// ResetToken adalah rahasia yang dikirim ke pengguna. Seperti Password, ia
// tidak bisa mencetak dirinya sendiri.
type ResetToken struct {
	value string
}

// NewResetToken menghasilkan token acak baru.
func NewResetToken() (ResetToken, error) {
	raw := make([]byte, resetTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return ResetToken{}, fmt.Errorf("generating reset token: %w", err)
	}
	// base64url supaya ia aman ditempelkan ke tautan tanpa penyandian ulang.
	return ResetToken{value: base64.RawURLEncoding.EncodeToString(raw)}, nil
}

// ParseResetToken menerima token yang dikirim balik oleh pengguna.
//
// Ia hanya memeriksa bentuk, bukan keabsahan. Yang menentukan sah atau tidak
// adalah barisnya di penyimpanan.
func ParseResetToken(raw string) (ResetToken, error) {
	if raw == "" {
		return ResetToken{}, ErrResetTokenInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != resetTokenBytes {
		return ResetToken{}, ErrResetTokenInvalid
	}
	return ResetToken{value: raw}, nil
}

func (ResetToken) String() string   { return "[REDACTED]" }
func (ResetToken) GoString() string { return "domain.ResetToken{[REDACTED]}" }

// Expose mengeluarkan tokennya. Pemanggilnya hanya dua: yang menyusun tautan
// untuk dikirim, dan yang menghitung hash-nya.
func (t ResetToken) Expose() string { return t.value }

// ResetTokenHash adalah yang disimpan.
type ResetTokenHash [sha256.Size]byte

// HashResetToken menghitung hash yang disimpan.
//
// SHA-256, bukan argon2, dan itu disengaja. Argon2 melawan penebakan kata
// sandi yang dipilih manusia; token ini 256 bit acak, jadi tidak ada yang
// menebaknya. Yang dilawan di sini hanya satu hal: bocornya basis data
// DILARANG langsung berarti bocornya kemampuan mengambil alih akun.
func HashResetToken(t ResetToken) ResetTokenHash {
	return sha256.Sum256([]byte(t.value))
}

// Equal membandingkan dua hash dalam waktu tetap.
func (h ResetTokenHash) Equal(other ResetTokenHash) bool {
	return subtle.ConstantTimeCompare(h[:], other[:]) == 1
}

// PasswordReset adalah satu permintaan reset yang beredar.
type PasswordReset struct {
	TokenHash ResetTokenHash
	UserID    UserID
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// NewPasswordReset membuat permintaan baru beserta tokennya.
//
// Tokennya dikembalikan terpisah dan tidak pernah disimpan di dalam struct:
// setelah nilai balik ini dipakai, satu-satunya salinan yang tersisa ada di
// kotak masuk penggunanya.
func NewPasswordReset(userID UserID, now time.Time) (PasswordReset, ResetToken, error) {
	token, err := NewResetToken()
	if err != nil {
		return PasswordReset{}, ResetToken{}, err
	}
	return PasswordReset{
		TokenHash: HashResetToken(token),
		UserID:    userID,
		ExpiresAt: now.Add(resetTokenLifetime),
		CreatedAt: now,
	}, token, nil
}

// Redeem memeriksa apakah token ini masih boleh dipakai, dan menandainya
// terpakai bila ya.
//
// Pemeriksaan dan penandaan berada di satu tempat dengan sengaja. Kalau
// keduanya terpisah, akan selalu ada jalur yang memeriksa lalu lupa
// menandai - dan token sekali pakai yang tidak pernah ditandai adalah token
// yang bisa dipakai berkali-kali.
func (r *PasswordReset) Redeem(now time.Time) error {
	if r.UsedAt != nil {
		return ErrResetTokenUsed
	}
	if !now.Before(r.ExpiresAt) {
		return ErrResetTokenExpired
	}
	at := now
	r.UsedAt = &at
	return nil
}

// PasswordResetRepository adalah port penyimpanan permintaan reset.
type PasswordResetRepository interface {
	Create(ctx context.Context, r PasswordReset) error

	// FindByTokenHash mencari berdasarkan hash, bukan berdasarkan email.
	// Pencarian lewat email akan membuat token milik siapa pun bisa
	// dipasangkan dengan alamat siapa pun.
	FindByTokenHash(ctx context.Context, hash ResetTokenHash) (PasswordReset, error)

	// MarkUsed menyimpan penandaan terpakai.
	MarkUsed(ctx context.Context, hash ResetTokenHash, usedAt time.Time) error

	// InvalidateAllFor membatalkan seluruh permintaan yang masih beredar
	// milik seorang pengguna.
	//
	// Dipanggil setelah kata sandi benar-benar berganti: permintaan lain yang
	// masih hidup adalah kredensial yang masih berlaku atas akun yang baru
	// saja diamankan, dan yang paling mungkin menerbitkannya adalah orang
	// yang sedang mencoba merebut akun itu.
	InvalidateAllFor(ctx context.Context, userID UserID, at time.Time) error
}
