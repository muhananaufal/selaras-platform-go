package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
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
// Bentuknya JSON karena itu yang diminta jalur personalisasi, dan jawaban yang
// bentuknya berbeda dari yang sesungguhnya akan membuat test lulus terhadap
// sesuatu yang tidak pernah terjadi di produksi.
func deterministicAnswer(req Request) string {
	sum := sha256.Sum256([]byte(req.System + "\x1f" + req.Prompt))

	payload := map[string]any{
		"generated_by":   "fake",
		"prompt_version": req.PromptVersion,
		"digest":         hex.EncodeToString(sum[:]),
	}

	// Marshal map[string]any dengan kunci yang tetap tidak bisa gagal, tetapi
	// galatnya tetap tidak diabaikan: mengabaikannya berarti jawaban kosong
	// akan lolos sebagai jawaban yang sah.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("{%q:%q}", "error", err.Error())
	}
	return string(encoded)
}
