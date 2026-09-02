// Package domain memuat aturan identitas yang tidak bergantung pada
// penyimpanan, transport, maupun framework apa pun. Ia sengaja tidak
// mengimpor apa pun dari adapter: itulah yang membuat pilihan library di
// luar sini tetap murah dibalik.
package domain

import (
	"errors"
	"strings"
)

// ErrInvalidEmail dikembalikan ketika alamat tidak memenuhi bentuk minimum.
var ErrInvalidEmail = errors.New("invalid email address")

// Email adalah alamat yang sudah dinormalisasi.
//
// Tipe ini nilai, bukan string telanjang, supaya alamat yang belum
// divalidasi tidak bisa menyelinap ke dalam domain hanya karena ia
// kebetulan berbentuk string.
type Email struct {
	value string
}

// NewEmail menormalisasi dan memvalidasi sebuah alamat.
//
// Normalisasi memakai huruf kecil karena alamat yang hanya berbeda
// besar-kecil adalah orang yang sama; tanpa itu dua akun bisa lahir untuk
// satu alamat, dan keunikan di database tidak akan menangkapnya.
//
// Validasinya sengaja minimum. Satu-satunya cara membuktikan sebuah alamat
// benar-benar ada adalah mengirim surat ke sana, dan regex yang berusaha
// menegakkan RFC 5322 justru menolak alamat yang sah.
func NewEmail(raw string) (Email, error) {
	v := strings.ToLower(strings.TrimSpace(raw))

	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return Email{}, ErrInvalidEmail
	}

	at := strings.LastIndex(v, "@")
	if at <= 0 || at == len(v)-1 {
		return Email{}, ErrInvalidEmail
	}
	if !strings.Contains(v[at+1:], ".") {
		return Email{}, ErrInvalidEmail
	}

	return Email{value: v}, nil
}

func (e Email) String() string { return e.value }

// IsZero menandai Email yang belum pernah dibangun lewat NewEmail.
func (e Email) IsZero() bool { return e.value == "" }
