package handler

import (
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// idempotencyKeyFor mengikat kunci idempotensi klien ke pengguna yang
// mengirimnya.
//
// Kunci dari header dipakai APA ADANYA sebagai kunci pekerjaan di hilir
// (llm-worker, scope tunggal). Tanpa pengikatan ini, dua pengguna yang
// kebetulan - atau sengaja - memakai kunci yang sama saling meniadakan:
// pekerjaan kedua dibuang sebagai duplikat pekerjaan pertama. Pemisahnya
// karakter kontrol, bukan ":", supaya id pengguna dan kunci klien tidak bisa
// dirangkai menjadi kunci milik orang lain.
//
// nil bila klien tidak mengirim kunci: use case di hilir menurunkan kuncinya
// sendiri dari agregatnya.
func idempotencyKeyFor(claims domain.Claims, header string) *commonv1.IdempotencyKey {
	if header == "" {
		return nil
	}
	return &commonv1.IdempotencyKey{Value: claims.UserID.String() + "\x1f" + header}
}
