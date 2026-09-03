// Package llm adalah batas antara sistem ini dan penyedia model bahasa.
//
// Batas itu ada karena satu alasan yang praktis: penyedia berubah, mahal, dan
// tidak bisa dipanggil dari test. Yang di dalam sini hanya bentuk permintaan
// dan jawabannya; yang tahu cara bicara HTTP ke Google ada di paket anaknya,
// dan paket ini TIDAK boleh mengimpornya - dijaga oleh boundary_test.go.
package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Request adalah satu permintaan ke model.
type Request struct {
	// System adalah instruksi peran. Ia dipisahkan dari Prompt karena penyedia
	// memperlakukannya berbeda, dan menggabungkannya menjadi satu string akan
	// membuang perbedaan itu.
	System string

	Prompt string

	// PromptVersion adalah versi templat yang menghasilkan Prompt.
	//
	// Ia wajib. Hasil yang tersimpan tanpa versi promptnya tidak bisa
	// dijelaskan setelah promptnya berubah: tidak ada cara mengetahui apakah
	// jawaban yang aneh berasal dari model atau dari templat yang sudah
	// diganti (F3-09).
	PromptVersion string

	// Temperature 0 berarti "pakai bawaan penyedia". Nilai negatif ditolak.
	Temperature float64

	// MaxOutputBytes membatasi ukuran jawaban yang mau diterima.
	//
	// Tanpa batas, satu jawaban yang mengoceh bisa memenuhi memori worker dan
	// kolom basis datanya. Nol berarti memakai DefaultMaxOutputBytes.
	MaxOutputBytes int

	// JSON meminta jawaban berupa JSON. Penyedia yang mendukungnya diminta
	// menegakkannya; yang tidak, jawabannya tetap diperiksa di sisi ini.
	JSON bool
}

// DefaultMaxOutputBytes adalah batas bawaan ukuran jawaban.
//
// Laporan personalisasi terpanjang di sistem lama berada jauh di bawah ini;
// angkanya dipilih longgar supaya tidak memotong jawaban yang sah, tetapi tetap
// terbatas supaya jawaban yang kabur tidak menghabiskan memori.
const DefaultMaxOutputBytes = 256 * 1024

// Response adalah jawaban model beserta yang perlu dicatat bersamanya.
type Response struct {
	Text string

	// Model adalah nama model yang benar-benar menjawab, sebagaimana dilaporkan
	// penyedia - bukan nama yang diminta. Keduanya bisa berbeda saat penyedia
	// mengalihkan permintaan, dan yang perlu dicatat adalah yang menjawab.
	Model string

	// PromptVersion dibawa kembali dari permintaannya supaya pemanggil tidak
	// perlu memasangkannya sendiri.
	PromptVersion string

	// FinishReason menyebutkan mengapa model berhenti. Jawaban yang terpotong
	// karena batas token bukan jawaban yang selesai, dan membedakannya
	// mencegah laporan setengah jadi tersimpan sebagai laporan utuh.
	FinishReason string
}

// Truncated menyatakan jawabannya terpotong.
func (r *Response) Truncated() bool {
	return !strings.EqualFold(r.FinishReason, "stop") && r.FinishReason != ""
}

// Provider adalah yang dibutuhkan sistem dari sebuah model bahasa.
//
// Sesempit ini dengan sengaja. Antarmuka yang mencerminkan seluruh kemampuan
// penyedia akan mengunci sistem pada bentuk penyedia itu, dan penyedia
// berikutnya tidak akan cocok.
type Provider interface {
	// Name adalah nama penyedia untuk log dan metrik.
	Name() string

	// Generate meminta satu jawaban.
	//
	// Ia WAJIB menghormati ctx: pekerjaan LLM menunggu jaringan selama puluhan
	// detik, dan worker yang tidak bisa dihentikan di tengahnya akan menahan
	// shutdown sampai timeout paksa.
	Generate(ctx context.Context, req Request) (*Response, error)
}

// Galat yang dikenali pemanggil.
var (
	// ErrRateLimited berarti penyedia menolak sementara karena kuota. Ia layak
	// dicoba lagi, dan itulah sebabnya ia dibedakan dari galat lain.
	ErrRateLimited = errors.New("the provider is rate limiting us")

	// ErrTruncated berarti jawabannya melewati batas yang diminta.
	ErrTruncated = errors.New("the answer was cut off")

	// ErrEmptyAnswer berarti penyedia menjawab tanpa isi. Ia bukan kegagalan
	// jaringan, jadi mencoba lagi biasanya sia-sia - tetapi menyimpannya
	// sebagai jawaban jauh lebih buruk.
	ErrEmptyAnswer = errors.New("the provider answered with nothing")
)

// Validate memeriksa permintaan sebelum ia dikirim ke mana pun.
//
// Ia di sini, bukan di tiap adapter, supaya penyedia baru tidak bisa
// melonggarkan aturannya diam-diam.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("an empty prompt would spend a request on nothing")
	}
	if r.PromptVersion == "" {
		return errors.New("a result without its prompt version cannot be explained later")
	}
	if r.Temperature < 0 {
		return fmt.Errorf("temperature %v is below zero", r.Temperature)
	}
	if r.MaxOutputBytes < 0 {
		return fmt.Errorf("max output %d is below zero", r.MaxOutputBytes)
	}
	return nil
}

// Limit mengembalikan batas ukuran yang berlaku.
func (r Request) Limit() int {
	if r.MaxOutputBytes <= 0 {
		return DefaultMaxOutputBytes
	}
	return r.MaxOutputBytes
}
