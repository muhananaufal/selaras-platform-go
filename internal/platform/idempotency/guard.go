// Package idempotency membuat pekerjaan yang datang dua kali hanya terjadi
// sekali.
//
// Ia pasangan wajib dari relay outbox. Relay itu at-least-once dengan sengaja:
// menandai baris terkirim sebelum broker mengakuinya akan kehilangan event
// selamanya, jadi ia memilih mengirim ulang. Duplikat yang timbul harus
// dihentikan di sisi penerima, dan di sinilah ia dihentikan.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ErrAlreadyProcessed dikembalikan saat kuncinya sudah pernah dipakai.
var ErrAlreadyProcessed = errors.New("this work has already been done")

// Guard menjaga satu kunci hanya dikerjakan sekali.
//
// Ia menerima Querier, bukan kolam koneksi, karena klaim dan pekerjaannya WAJIB
// commit bersama. Klaim yang commit sendiri lalu pekerjaannya gagal akan
// meninggalkan kunci yang tercatat selesai untuk pekerjaan yang tidak pernah
// terjadi - dan tidak ada percobaan ulang yang bisa memperbaikinya.
type Guard struct {
	db    pg.Querier
	scope string
}

// NewGuard membuat penjaga untuk satu ruang lingkup.
//
// scope memisahkan konsumen yang berbeda. Tanpa itu, penulis cache yang sudah
// menangani sebuah event akan membuat pengirim notifikasi mengira dirinya juga
// sudah - dan notifikasinya tidak pernah terkirim.
func NewGuard(db pg.Querier, scope string) (*Guard, error) {
	if db == nil {
		return nil, errors.New("nil querier")
	}
	if scope == "" {
		return nil, errors.New("a guard without a scope would let one consumer silence another")
	}
	return &Guard{db: db, scope: scope}, nil
}

// Claim mencoba mengklaim sebuah kunci.
//
// Ia mengembalikan true kalau kunci itu baru, dan false kalau sudah pernah
// dipakai. Keputusannya datang dari kunci primer basis data lewat
// ON CONFLICT DO NOTHING - satu pernyataan, bukan SELECT lalu INSERT. Yang
// kedua punya celah di antara keduanya, dan dua proses yang masuk bersamaan
// akan sama-sama membaca "belum" lalu sama-sama mengerjakannya.
func (g *Guard) Claim(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, errors.New("an empty idempotency key would collapse every job into one")
	}

	const q = `
		INSERT INTO processed_messages (key, scope, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO NOTHING`

	tag, err := g.db.Exec(ctx, q, g.scopedKey(key), g.scope, time.Now())
	if err != nil {
		return false, fmt.Errorf("claiming the idempotency key: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// SaveResult menyimpan hasil pekerjaan yang kuncinya sudah diklaim.
//
// Ia dipanggil di transaksi yang sama dengan Claim. Menyimpannya di transaksi
// lain berarti ada saat di mana kuncinya tercatat selesai tetapi hasilnya belum
// ada - dan permintaan ulang di saat itu dijawab "sudah pernah" tanpa jawaban.
func (g *Guard) SaveResult(ctx context.Context, key string, result []byte) error {
	if key == "" {
		return errors.New("empty idempotency key")
	}

	const q = `UPDATE processed_messages SET result = $2 WHERE key = $1`
	tag, err := g.db.Exec(ctx, q, g.scopedKey(key), result)
	if err != nil {
		return fmt.Errorf("saving the result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Menyimpan hasil untuk kunci yang belum diklaim berarti urutannya
		// terbalik di pemanggil. Mendiamkannya akan membuang hasilnya diam-diam.
		return fmt.Errorf("no claim exists for key %q", key)
	}
	return nil
}

// Result mengambil hasil yang tersimpan.
//
// found bernilai false kalau kuncinya belum pernah diklaim. Kunci yang sudah
// diklaim tetapi hasilnya belum tersimpan mengembalikan found true dengan
// result nil - dua keadaan yang berbeda, dan membedakannya penting: yang
// pertama berarti "kerjakan", yang kedua berarti "sedang atau sudah dikerjakan
// tanpa hasil yang disimpan".
func (g *Guard) Result(ctx context.Context, key string) (result []byte, found bool, err error) {
	if key == "" {
		return nil, false, errors.New("empty idempotency key")
	}

	const q = `SELECT result FROM processed_messages WHERE key = $1`

	err = g.db.QueryRow(ctx, q, g.scopedKey(key)).Scan(&result)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("reading the stored result: %w", err)
	}
	return result, true, nil
}

// scopedKey menggabungkan ruang lingkup ke dalam kuncinya.
//
// Pemisahnya "\x1f" (unit separator), bukan karakter yang bisa muncul di dalam
// scope atau key. Dengan pemisah biasa seperti ":", scope "a" + key "b:c" dan
// scope "a:b" + key "c" akan menghasilkan kunci yang sama - dan dua pekerjaan
// yang tidak berhubungan saling meniadakan.
func (g *Guard) scopedKey(key string) string {
	return g.scope + "\x1f" + key
}

// Sweep menghapus catatan yang lebih tua dari usia yang diberikan.
//
// Tabel ini tidak dipartisi (lihat schema.sql), jadi pertumbuhannya ditangani
// di sini. Usianya harus lebih panjang dari jendela percobaan ulang mana pun
// di sistem: menyapu terlalu cepat akan membuat pesan lama yang datang
// terlambat dikerjakan untuk kedua kalinya.
func Sweep(ctx context.Context, db pg.Querier, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, errors.New("sweeping with no age limit would erase every claim")
	}

	const q = `DELETE FROM processed_messages WHERE created_at < $1`
	tag, err := db.Exec(ctx, q, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("sweeping processed messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Release menghapus klaim sebuah kunci.
//
// Ia SATU-SATUNYA cara pekerjaan yang gagal bisa dicoba lagi. Tanpa ini, klaim
// yang sudah diambil menutup kuncinya selamanya: pengiriman berikutnya
// dilewati sebagai duplikat, dan pekerjaan yang gagal sekali tidak akan pernah
// dikerjakan lagi oleh siapa pun.
//
// Ia berbahaya kalau dipanggil di tempat yang salah, dan bahayanya persis
// kebalikan dari gunanya: melepas klaim pekerjaan yang BERHASIL berarti
// pekerjaan itu akan dikerjakan dua kali. Ia hanya boleh dipanggil pada jalur
// kegagalan yang memang akan diulang, di dalam transaksi yang sama dengan
// pencatatan kegagalannya.
func (g *Guard) Release(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("empty idempotency key")
	}

	const q = `DELETE FROM processed_messages WHERE key = $1`
	if _, err := g.db.Exec(ctx, q, g.scopedKey(key)); err != nil {
		return fmt.Errorf("releasing the idempotency key: %w", err)
	}
	return nil
}
