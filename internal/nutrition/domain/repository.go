package domain

import "context"

// Page adalah permintaan halaman riwayat panduan.
//
// Berbasis offset. Riwayat menu seseorang berjumlah puluhan sampai ratusan,
// bukan jutaan, dan cursor menambah bentuk yang harus dijelaskan klien tanpa
// menghilangkan masalah yang belum ada.
type Page struct {
	Number int
	Size   int
}

// Normalise membatasi halaman ke rentang yang masuk akal.
//
// Sistem lama mengembalikan SELURUH riwayat dalam satu panggilan, di-cache
// selamanya. Riwayat yang tumbuh tiap hari akan membuat satu respons hub
// membesar tanpa batas, dan yang membayarnya adalah pengguna paling setia.
func (p Page) Normalise() Page {
	if p.Number < 1 {
		p.Number = 1
	}
	if p.Size < 1 {
		p.Size = 20
	}
	if p.Size > 100 {
		p.Size = 100
	}
	return p
}

// Offset adalah jumlah baris yang dilewati.
func (p Page) Offset() int { return (p.Number - 1) * p.Size }

// PreferencesRepository menyimpan preferensi kuliner.
type PreferencesRepository interface {
	// FindByUser mengembalikan ErrPreferencesNotFound bila pengguna belum
	// pernah menyentuh preferensinya.
	//
	// Ketiadaan preferensi BUKAN kegagalan, dan pemanggilnya menanganinya
	// dengan membuat himpunan kosong. Galat terpisah dipakai supaya "belum ada"
	// tidak tersamar sebagai "kosong" - keduanya perlu dibedakan saat menulis:
	// yang satu INSERT, yang lain UPDATE.
	FindByUser(ctx context.Context, userID UserID) (*Preferences, error)

	Create(ctx context.Context, p *Preferences) error
	Update(ctx context.Context, p *Preferences) error
}

// GuideRepository menyimpan panduan menu harian.
type GuideRepository interface {
	Create(ctx context.Context, g *Guide) error

	FindByID(ctx context.Context, id ID) (*Guide, error)

	// ListForUser mengembalikan riwayat panduan, terbaru lebih dulu, beserta
	// jumlah seluruhnya.
	//
	// Jumlahnya ikut karena klien butuh tahu masih ada halaman berikutnya atau
	// tidak; menghitungnya dengan memuat semuanya akan meniadakan gunanya
	// berhalaman.
	ListForUser(ctx context.Context, userID UserID, page Page) (items []*Guide, total int, err error)

	// ListChosen mengembalikan panduan yang benar-benar DIPILIH pengguna,
	// terbaru lebih dulu, untuk riwayat pembelajaran.
	//
	// Ia menyaring chosen, bukan sekadar mengambil yang terakhir dibuat seperti
	// sistem lama (B17). Perbedaannya bukan gaya: mengambil yang terakhir
	// dibuat berarti menyuapkan kembali saran model kepada model sebagai
	// "menu yang disukai pengguna", padahal pengguna belum tentu memakannya.
	// Selama belum ada yang menandai, daftar ini memang kosong - dan kosong
	// adalah jawaban yang benar.
	ListChosen(ctx context.Context, userID UserID, limit int) ([]*Guide, error)

	Update(ctx context.Context, g *Guide) error
}
