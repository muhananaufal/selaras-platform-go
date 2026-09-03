package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Topic adalah satu topic beserta alasan jumlah partisinya.
//
// Alasannya ikut di sini, bukan hanya di dokumen, karena jumlah partisi adalah
// batas atas paralelisme konsumen (ADR-014 aturan 1) dan menaikkannya kemudian
// akan mengubah pemetaan kunci ke partisi - artinya urutan per kunci yang sudah
// berjalan patah di titik perubahan.
type Topic struct {
	Name       string
	Partitions int32
	Why        string
}

// Topics adalah seluruh topic platform.
//
// Ia daftar tunggal: alat pembuat topic dan dokumennya membaca yang sama,
// sehingga dokumen tidak bisa menyimpang dari yang benar-benar dibuat.
func Topics() []Topic {
	return []Topic{
		{
			Name:       "profile.updated",
			Partitions: 3,
			Why: "Konsumennya penulis cache (F2-16) - pekerjaan pendek, terikat basis data. " +
				"Tiga cukup untuk memisahkan pengguna yang sibuk dari yang lain tanpa " +
				"menyebar beban yang belum ada.",
		},
		{
			Name:       "assessment.completed",
			Partitions: 3,
			Why: "Pemicu personalisasi. Lajunya terikat pada berapa banyak penilaian yang " +
				"diselesaikan orang, bukan pada kecepatan mesin - dan itu angka yang kecil.",
		},
		{
			Name:       "coaching.program.updated",
			Partitions: 3,
			Why: "Perubahan program coaching. Selajur dengan profile.updated - dibaca " +
				"pembaca yang sama dan lajunya ditentukan orang, bukan mesin.",
		},
		{
			Name:       "llm.jobs",
			Partitions: 12,
			Why: "Satu-satunya topic yang butuh paralelisme sungguhan. Pekerjaannya menunggu " +
				"jaringan selama puluhan detik, jadi jumlah partisi menentukan berapa banyak " +
				"pekerjaan yang boleh menunggu bersamaan. Dua belas adalah plafon " +
				"maxReplicaCount llm-worker di F9-22; menaikkannya nanti mudah, " +
				"menurunkannya tidak.",
		},
		{
			Name:       "llm.results",
			Partitions: 12,
			Why: "Dipasangkan dengan llm.jobs supaya satu worker bisa memegang partisi yang " +
				"bersesuaian di keduanya. Jumlah yang berbeda akan membuat hasil sebuah job " +
				"mendarat di partisi yang dipegang worker lain.",
		},
		{
			Name:       "llm.dlq",
			Partitions: 1,
			Why: "Antrean surat mati (F3-13). Ia dibaca manusia saat menyelidiki, bukan oleh " +
				"armada konsumen. Satu partisi menjaga urutannya utuh - dan kalau ia sampai " +
				"butuh lebih, yang salah bukan jumlah partisinya.",
		},
		{
			Name:       "user.deletion",
			Partitions: 1,
			Why: "Penghapusan akun harus berurutan terhadap dirinya sendiri dan jarang terjadi. " +
				"Paralelisme di sini hanya menambah cara untuk salah.",
		},
	}
}

// EnsureTopics membuat topic yang belum ada.
//
// Topic yang sudah ada DIBIARKAN, tidak diubah dan tidak dihapus. Menaikkan
// jumlah partisi sebuah topic yang berisi akan mengubah pemetaan kunci ke
// partisi: pesan untuk kunci yang sama tiba-tiba mendarat di tempat lain, dan
// urutan yang selama ini terjaga patah tanpa satu pun galat.
func EnsureTopics(ctx context.Context, client *kgo.Client, topics []Topic, replicas int16) ([]string, error) {
	if len(topics) == 0 {
		return nil, errors.New("no topics were given")
	}
	if replicas < 1 {
		return nil, errors.New("a topic needs at least one replica")
	}

	admin := kadm.NewClient(client)

	var created []string
	for _, t := range topics {
		// kadm.CreateTopic mengembalikan penolakan broker lewat err, bukan hanya
		// lewat resp.Err [kadm@v1.18.0/topics.go:139]. Memeriksa resp.Err saja
		// membuat cabang "sudah ada" tidak pernah tercapai, dan alat ini gagal
		// pada jalankan kedua - persis yang terjadi sebelum baris ini ditulis.
		_, err := admin.CreateTopic(ctx, t.Partitions, replicas, nil, t.Name)
		switch {
		case err == nil:
			created = append(created, t.Name)
		case errors.Is(err, kerr.TopicAlreadyExists):
			// Keadaan yang benar, bukan kegagalan: alat ini harus bisa
			// dijalankan berkali-kali.
		default:
			return created, fmt.Errorf("creating %q: %w", t.Name, err)
		}
	}
	return created, nil
}

// DescribeTopics membaca kembali apa yang benar-benar ada di broker.
//
// Ia dipakai untuk membuktikan, bukan untuk mengasumsikan: membuat topic lalu
// melapor sukses tanpa membacanya kembali akan menyembunyikan broker yang
// menerima permintaannya lalu diam-diam membuat sesuatu yang lain.
func DescribeTopics(ctx context.Context, client *kgo.Client) (map[string]int, error) {
	admin := kadm.NewClient(client)

	details, err := admin.ListTopics(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing topics: %w", err)
	}

	out := make(map[string]int, len(details))
	for name, d := range details {
		if d.Err != nil {
			return nil, fmt.Errorf("the broker reported %q as broken: %w", name, d.Err)
		}
		out[name] = len(d.Partitions)
	}
	return out, nil
}

// WaitForTopics menunggu sampai broker benar-benar mengumumkan topic-nya.
//
// CreateTopic yang berhasil TIDAK berarti topic itu langsung muncul di
// metadata: broker menyebarkannya secara asinkron, dan pembacaan yang
// dilakukan seketika setelahnya akan melaporkan topic yang baru saja berhasil
// dibuat sebagai tidak ada. Sekali ini benar-benar terjadi di sini - empat dari
// enam topic "hilang" pada pembacaan pertama.
//
// Menunggu di sini lebih jujur daripada memperlonggar pemeriksaannya, karena
// yang ingin dibuktikan tetap sama: topic itu ada, dengan jumlah partisi yang
// diminta.
func WaitForTopics(ctx context.Context, client *kgo.Client, topics []Topic) (map[string]int, error) {
	const interval = 250 * time.Millisecond

	var last map[string]int
	for {
		found, err := DescribeTopics(ctx, client)
		if err != nil {
			return nil, err
		}
		last = found

		settled := true
		for _, t := range topics {
			if got, ok := found[t.Name]; !ok || got != int(t.Partitions) {
				settled = false
				break
			}
		}
		if settled {
			return found, nil
		}

		select {
		case <-ctx.Done():
			// Deadline habis. Yang terakhir terbaca tetap dikembalikan supaya
			// pemanggil bisa menyebutkan topic mana yang tidak pernah muncul.
			return last, fmt.Errorf("the broker never announced every topic: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}
