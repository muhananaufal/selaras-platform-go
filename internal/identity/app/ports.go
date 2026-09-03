// Package app memuat use case identity: alur yang mengubah keadaan, ditulis
// dalam tipe domain dan port, tanpa satu pun detail transport atau basis data.
package app

import (
	"context"
	"errors"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ErrPasswordMismatch terjadi saat konfirmasi tidak sama dengan kata sandi.
var ErrPasswordMismatch = errors.New("password confirmation does not match")

// Repositories adalah kumpulan penyimpanan yang dilihat sebuah use case di
// dalam satu satuan kerja.
//
// Ia dikumpulkan menjadi satu antarmuka, bukan diserahkan satu per satu,
// karena alur seperti reset kata sandi menulis ke dua tempat sekaligus:
// kata sandi pengguna, dan penandaan token yang baru saja dipakai. Bila
// keduanya berada di transaksi yang berbeda, ada celah di mana kata sandi
// sudah berganti sementara tokennya masih bisa dipakai lagi.
type Repositories interface {
	Users() domain.UserRepository
	PasswordResets() domain.PasswordResetRepository

	// Sagas dan Events dipakai penghapusan akun.
	//
	// Keduanya berada di satuan kerja yang SAMA dengan penulisan sagalnya:
	// saga yang tercatat tanpa eventnya menggantung selamanya menunggu enam
	// unit yang tidak pernah diberi tahu, dan event tanpa sagalnya menghapus
	// data seseorang tanpa satu pun catatan bahwa itu diminta.
	Sagas() SagaRepository
	Events() EventWriter
}

// EventWriter menulis event ke outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// UnitOfWork menjalankan beberapa tulisan sebagai satu satuan.
//
// Ia ada di sini, bukan di adapter, supaya use case bisa menuntut atomicity
// tanpa tahu bahwa di baliknya ada transaksi Postgres. Nanti, saat outbox
// masuk (F3-03), baris event ditulis lewat satuan yang sama - dan use
// case-nya tidak perlu berubah.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// ProfileCreator meminta profile-svc membuat profil kosong bagi pengguna baru.
//
// ADR-002 aturan 1: panggilan ini best-effort. Kegagalannya DILARANG
// menggagalkan registrasi - pengguna tanpa profil adalah keadaan yang memang
// sudah sah hari ini (B7), dan `user.registered` yang merekonsiliasinya
// belakangan.
type ProfileCreator interface {
	// CreateEmptyProfile mengembalikan id profil yang baru dibuat.
	CreateEmptyProfile(ctx context.Context, userID domain.UserID) (string, error)
}

// AuthResult adalah yang dibawa pulang oleh register dan login.
type AuthResult struct {
	UserID        string
	UserProfileID string
	AccessToken   string
}

// ProfileFinder mengambil id profil seorang pengguna dari profile-svc.
//
// ADR-002 aturan 2: dipanggil sekali per login, bukan sekali per request.
// Profil yang belum ada menghasilkan string kosong tanpa galat - itu keadaan
// yang sah (B7), dan setiap konsumen klaim wajib menanganinya.
type ProfileFinder interface {
	FindProfileID(ctx context.Context, userID domain.UserID) (string, error)
}
