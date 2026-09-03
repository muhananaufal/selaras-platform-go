package domain

import (
	"context"
	"errors"
	"time"
)

// ErrNoDashboard berarti pengguna itu belum punya baris proyeksi sama sekali.
//
// Ia BUKAN kesalahan: pengguna yang baru mendaftar belum menghasilkan satu
// event pun. Pemanggilnya menjawabnya dengan dasbor kosong, bukan dengan 404 -
// halaman yang menyambut pengguna baru tidak boleh terlihat rusak.
var ErrNoDashboard = errors.New("no dashboard has been projected for this user")

// Repository membaca dan menulis read-model.
//
// Ia sengaja tidak punya Create dan Update terpisah. Proyeksi menerima event
// dalam urutan yang tidak dijamin, dan setiap penulisannya harus berlaku baik
// barisnya sudah ada maupun belum - dua metode berarti pemanggil harus tahu
// yang mana, dan tebakan yang salah menjatuhkan proyeksi.
type Repository interface {
	// Find mengembalikan ErrNoDashboard bila belum ada barisnya.
	Find(ctx context.Context, userID UserID) (*Dashboard, error)

	// ApplyAssessment memasukkan satu penilaian ke dalam proyeksi.
	//
	// IDEMPOTEN terhadap slug: penilaian yang sama diterapkan dua kali
	// menghasilkan baris yang sama, bukan dua baris riwayat dan bukan pula
	// jumlah yang bertambah dua (F7-03). Event bisa tiba dua kali - relay
	// outbox at-least-once - dan yang kedua tidak boleh menggeser apa pun.
	ApplyAssessment(ctx context.Context, userID UserID, a *Assessment, occurredAt time.Time) error

	// ApplyProgram menyalin keadaan program coaching.
	//
	// completion nil berarti event ini tidak membawanya, dan angka yang sudah
	// tersimpan DIBIARKAN. Menulis nol untuk "tidak dibawa" membuat dasbor
	// melompat kembali ke nol persen setiap kali program dijeda.
	ApplyProgram(ctx context.Context, userID UserID, p *Program, occurredAt time.Time) error

	// Forget menghapus proyeksi seorang pengguna, untuk saga penghapusan akun.
	Forget(ctx context.Context, userID UserID) error
}

// ProjectionState adalah posisi sebuah proyeksi.
type ProjectionState struct {
	Name          string
	LastEventAt   time.Time
	EventsApplied int64
	UpdatedAt     time.Time
}

// StateRepository menyimpan posisi proyeksi.
//
// Ia BUKAN pengganti offset Kafka - itu tetap milik consumer group. Yang di
// sini menjawab pertanyaan yang berbeda: "sampai peristiwa kapan proyeksi ini
// sudah dibangun", yang dipakai perintah rebuild untuk menyatakan hasilnya
// lengkap dan dipakai pengukuran lag untuk mengetahui seberapa jauh
// tertinggalnya.
type StateRepository interface {
	Get(ctx context.Context, name string) (ProjectionState, error)
	Advance(ctx context.Context, name string, eventAt time.Time) error
	Reset(ctx context.Context, name string) error
}
