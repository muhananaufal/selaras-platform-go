package handler

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
)

// bind mengurai badan JSON dan menjawab 422 bila cacat.
//
// Nilai baliknya boolean, bukan error, karena jawabannya sudah dikirim: setiap
// pemanggil hanya perlu tahu apakah ia boleh melanjutkan. Bentuk itu membuat
// pola "tulis galat lalu lupa return" tidak mungkin ditulis.
func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		httperr.WriteValidation(c, fieldErrors(err))
		return false
	}
	return true
}

// fieldErrors menerjemahkan galat validator menjadi bentuk per-bidang yang
// sudah dipakai frontend hari ini.
//
// Galat yang bukan dari validator - JSON yang rusak, misalnya - tidak punya
// bidang untuk ditunjuk, dan pesannya TIDAK diteruskan: pesan pengurai JSON
// membawa offset byte dan nama tipe Go, yang tidak berguna bagi klien dan
// membocorkan bentuk internalnya.
func fieldErrors(err error) map[string][]string {
	var invalid validator.ValidationErrors
	if !errors.As(err, &invalid) {
		return map[string][]string{
			"body": {"The request body could not be read."},
		}
	}

	fields := make(map[string][]string, len(invalid))
	for _, fieldErr := range invalid {
		name := jsonName(fieldErr.Field())
		fields[name] = append(fields[name], messageFor(fieldErr))
	}
	return fields
}

// jsonName mengubah nama bidang Go menjadi nama yang dikirim klien.
//
// Klien mengirim snake_case dan tidak pernah melihat nama Go-nya; galat yang
// menyebut "PasswordConfirmation" memaksa pembacanya menebak bidang mana yang
// dimaksud.
func jsonName(field string) string {
	var out strings.Builder
	for i, r := range field {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(r + ('a' - 'A'))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func messageFor(err validator.FieldError) string {
	switch err.Tag() {
	case "required":
		return "This field is required."
	case "email":
		return "This must be a valid email address."
	case "min":
		return "This must be at least " + err.Param() + " characters."
	case "max":
		return "This must not exceed " + err.Param() + " characters."
	default:
		return "This value is invalid."
	}
}

// bearer mengambil token dari header Authorization.
//
// Ia menganggap header-nya sudah lolos middleware autentikasi, jadi ia tidak
// memvalidasi ulang - yang dibutuhkan hanya nilainya untuk diteruskan.
func bearer(header string) string {
	_, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found {
		return ""
	}
	return strings.TrimSpace(value)
}
