package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Fake adalah penyedia yang dipakai seluruh test.
//
// Ia ada supaya `go test ./...` tidak pernah menyentuh jaringan (R6). Test yang
// memanggil penyedia sungguhan lambat, berbiaya, gagal saat internet mati, dan
// hasilnya berubah dari satu jalankan ke jalankan berikutnya - test yang
// hasilnya berubah tidak membuktikan apa-apa.
//
// Jawabannya deterministik: prompt yang sama selalu menghasilkan jawaban yang
// sama. Itu yang membuat test idempotensi bisa membedakan "dikerjakan sekali"
// dari "dikerjakan dua kali dengan hasil kebetulan sama".
type Fake struct {
	mu sync.Mutex

	// Answer, bila diisi, dipakai apa adanya. Kosong berarti jawaban turunan
	// yang deterministik.
	Answer string

	// Err, bila diisi, dikembalikan alih-alih jawaban. Ia yang membuat jalur
	// kegagalan bisa diuji tanpa mematikan apa pun.
	Err error

	// FinishReason bawaan "stop". Diisi lain untuk menguji jawaban terpotong.
	FinishReason string

	// Model adalah nama yang dilaporkan sebagai penjawab.
	Model string

	calls []Request
}

// NewFake membuat penyedia palsu dengan nilai bawaan yang masuk akal.
func NewFake() *Fake {
	return &Fake{FinishReason: "stop", Model: "fake-1"}
}

var _ Provider = (*Fake)(nil)

func (f *Fake) Name() string { return "fake" }

// Generate menjawab tanpa menyentuh apa pun di luar proses ini.
func (f *Fake) Generate(ctx context.Context, req Request) (*Response, error) {
	// ctx tetap dihormati meski tidak ada jaringan. Penyedia palsu yang
	// mengabaikan pembatalan akan menyembunyikan worker yang tidak bisa
	// dihentikan, dan itu justru yang ingin diuji.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.calls = append(f.calls, req)
	err := f.Err
	answer := f.Answer
	finish := f.FinishReason
	model := f.Model
	f.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if answer == "" {
		answer = deterministicAnswer(req)
	}
	if len(answer) > req.Limit() {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d byte limit",
			ErrTruncated, len(answer), req.Limit())
	}

	return &Response{
		Text:          answer,
		Model:         model,
		PromptVersion: req.PromptVersion,
		FinishReason:  finish,
	}, nil
}

// Calls mengembalikan salinan permintaan yang pernah diterima.
//
// Salinan, bukan slice aslinya: mengembalikan yang asli akan membuat pemanggil
// bisa mengubah catatan yang sedang ditulis goroutine lain.
func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.calls...)
}

// CallCount adalah jumlah permintaan yang pernah masuk.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// deterministicAnswer menurunkan jawaban dari promptnya.
//
// Bentuknya mengikuti BENTUK YANG DIMINTA promptnya, bukan satu bentuk untuk
// semua. Alasannya ditemukan dengan menjalankannya: versi pertama selalu
// mengembalikan bentuk yang sama, sehingga kurikulum yang diminta coaching-svc
// tersimpan sebagai laporan kelulusan - konsumennya membedakan keduanya dari
// ada tidaknya "weeks", dan jawaban palsu itu tidak punya keduanya.
//
// Jawaban palsu yang bentuknya berbeda dari yang sesungguhnya membuat test
// lulus terhadap sesuatu yang tidak pernah terjadi di produksi.
func deterministicAnswer(req Request) string {
	sum := sha256.Sum256([]byte(req.System + "\x1f" + req.Prompt))
	digest := hex.EncodeToString(sum[:])

	payload := shapeFor(req.PromptVersion, digest)
	payload["generated_by"] = "fake"
	payload["prompt_version"] = req.PromptVersion
	payload["digest"] = digest

	// Marshal map[string]any dengan kunci yang tetap tidak bisa gagal, tetapi
	// galatnya tetap tidak diabaikan: mengabaikannya berarti jawaban kosong
	// akan lolos sebagai jawaban yang sah.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("{%q:%q}", "error", err.Error())
	}
	return string(encoded)
}

// shapeFor menghasilkan kerangka jawaban yang sesuai templat yang memintanya.
//
// Ia dikenali dari PromptVersion, yang berbentuk "<nama templat>@<versi>".
// Isinya sengaja minimal tetapi BERBENTUK BENAR: yang diuji jalur ujung ke
// ujung adalah apakah hasilnya bisa dibaca dan disimpan, bukan apakah isinya
// bermakna secara klinis.
func shapeFor(promptVersion, digest string) map[string]any {
	name, _, _ := strings.Cut(promptVersion, "@")

	switch name {
	case "curriculum":
		return map[string]any{
			"program_title":       "Program Palsu untuk Pengujian",
			"program_description": "Kurikulum yang dihasilkan penyedia palsu.",
			"weeks":               fakeWeeks(),
		}

	case "graduation":
		return map[string]any{
			"headline": "Program selesai",
			"summary":  "Ringkasan yang dihasilkan penyedia palsu.",
			"completion": map[string]any{
				"total": 0, "completed": 0, "note": "angka dari penyedia palsu",
			},
			"what_went_well":        []string{"memulai program"},
			"what_to_carry_forward": []string{"kebiasaan harian"},
			"next_step":             "lanjutkan pekan berikutnya",
		}

	case "chat_reply":
		return map[string]any{
			"text":        "Balasan yang dihasilkan penyedia palsu.",
			"suggestions": []string{},
		}

	default:
		// Personalisasi dan apa pun yang belum dikenali: bentuk lama, yang
		// sudah cukup untuk membuktikan jawaban sampai dan tersimpan.
		return map[string]any{}
	}
}

// fakeWeeks menghasilkan empat pekan berisi satu misi utama per hari.
//
// Tanggalnya berurutan tanpa lompatan, seperti yang diminta prompt-nya: pembaca
// kurikulum menolak tanggal yang tidak bisa dibaca, dan kerangka yang melanggar
// aturannya sendiri tidak membuktikan apa pun.
func fakeWeeks() []map[string]any {
	start := time.Now().Truncate(24 * time.Hour)

	weeks := make([]map[string]any, 0, 4)
	day := 0
	for w := 1; w <= 4; w++ {
		tasks := make([]map[string]any, 0, 7)
		for range 7 {
			tasks = append(tasks, map[string]any{
				"task_date": start.AddDate(0, 0, day).Format(time.DateOnly),
				"main_mission": map[string]any{
					"task_type":   "main_mission",
					"title":       "Jalan kaki 20 menit",
					"description": "Tugas yang dihasilkan penyedia palsu.",
				},
				"bonus_challenges": []any{},
			})
			day++
		}
		weeks = append(weeks, map[string]any{
			"week_number": w,
			"title":       fmt.Sprintf("Pekan %d", w),
			"description": "Pekan yang dihasilkan penyedia palsu.",
			"tasks":       tasks,
		})
	}
	return weeks
}
