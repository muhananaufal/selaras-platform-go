package domain

import (
	"context"
	"errors"
)

var (
	// ErrUserNotFound dikembalikan saat pencarian tidak menemukan siapa pun.
	// Pemanggil DILARANG meneruskannya apa adanya ke jawaban login: apakah
	// sebuah email terdaftar adalah informasi, dan membocorkannya mengubah
	// halaman masuk menjadi alat pencacahan akun.
	ErrUserNotFound = errors.New("user not found")

	// ErrEmailTaken berasal dari indeks unik, bukan dari pemeriksaan
	// pendahuluan. Membaca dulu lalu menulis akan lolos di antara dua
	// permintaan yang mendaftar bersamaan; basis data yang memutuskan.
	ErrEmailTaken = errors.New("email already registered")

	// ErrGoogleIDTaken sama asalnya: satu identitas Google hanya boleh
	// menunjuk ke satu akun.
	ErrGoogleIDTaken = errors.New("google id already linked to another account")
)

// UserRepository adalah port penyimpanan agregat User.
//
// Ia berbicara dalam tipe domain, bukan baris - domain tidak boleh tahu ada
// SQL di baliknya, dan mengganti penyimpanan tidak boleh menyentuh berkas
// mana pun di paket ini.
//
// Setiap metode menerima context supaya pembatalan permintaan benar-benar
// sampai ke kueri yang sedang berjalan, bukan berhenti di lapisan HTTP
// sementara basis data terus bekerja untuk jawaban yang tak akan dibaca.
type UserRepository interface {
	// Create menyimpan user baru. Email atau google id yang bentrok
	// menghasilkan ErrEmailTaken atau ErrGoogleIDTaken.
	Create(ctx context.Context, u *User) error

	// Update menyimpan perubahan pada user yang sudah ada.
	Update(ctx context.Context, u *User) error

	// Pencarian hanya mengembalikan akun yang hidup. Akun terhapus lunak
	// tidak ditemukan, karena satu-satunya alasan menyimpannya adalah audit,
	// bukan autentikasi.
	FindByID(ctx context.Context, id UserID) (*User, error)
	FindByEmail(ctx context.Context, email Email) (*User, error)
	FindByGoogleID(ctx context.Context, googleID string) (*User, error)

	// Delete menghapus akun secara PERMANEN.
	//
	// Dipanggil hanya di akhir saga penghapusan, setelah keenam unit
	// mengonfirmasi datanya benar-benar hilang. Ia bukan penghapusan lunak:
	// baris yang tertinggal setelah seseorang meminta akunnya dihapus adalah
	// data pribadi yang tidak seorang pun tahu masih ada.
	Delete(ctx context.Context, id UserID) error
}
