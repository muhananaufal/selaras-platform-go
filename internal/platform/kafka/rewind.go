package kafka

import (
	"github.com/twmb/franz-go/pkg/kgo"
)

// Rewinder mengingat record yang gagal diproses dan memundurkan konsumen ke
// sana, supaya pesannya benar-benar dikirim ulang.
//
// Ia ada karena pola "gagal? jangan komit" TIDAK cukup, dan itu terbukti saat
// dijalankan.
//
// Yang terjadi tanpa ini: satu batch gagal, offsetnya tidak dikomit, konsumen
// terus memanggil PollFetches dan menerima batch BERIKUTNYA - franz-go tidak
// mengirim ulang apa pun di dalam sesi yang sama. Batch berikutnya berhasil,
// CommitUncommittedOffsets dipanggil, dan ia mengomit SELURUH yang sudah
// dikonsumsi - termasuk record yang gagal tadi. Record itu hilang selamanya,
// dan tidak ada satu pun galat yang muncul saat itu terjadi.
//
// Persis itu yang terjadi pada saga penghapusan akun: dasbor gagal menulis
// konfirmasinya, lalu lima konfirmasi dari unit lain lewat di topic yang sama,
// batch itu berhasil, dan offsetnya melompati permintaan penghapusan yang belum
// dikerjakan. Sagalnya menggantung tanpa siapa pun tahu sebabnya.
type Rewinder struct {
	// lowest menyimpan offset TERKECIL yang gagal per topic dan partisi.
	//
	// Terkecil, bukan terakhir: satu batch bisa memuat beberapa kegagalan, dan
	// memundurkan ke yang terakhir akan melewati yang sebelumnya.
	lowest map[string]map[int32]kgo.EpochOffset
}

func NewRewinder() *Rewinder {
	return &Rewinder{lowest: map[string]map[int32]kgo.EpochOffset{}}
}

// Failed mencatat satu record yang gagal diproses.
func (r *Rewinder) Failed(rec *kgo.Record) {
	if rec == nil {
		return
	}

	partitions, ok := r.lowest[rec.Topic]
	if !ok {
		partitions = map[int32]kgo.EpochOffset{}
		r.lowest[rec.Topic] = partitions
	}

	// Offset record itu SENDIRI, bukan satu sesudahnya.
	//
	// EpochOffset.Offset adalah tempat konsumen mulai membaca; satu sesudahnya
	// adalah yang dipakai saat mengomit. Memakai +1 di sini akan melewati
	// justru record yang ingin diulang.
	candidate := kgo.EpochOffset{Epoch: rec.LeaderEpoch, Offset: rec.Offset}

	if existing, seen := partitions[rec.Partition]; seen && existing.Less(candidate) {
		return
	}
	partitions[rec.Partition] = candidate
}

// Any menyatakan ada kegagalan yang tercatat.
func (r *Rewinder) Any() bool { return len(r.lowest) > 0 }

// Rewind memundurkan konsumen ke record gagal yang paling awal.
//
// Ia dipanggil SETELAH PollFetches selesai dan SEBELUM poll berikutnya, tanpa
// commit yang berjalan bersamaan - itu syarat pemakaian SetOffsets yang
// disebutkan franz-go, dan loop satu goroutine memenuhinya.
//
// Setelah dipanggil, catatannya dikosongkan: putaran berikutnya mencatat
// kegagalannya sendiri.
func (r *Rewinder) Rewind(client *kgo.Client) {
	if len(r.lowest) == 0 {
		return
	}
	client.SetOffsets(r.lowest)
	r.lowest = map[string]map[int32]kgo.EpochOffset{}
}
