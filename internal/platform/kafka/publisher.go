package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Message adalah satu pesan yang siap diterbitkan.
type Message struct {
	Topic string

	// Key menentukan partisi. Ia wajib: tanpa kunci, Kafka menyebar pesan ke
	// partisi mana pun dan urutan antar pesan satu agregat hilang.
	Key []byte

	Value []byte

	// Headers membawa metadata yang bisa dibaca konsumen tanpa membongkar
	// isinya - jenis event dan id-nya, untuk pencatatan dan penyaringan.
	Headers map[string]string
}

// Publisher menerbitkan pesan dan menunggu broker mengakuinya.
type Publisher struct {
	client *kgo.Client
}

func NewPublisher(client *kgo.Client) *Publisher { return &Publisher{client: client} }

// Publish menerbitkan sekumpulan pesan dan mengembalikan indeks yang berhasil.
//
// Ia mengembalikan keberhasilan PER PESAN, bukan satu boolean untuk seluruh
// batch. Bedanya nyata: kalau sebuah batch dinyatakan gagal seluruhnya padahal
// sebagian sudah diterima broker, percobaan berikutnya akan mengirim ulang
// bagian yang sudah berhasil - dan setiap kegagalan sementara berubah menjadi
// duplikat yang bisa dihindari.
//
// Menunggu (ProduceSync) juga disengaja. Menerbitkan tanpa menunggu berarti
// baris outbox ditandai terkirim berdasarkan harapan, dan outbox-nya kehilangan
// seluruh gunanya.
func (p *Publisher) Publish(ctx context.Context, msgs []Message) ([]int, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	records := make([]*kgo.Record, 0, len(msgs))
	index := make(map[*kgo.Record]int, len(msgs))
	for i, m := range msgs {
		if m.Topic == "" {
			return nil, errors.New("a message with no topic cannot be published")
		}
		if len(m.Key) == 0 {
			return nil, errors.New("a message with no key would lose its ordering")
		}

		rec := &kgo.Record{Topic: m.Topic, Key: m.Key, Value: m.Value}
		for k, v := range m.Headers {
			rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
		}
		index[rec] = i
		records = append(records, rec)
	}

	results := p.client.ProduceSync(ctx, records...)

	// Dipetakan balik lewat identitas pointer, BUKAN lewat indeks.
	//
	// ProduceSync mengumpulkan hasilnya dari promise yang selesai secara
	// asinkron [franz-go@v1.21.6/pkg/kgo/producer.go:359-366], sehingga
	// urutannya adalah urutan penyelesaian - bukan urutan pengiriman. Membaca
	// hasil ke-i sebagai hasil pesan ke-i akan menandai baris outbox yang salah
	// sebagai terkirim, dan yang benar-benar gagal justru hilang diam-diam.
	//
	// ProduceResult.Record dijamin non-nil [ibid.:316-322], jadi pemetaannya
	// selalu bisa dilakukan.
	var ok []int
	var firstErr error
	for _, res := range results {
		i, found := index[res.Record]
		if !found {
			// Tidak mungkin terjadi selama kontraknya dipegang. Kalau toh
			// terjadi, mendiamkannya berarti menandai baris terkirim tanpa
			// dasar.
			if firstErr == nil {
				firstErr = errors.New("the broker answered about a record that was never sent")
			}
			continue
		}
		if res.Err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("publishing to %s: %w", msgs[i].Topic, res.Err)
			}
			continue
		}
		ok = append(ok, i)
	}
	return ok, firstErr
}
