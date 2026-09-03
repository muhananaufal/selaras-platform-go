package domain

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// slugBytes adalah 10 byte, 80 bit.
//
// Slug adalah id publik dan satu-satunya yang melindunginya dari ditebak. Id
// berurutan - seperti bigint auto-increment di sistem lama - akan membiarkan
// siapa pun menelusuri program orang lain hanya dengan menghitung, dan
// otorisasi yang benar pun tidak menghapus fakta bahwa jumlahnya jadi bisa
// dihitung.
const slugBytes = 10

// slugEncoding memakai base32 huruf kecil tanpa padding: aman di URL, dan tidak
// punya pasangan karakter yang mudah tertukar saat dibacakan.
var slugEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewSlug menghasilkan id publik baru.
func NewSlug() (string, error) {
	raw := make([]byte, slugBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating slug: %w", err)
	}
	return slugEncoding.EncodeToString(raw), nil
}

// NormaliseSlug membersihkan slug yang datang dari URL.
//
// Huruf besar dan spasi di ujung datang dari salin-tempel, bukan dari niat
// mencari sesuatu yang lain.
func NormaliseSlug(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
