package domain

import "context"

// Page adalah permintaan halaman.
//
// Berbasis offset, bukan cursor. Percakapan seorang pengguna berjumlah puluhan,
// bukan jutaan, dan cursor menambah bentuk yang harus dijelaskan klien tanpa
// menghilangkan masalah yang belum ada.
type Page struct {
	Number int
	Size   int
}

// Normalise membatasi halaman ke rentang yang masuk akal.
//
// Ukuran yang tidak dibatasi membiarkan satu permintaan meminta seluruh
// riwayat, dan itu bukan pilihan pemanggil untuk diambil.
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

// ConversationRepository menyimpan percakapan dan pesannya.
type ConversationRepository interface {
	Create(ctx context.Context, c *Conversation) error

	// FindBySlug mencari lewat id publiknya.
	FindBySlug(ctx context.Context, slug string) (*Conversation, error)

	// ListForUser mengembalikan percakapan seorang pengguna, terbaru lebih
	// dulu, beserta jumlah seluruhnya.
	//
	// Jumlahnya ikut karena klien butuh tahu ada berapa halaman; menghitungnya
	// dengan memuat semuanya akan meniadakan gunanya berhalaman.
	ListForUser(ctx context.Context, userID UserID, page Page) (items []*Conversation, total int, err error)

	Update(ctx context.Context, c *Conversation) error

	// Delete menghapus percakapan beserta pesannya.
	//
	// Berantai lewat ON DELETE CASCADE di basis data, bukan dengan menghapus
	// satu per satu di Go: yang kedua meninggalkan sisa saat prosesnya mati di
	// tengah, dan sisa itu tidak akan pernah ditemukan siapa pun.
	Delete(ctx context.Context, id ID) error

	CreateMessage(ctx context.Context, m *Message) error

	// ListMessages membaca percakapan, terlama lebih dulu.
	//
	// Berhalaman untuk tampilan; jendela konteks memakai TailMessages.
	ListMessages(ctx context.Context, conversationID ID, page Page) (items []*Message, total int, err error)

	// TailMessages membaca sejumlah pesan TERAKHIR, terlama lebih dulu.
	//
	// Ia terpisah dari ListMessages karena pertanyaannya berbeda: yang satu
	// "halaman ke berapa", yang lain "apa yang baru saja dikatakan". Mengambil
	// halaman pertama sebagai konteks akan memberi model awal percakapan dan
	// melewatkan yang paling relevan (D8).
	TailMessages(ctx context.Context, conversationID ID, limit int) ([]*Message, error)
}
