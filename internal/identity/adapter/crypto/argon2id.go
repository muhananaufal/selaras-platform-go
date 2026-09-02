// Package crypto memasang port PasswordHasher milik domain.
package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ErrMalformedHash menandai hash yang tidak bisa diurai. Ia dibedakan dari
// "kata sandi salah" karena penyebabnya berbeda: yang satu masukan
// pengguna, yang satu data rusak di penyimpanan.
var ErrMalformedHash = errors.New("malformed password hash")

// Params adalah biaya argon2id.
//
// argon2id dipilih, bukan bcrypt, karena inilah yang dipakai untuk sistem
// baru: ia melawan serangan GPU lewat kebutuhan memori, yang tidak dimiliki
// bcrypt. Alasannya bukan ketiadaan hash lama - itu kebetulan, bukan
// argumen (ADR-016).
type Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams mengikuti profil yang direkomendasikan RFC 9106 untuk
// penggunaan umum: 64 MiB memori, tiga iterasi.
//
// Angka ini WAJIB ditinjau ulang terhadap perangkat keras nyata sebelum
// dipakai melayani trafik: parameter yang terlalu ringan tidak melindungi
// apa pun, dan yang terlalu berat mengubah login menjadi vektor
// denial-of-service terhadap diri sendiri.
func DefaultParams() Params {
	return Params{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// FastParamsForTests memangkas biaya supaya suite test tidak menghabiskan
// menit hanya untuk menunggu fungsi yang memang sengaja dibuat lambat.
// DILARANG dipakai di luar test.
func FastParamsForTests() Params {
	return Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
}

// Argon2idHasher memasang domain.PasswordHasher.
type Argon2idHasher struct {
	params Params
}

func NewArgon2idHasher(p Params) *Argon2idHasher { return &Argon2idHasher{params: p} }

var _ domain.PasswordHasher = (*Argon2idHasher)(nil)

// Hash menghasilkan string PHC yang membawa parameternya sendiri, sehingga
// biaya bisa dinaikkan kelak tanpa membatalkan hash yang sudah tersimpan.
func (h *Argon2idHasher) Hash(pw domain.Password) (domain.PasswordHash, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("reading salt: %w", err)
	}

	key := argon2.IDKey(
		[]byte(pw.Expose()), salt,
		h.params.Iterations, h.params.Memory, h.params.Parallelism, h.params.KeyLength,
	)

	return domain.PasswordHash(fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.Memory, h.params.Iterations, h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)), nil
}

// Verify membandingkan kandidat terhadap hash tersimpan.
//
// Perbandingannya waktu-tetap. Perbandingan biasa berhenti pada byte
// pertama yang berbeda, dan selisih waktunya cukup untuk menebak hash
// byte demi byte.
func (h *Argon2idHasher) Verify(stored domain.PasswordHash, candidate domain.Password) (bool, bool, error) {
	p, salt, want, err := decode(string(stored))
	if err != nil {
		return false, false, err
	}

	got := argon2.IDKey([]byte(candidate.Expose()), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}

	return true, h.outdated(p), nil
}

// outdated benar bila hash dibuat dengan biaya lebih rendah daripada yang
// dipakai sekarang. Pemanggil memakai ini untuk menaikkan hash secara
// diam-diam saat pengguna berikutnya berhasil masuk.
func (h *Argon2idHasher) outdated(p Params) bool {
	return p.Memory < h.params.Memory ||
		p.Iterations < h.params.Iterations ||
		p.Parallelism < h.params.Parallelism ||
		p.KeyLength < h.params.KeyLength
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	if version != argon2.Version {
		return Params{}, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrMalformedHash, version)
	}

	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}

	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(key))
	return p, salt, key, nil
}
