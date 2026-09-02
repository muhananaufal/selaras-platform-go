package handler

import (
	"github.com/gin-gonic/gin"
)

// Bentuk amplop jawaban adalah bagian dari kontrak, bukan detail penulisan.
// Kuncinya dikumpulkan di sini supaya salah ketik - "mesage" alih-alih
// "message" - gagal saat kompilasi, bukan diam-diam memecahkan klien yang
// mencari kunci yang tidak pernah datang.
const (
	keyData    = "data"
	keyMessage = "message"
	keySuccess = "success"
)

// writeData mengirim satu sumber daya di dalam amplop `data`.
func writeData(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{keyData: data})
}

// writeDataWithMessage dipakai endpoint yang sudah mengirim pesan bersama
// datanya hari ini; bentuk itu dipertahankan supaya frontend tidak berubah.
func writeDataWithMessage(c *gin.Context, status int, message string, data any) {
	c.JSON(status, gin.H{keyMessage: message, keyData: data})
}

// writeMessage mengirim jawaban yang tidak membawa sumber daya apa pun.
func writeMessage(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{keySuccess: true, keyMessage: message})
}
