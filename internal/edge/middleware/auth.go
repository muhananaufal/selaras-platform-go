// Package middleware memuat lapisan yang membungkus setiap permintaan di edge.
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Kunci konteks tempat klaim disimpan setelah token diterima.
const (
	contextClaims = "selaras.claims"
)

// TokenVerifier memeriksa tanda tangan dan masa berlaku token.
//
// Gateway memverifikasinya SENDIRI dengan kunci publik. Itu inti ADR-007 dan
// alasan ADR-020 memilih EdDSA: kalau verifikasi dikerjakan identity-svc,
// setiap request terautentikasi menjadi satu panggilan jaringan wajib ke
// sana - dan itu justru yang dihapus.
type TokenVerifier interface {
	Verify(raw string) (domain.Claims, error)
}

// Authenticate menolak setiap permintaan yang tidak membawa token yang sah
// dan belum dicabut.
//
// Dua pemeriksaan, dan keduanya wajib. Tanda tangan membuktikan tokennya kami
// yang menerbitkan dan belum kedaluwarsa; generasi membuktikan ia belum
// dicabut. Tanpa yang kedua, logout dan reset kata sandi tidak berlaku sampai
// tokennya kedaluwarsa sendiri.
func Authenticate(tokens TokenVerifier, revocations domain.RevocationChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			unauthorized(c)
			return
		}

		claims, err := tokens.Verify(raw)
		if err != nil {
			// Alasan penolakan TIDAK disebutkan. Membedakan "tanda tangan
			// salah" dari "sudah kedaluwarsa" memberi tahu penyerang bahwa
			// tanda tangannya benar - artinya kuncinya bocor.
			unauthorized(c)
			return
		}

		current, err := revocations.IsCurrent(c.Request.Context(), claims.UserID, claims.Generation)
		if err != nil {
			// GAGAL-TERTUTUP (ADR-020). Tidak bisa memastikan pencabutan
			// berarti menolak, bukan menerima: menerima akan mengubah setiap
			// gangguan menjadi jendela di mana logout tidak berlaku.
			//
			// Jawabannya 503, bukan 401: kliennya tidak melakukan kesalahan
			// apa pun, dan 401 akan membuat aplikasi mengeluarkan penggunanya
			// karena gangguan sesaat di sisi kami.
			httperr.Write(c, http.StatusServiceUnavailable, httperr.CodeUnavailable,
				"Cannot verify the session right now.")
			return
		}
		if !current {
			unauthorized(c)
			return
		}

		c.Set(contextClaims, claims)
		c.Next()
	}
}

// bearerToken mengambil token dari header Authorization.
//
// Skemanya dibandingkan tanpa peduli besar-kecil huruf karena RFC 7235
// menyatakannya case-insensitive, dan klien yang mengirim "bearer" tidak
// sedang melakukan kesalahan.
func bearerToken(header string) (string, bool) {
	scheme, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(value)
	return token, token != ""
}

func unauthorized(c *gin.Context) {
	httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
}

// ClaimsFrom mengambil klaim yang sudah diverifikasi dari konteks.
//
// Nilai balik kedua bukan basa-basi: handler yang dipasang tanpa middleware
// ini akan mendapat false, dan itu jauh lebih baik daripada panik atau -
// jauh lebih buruk lagi - bekerja dengan klaim kosong dan memperlakukan
// permintaan tanpa token sebagai milik pengguna dengan id kosong.
func ClaimsFrom(c *gin.Context) (domain.Claims, bool) {
	value, exists := c.Get(contextClaims)
	if !exists {
		return domain.Claims{}, false
	}
	claims, ok := value.(domain.Claims)
	return claims, ok
}

// ErrNoClaims dikembalikan handler yang menuntut autentikasi tetapi tidak
// menemukannya - keadaan yang hanya bisa terjadi karena salah pasang rute.
var ErrNoClaims = errors.New("no verified claims on this request")
