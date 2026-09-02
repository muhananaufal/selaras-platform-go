package domain

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrInvalidToken menutupi setiap alasan sebuah token ditolak: tanda
	// tangan keliru, kedaluwarsa, algoritma tak diizinkan, klaim tidak
	// lengkap. Pemanggil tidak boleh membedakannya di jawaban ke klien -
	// memberi tahu penyerang bahwa tanda tangannya benar tetapi sudah
	// kedaluwarsa adalah memberi tahu bahwa kuncinya bocor.
	ErrInvalidToken = errors.New("invalid token")

	// ErrTokenRevoked dipisahkan karena hanya dipakai di dalam sistem, untuk
	// memutuskan apakah pengguna perlu diminta masuk lagi.
	ErrTokenRevoked = errors.New("token revoked")
)

// Claims adalah isi token akses.
//
// UserProfileID ikut dibawa karena ADR-007: tanpanya, setiap unit yang butuh
// profil harus bertanya lebih dulu ke identity-svc, dan itu satu panggilan
// jaringan wajib di setiap request terautentikasi.
//
// Generation membawa generasi token pengguna saat diterbitkan. Ia yang
// membuat pencabutan mungkin tanpa menyimpan daftar token.
type Claims struct {
	UserID        UserID
	UserProfileID string
	Role          Role
	Generation    int64
	IssuedAt      time.Time
	ExpiresAt     time.Time
}

// TokenIssuer menandatangani klaim menjadi token akses. Implementasinya
// memegang kunci privat, dan hanya identity-svc yang boleh memilikinya.
type TokenIssuer interface {
	Issue(c Claims) (string, error)
}

// TokenVerifier memeriksa tanda tangan dan masa berlaku sebuah token.
//
// Ia sengaja TIDAK memeriksa pencabutan. Verifikasi tanda tangan bersifat
// murni dan bisa dilakukan siapa saja yang punya kunci publik; pemeriksaan
// pencabutan butuh keadaan bersama dan bisa gagal. Menggabungkan keduanya
// akan memaksa setiap pemakai membawa koneksi penyimpanan hanya untuk
// membaca sebuah klaim.
type TokenVerifier interface {
	Verify(raw string) (Claims, error)
}

// RevocationChecker menjawab apakah generasi yang dibawa token masih generasi
// yang berlaku bagi pengguna itu.
//
// Ini port terpisah, dan itu yang membuat ADR-012 tetap bermakna: token
// pembawa klaim menghapus panggilan ke identity-svc, sementara pemeriksaan
// pencabutan bisa dilayani penyimpanan bersama yang jauh lebih murah.
//
// Implementasinya WAJIB gagal-tertutup. Penyimpanan yang tidak bisa dihubungi
// berarti pencabutan tidak bisa dibuktikan, dan menerima token dalam keadaan
// itu mengubah setiap gangguan menjadi jendela di mana logout tidak berlaku.
type RevocationChecker interface {
	// IsCurrent benar bila generasi masih yang berlaku bagi pengguna itu.
	IsCurrent(ctx context.Context, userID UserID, generation int64) (bool, error)
}

// RevocationPublisher mengumumkan generasi baru seorang pengguna setelah
// perubahannya tersimpan.
type RevocationPublisher interface {
	PublishGeneration(ctx context.Context, userID UserID, generation int64) error
}
