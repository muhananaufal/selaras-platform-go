package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
)

// MaxBodyBytes adalah batas ukuran badan permintaan.
//
// Satu megabyte. Badan terbesar yang sah di API ini adalah kuesioner penilaian
// risiko - beberapa puluh bidang - dan pesan chat yang dibatasi 16 KiB di
// domainnya. Satu megabyte memberi ruang berlipat-lipat di atas keduanya sambil
// tetap menghentikan yang tidak masuk akal.
//
// Tanpa batas, satu permintaan bisa memaksa gateway membaca seluruh badan ke
// memori sebelum ada validasi apa pun yang sempat berjalan - dan cara termurah
// menjatuhkan sebuah service adalah mengirimkannya sesuatu yang sangat besar.
const MaxBodyBytes int64 = 1 << 20

// LimitBody menolak badan permintaan yang terlalu besar.
//
// Ia memasang http.MaxBytesReader, yang menghentikan pembacaan DI TENGAH JALAN
// alih-alih membaca seluruhnya lalu mengukurnya. Membaca dulu baru mengukur
// tidak melindungi apa pun: memorinya sudah terpakai saat ukurannya diketahui.
//
// Content-Length yang menyatakan ukuran besar ditolak lebih awal lagi, tanpa
// membaca satu byte pun. Header itu tidak dipercaya sebagai kebenaran - pembaca
// berbatas di bawah tetap menjaga permintaan yang berbohong - tetapi menolak
// yang jujur lebih awal menghemat pekerjaan.
func LimitBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}

		if c.Request.ContentLength > MaxBodyBytes {
			tooLarge(c)
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxBodyBytes)
		c.Next()
	}
}

// tooLarge menjawab dengan bentuk galat yang sama seperti penolakan lain.
//
// Bentuk yang seragam adalah intinya: klien yang menangani galat lewat bidang
// `code` tidak boleh menemukan satu endpoint yang menjawab dengan bentuk lain,
// karena penanganan galat yang bercabang selalu punya cabang yang tidak diuji.
func tooLarge(c *gin.Context) {
	httperr.Write(c, http.StatusRequestEntityTooLarge, httperr.CodeInvalidArgument,
		"The request body is too large.")
	c.Abort()
}
