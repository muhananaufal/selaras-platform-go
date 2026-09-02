package domain

import (
	"errors"
	"strings"
)

var (
	// ErrPasswordTooShort mempertahankan batas minimum sistem lama.
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")

	// ErrPasswordTooLong menjaga biaya hashing tetap terbatas.
	ErrPasswordTooLong = errors.New("password must not exceed 1024 characters")
)

const (
	minPasswordLength = 8

	// argon2 menerima masukan sepanjang apa pun dan biayanya tumbuh
	// bersamanya. Tanpa batas atas, siapa pun bisa mengirim megabita ke
	// endpoint login - yang tidak butuh autentikasi untuk dipanggil - dan
	// membakar CPU seluruh proses.
	maxPasswordLength = 1024
)

// Password adalah kata sandi mentah yang sudah lolos aturan panjang.
//
// Ia sengaja TIDAK bisa dicetak. String dan GoString mengembalikan
// penanda, bukan isinya, sehingga kata sandi tidak bocor lewat log,
// pesan galat, atau dump struct - tiga jalur kebocoran yang tidak
// bergantung pada kedisiplinan siapa pun.
type Password struct {
	value string
}

// NewPassword memvalidasi panjang kata sandi.
//
// Kompleksitas karakter sengaja tidak dipaksakan. Aturan semacam itu
// mendorong pola yang mudah ditebak, dan panjanglah yang benar-benar
// menentukan.
func NewPassword(raw string) (Password, error) {
	if strings.TrimSpace(raw) == "" || len(raw) < minPasswordLength {
		return Password{}, ErrPasswordTooShort
	}
	if len(raw) > maxPasswordLength {
		return Password{}, ErrPasswordTooLong
	}
	return Password{value: raw}, nil
}

// String memenuhi fmt.Stringer tanpa membocorkan isinya.
func (Password) String() string { return "[REDACTED]" }

// GoString memenuhi fmt.GoStringer, yang dipakai verb %#v.
func (Password) GoString() string { return "domain.Password{[REDACTED]}" }

// Expose mengembalikan nilai sebenarnya. Namanya sengaja canggung: satu-
// satunya pemanggil yang sah adalah pemasang hash.
func (p Password) Expose() string { return p.value }

// PasswordHash adalah hasil hashing yang sudah dikodekan, termasuk
// parameter dan salt-nya. Isinya buram bagi domain.
type PasswordHash string

// PasswordHasher adalah port. Implementasinya hidup di adapter, sehingga
// domain tidak pernah tahu algoritma mana yang dipakai - dan mengganti
// algoritma tidak menyentuh satu pun aturan bisnis.
type PasswordHasher interface {
	Hash(Password) (PasswordHash, error)
	// Verify mengembalikan needsRehash bila hash lama dibuat dengan
	// parameter yang kini dianggap terlalu lemah.
	Verify(hash PasswordHash, candidate Password) (ok bool, needsRehash bool, err error)
}
