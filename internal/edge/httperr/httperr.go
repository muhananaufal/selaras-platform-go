// Package httperr menerjemahkan galat dari service ke jawaban HTTP.
package httperr

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Body adalah bentuk galat yang dijanjikan kontrak REST.
type Body struct {
	Success bool                `json:"success"`
	Message string              `json:"message"`
	Code    string              `json:"code,omitempty"`
	Errors  map[string][]string `json:"errors,omitempty"`
}

// Kode yang boleh keluar. Daftarnya tertutup dan cocok dengan enum di
// edge-v1.yaml: kode yang tidak ada di sana adalah kontrak yang dilanggar
// diam-diam, dan klien tidak punya cara menanganinya.
const (
	CodeInvalidArgument    = "INVALID_ARGUMENT"
	CodeUnauthenticated    = "UNAUTHENTICATED"
	CodeNotFound           = "NOT_FOUND"
	CodeAlreadyExists      = "ALREADY_EXISTS"
	CodeFailedPrecondition = "FAILED_PRECONDITION"

	// CodePermissionDenied dipakai saat pemanggilnya SUDAH terautentikasi
	// tetapi tetap tidak boleh melakukannya - misalnya kata sandi yang keliru
	// saat mengonfirmasi penghapusan akun.
	//
	// Sebelumnya jalur itu memakai FAILED_PRECONDITION, dan itu keliru: klien
	// yang membedakan kesalahan berdasarkan kode akan memperlakukannya sebagai
	// "keadaan belum siap" alih-alih "yang Anda ketik salah", lalu mencoba lagi
	// dengan masukan yang sama persis.
	CodePermissionDenied = "PERMISSION_DENIED"
	CodeRateLimited      = "RATE_LIMITED"
	CodeInternal         = "INTERNAL"
	CodeUnavailable      = "UNAVAILABLE"
)

// Write mengirim jawaban galat dan menghentikan rantai handler.
//
// Abort dipanggil, bukan sekadar menulis: tanpa itu, middleware atau handler
// berikutnya tetap berjalan dan bisa menulis badan kedua ke koneksi yang
// sama.
func Write(c *gin.Context, statusCode int, code, message string) {
	c.AbortWithStatusJSON(statusCode, Body{
		Success: false,
		Message: message,
		Code:    code,
	})
}

// WriteValidation mengirim 422 beserta galat per bidang, bentuk yang sudah
// dipakai frontend hari ini.
func WriteValidation(c *gin.Context, fields map[string][]string) {
	c.AbortWithStatusJSON(http.StatusUnprocessableEntity, Body{
		Success: false,
		Message: "The given data was invalid.",
		Code:    CodeInvalidArgument,
		Errors:  fields,
	})
}

// FromGRPC menerjemahkan galat dari service di belakang gateway.
//
// Pesan dari service internal TIDAK diteruskan apa adanya untuk kelas galat
// yang tidak dikenali. Pesan internal membawa nama tabel, potongan kueri, dan
// alamat host - semuanya berguna bagi orang yang sedang memetakan sistem ini,
// dan tidak satu pun berguna bagi klien yang sah.
//
// Untuk kelas yang dikenali, pesannya memang sudah ditulis untuk dibaca
// pemanggil di sisi service, jadi ia diteruskan.
func FromGRPC(c *gin.Context, err error) {
	st, ok := status.FromError(err)
	if !ok {
		writeUnexpected(c, err)
		return
	}

	switch st.Code() {
	case codes.InvalidArgument:
		Write(c, http.StatusUnprocessableEntity, CodeInvalidArgument, st.Message())

	// Unauthenticated selalu satu pesan, tanpa keterangan tambahan.
	// Membedakan "email tidak terdaftar" dari "kata sandi keliru" di sini
	// akan membatalkan penyeragaman yang dikerjakan identity-svc.
	case codes.Unauthenticated:
		Write(c, http.StatusUnauthorized, CodeUnauthenticated, "Unauthenticated.")

	case codes.PermissionDenied:
		Write(c, http.StatusForbidden, CodePermissionDenied, st.Message())

	case codes.NotFound:
		Write(c, http.StatusNotFound, CodeNotFound, st.Message())

	case codes.AlreadyExists:
		Write(c, http.StatusConflict, CodeAlreadyExists, st.Message())

	case codes.FailedPrecondition:
		Write(c, http.StatusConflict, CodeFailedPrecondition, st.Message())

	case codes.ResourceExhausted:
		Write(c, http.StatusTooManyRequests, CodeRateLimited, "Too many requests.")

	// Unimplemented adalah kemampuan yang memang belum ada, bukan kekeliruan
	// klien. 501 mengatakannya persis, dan pesannya boleh lewat karena ia
	// ditulis untuk dibaca manusia yang sedang mencoba memakainya.
	case codes.Unimplemented:
		Write(c, http.StatusNotImplemented, CodeUnavailable, st.Message())

	case codes.Unavailable:
		Write(c, http.StatusServiceUnavailable, CodeUnavailable, "The service is temporarily unavailable.")

	case codes.DeadlineExceeded:
		Write(c, http.StatusGatewayTimeout, CodeUnavailable, "The request took too long.")

	// Pemanggil yang pergi bukan galat yang perlu dilaporkan ke siapa pun.
	// Menulis jawaban ke koneksi yang sudah tertutup hanya menambah bising
	// di log tanpa menolong siapa pun.
	case codes.Canceled:
		c.Abort()

	default:
		writeUnexpected(c, err)
	}
}

func writeUnexpected(c *gin.Context, err error) {
	if errors.Is(err, context.Canceled) {
		c.Abort()
		return
	}

	slog.ErrorContext(c.Request.Context(), "unhandled upstream error",
		"path", c.FullPath(), "method", c.Request.Method, "error", err)
	Write(c, http.StatusInternalServerError, CodeInternal, "Internal server error.")
}
