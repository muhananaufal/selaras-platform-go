package kafka

import (
	"errors"
	"sort"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// RecoverRecreatedTopics melanggani ulang topic yang dibuat ulang di broker.
//
// Topic Kafka punya id di samping namanya. Bila sebuah topic dihapus lalu
// dibuat lagi dengan nama yang sama (broker kehilangan datanya - B23 - lalu
// `topics` membuatnya kembali), klien yang masih memegang id lama menerima
// UNKNOWN_TOPIC_ID pada SETIAP fetch. franz-go dengan sengaja tidak pulih
// dari sini: setelah lima kegagalan beruntun ia membiarkan galatnya muncul
// selamanya, "stall loudly" [franz-go@v1.21.6/pkg/kgo/source.go:1335-1352].
// Tanpa penanganan, satu-satunya jalan keluar adalah restart proses - dan
// itulah yang terjadi dua kali di F9 (B26).
//
// Pemulihannya mengikuti dokumentasi klien: PurgeTopicsFromClient membuang
// seluruh pengetahuan tentang topic itu (termasuk id lamanya), lalu
// AddConsumeTopics melanggani lagi dengan metadata yang segar
// [franz-go@v1.21.6/pkg/kgo/client.go:680-693, consumer.go:872-880].
// Pada consumer group ini memicu rebalance; itu harga yang jauh lebih murah
// daripada restart.
//
// Yang TIDAK dipulihkan: offset yang sudah dikomit untuk nama topic itu.
// Topic baru mulai dari nol, sementara group mengingat offset topic lama;
// record di bawah offset lama pada topic baru dilewati. Restart pun tidak
// mengubah itu, dan skenario ini memang bukan skenario produksi - tujuannya
// adalah konsumen yang kembali membaca record BARU tanpa campur tangan.
//
// Dikembalikan: nama topic yang dilanggani ulang, terurut; nil bila tidak ada
// galat yang berkaitan dengan topic yang dibuat ulang.
func RecoverRecreatedTopics(client *kgo.Client, errs []kgo.FetchError) []string {
	seen := map[string]struct{}{}
	for _, e := range errs {
		if errors.Is(e.Err, kerr.UnknownTopicID) && e.Topic != "" {
			seen[e.Topic] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}

	topics := make([]string, 0, len(seen))
	for t := range seen {
		topics = append(topics, t)
	}
	sort.Strings(topics)

	client.PurgeTopicsFromClient(topics...)
	client.AddConsumeTopics(topics...)
	return topics
}

// ForgetRecreatedTopic adalah sisi PRODUSER dari pemulihan yang sama.
//
// Klien yang pernah memproduksi ke sebuah topic menyimpan id-nya; setelah
// topic dibuat ulang, setiap produce gagal UNKNOWN_TOPIC_ID sampai metadata
// topic itu dibuang - dan franz-go tidak membuangnya sendiri. Relay outbox
// memanggil ini pada galat itu supaya tick berikutnya menyambung ke topic
// yang baru; tanpa ini relay mengulang galat yang sama setiap detik,
// selamanya, seperti yang terlihat di B28.
//
// true bila galatnya memang UNKNOWN_TOPIC_ID dan topic-nya dilupakan.
func ForgetRecreatedTopic(client *kgo.Client, topic string, err error) bool {
	if topic == "" || !errors.Is(err, kerr.UnknownTopicID) {
		return false
	}
	client.PurgeTopicsFromClient(topic)
	return true
}
