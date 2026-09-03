package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Publisher adalah yang dibutuhkan relay dari broker, dan tidak lebih.
//
// Ia antarmuka supaya relay bisa diuji tanpa broker, dan supaya kegagalan
// penerbitan bisa dibuat terjadi sesuai kehendak - yang di broker sungguhan
// justru sulit dilakukan.
type Publisher interface {
	// Publish mengembalikan indeks pesan yang berhasil diterbitkan.
	//
	// Per pesan, bukan satu keputusan untuk seluruh batch: menyatakan batch
	// gagal seluruhnya padahal sebagian sudah diterima broker akan mengirim
	// ulang bagian yang berhasil, dan mengubah setiap kegagalan sementara
	// menjadi duplikat yang bisa dihindari.
	Publish(ctx context.Context, msgs []kafka.Message) ([]int, error)
}

// RelayOptions mengatur ritme relay.
type RelayOptions struct {
	// Batch adalah jumlah maksimum event yang diambil sekali putaran.
	Batch int

	// Interval adalah jeda saat outbox kosong. Saat masih ada isinya, relay
	// langsung berputar lagi - menunggu di sana hanya menambah keterlambatan
	// pada pekerjaan yang sudah menunggu.
	Interval time.Duration

	// PublishTimeout membatasi berapa lama satu penerbitan boleh menunggu.
	//
	// Ia ada karena klien Kafka menyangga dan mencoba ulang di dalam: dengan
	// broker yang mati, ProduceSync tidak mengembalikan galat sampai batas
	// coba ulangnya sendiri habis - puluhan detik - dan selama itu relay
	// tergantung tanpa mencatat apa pun. Baris outbox-nya diam di sana dengan
	// attempts nol dan last_error kosong, dan orang yang menyelidikinya tidak
	// menemukan penjelasan apa pun.
	//
	// Ini benar-benar terjadi: test ketahanan F3-14 menemukannya dengan
	// mematikan broker sungguhan.
	PublishTimeout time.Duration
}

// Relay memindahkan event dari outbox ke broker.
//
// Jaminannya AT-LEAST-ONCE, dan itu bukan kompromi yang bisa dihindari.
// Menerbitkan ke broker dan menandai baris terkirim adalah dua sistem yang
// berbeda; salah satu dari keduanya harus terjadi lebih dulu:
//
//   - Menandai lebih dulu, lalu menerbitkan: proses yang mati di antaranya
//     kehilangan event itu SELAMANYA. Tidak ada yang akan mencarinya lagi.
//   - Menerbitkan lebih dulu, lalu menandai: proses yang mati di antaranya
//     menerbitkannya lagi saat hidup kembali. Duplikat, bukan kehilangan.
//
// Yang kedua yang dipilih. Duplikat bisa ditangani penerimanya lewat kunci
// idempotensi (F3-05); kehilangan tidak bisa ditangani siapa pun.
type Relay struct {
	pool pg.Beginner
	pub  Publisher
	log  *slog.Logger
	opts RelayOptions
}

func NewRelay(pool pg.Beginner, pub Publisher, log *slog.Logger, opts RelayOptions) (*Relay, error) {
	if pool == nil {
		return nil, errors.New("nil pool")
	}
	if pub == nil {
		return nil, errors.New("nil publisher")
	}
	if log == nil {
		return nil, errors.New("nil logger")
	}
	if opts.Batch <= 0 {
		opts.Batch = 100
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.PublishTimeout <= 0 {
		opts.PublishTimeout = 10 * time.Second
	}
	return &Relay{pool: pool, pub: pub, log: log, opts: opts}, nil
}

// Run berputar sampai ctx selesai.
//
// Ia mengembalikan nil saat dihentikan lewat ctx: penghentian yang diminta
// bukan kegagalan, dan melaporkannya sebagai galat akan membuat setiap
// shutdown yang rapi terlihat seperti kerusakan.
func (r *Relay) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "outbox relay started",
		"batch", r.opts.Batch, "interval", r.opts.Interval)

	for {
		moved, err := r.Once(ctx)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			r.log.InfoContext(ctx, "outbox relay stopped")
			return nil
		case err != nil:
			// Satu putaran yang gagal tidak mematikan relay. Basis data yang
			// sedang restart atau broker yang sedang memilih leader adalah
			// keadaan sementara, dan relay yang mati karenanya meninggalkan
			// outbox yang menumpuk tanpa ada yang mengurusnya.
			r.log.ErrorContext(ctx, "outbox relay round failed", "error", err)
		}

		// Masih ada isinya: berputar lagi tanpa jeda.
		if err == nil && moved >= r.opts.Batch {
			continue
		}

		select {
		case <-ctx.Done():
			r.log.InfoContext(ctx, "outbox relay stopped")
			return nil
		case <-time.After(r.opts.Interval):
		}
	}
}

// Once menjalankan satu putaran dan mengembalikan jumlah event yang terkirim.
//
// Seluruhnya di dalam SATU transaksi. Kunci FOR UPDATE SKIP LOCKED yang
// diambil saat membaca hanya bertahan selama transaksinya, jadi membaca di satu
// transaksi lalu menandai di transaksi lain akan melepaskan kuncinya di antara
// keduanya - dan relay kedua akan mengambil event yang sedang dikirim relay
// pertama.
func (r *Relay) Once(ctx context.Context) (int, error) {
	var moved int

	err := pg.InTx(ctx, r.pool, func(q pg.Querier) error {
		reader := NewReader(q)

		records, err := reader.Unpublished(ctx, r.opts.Batch)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}

		msgs, routable, unroutable := r.toMessages(records)

		// Event yang tidak bisa dirutekan tidak akan pernah bisa dikirim.
		// Menaikkan penghitungnya membuatnya bisa ditemukan; membiarkannya
		// membuat relay membacanya lagi setiap putaran, selamanya.
		if len(unroutable) > 0 {
			if err := reader.MarkFailed(ctx, unroutable, "no topic is defined for this event type"); err != nil {
				return err
			}
		}

		if len(msgs) == 0 {
			return nil
		}

		// Penerbitannya dibatasi waktu, TERPISAH dari transaksinya. Tanpa
		// batas ini, satu putaran bisa tergantung selama klien Kafka menyangga
		// dan mencoba ulang di dalam - dan kegagalannya tidak pernah tercatat.
		pubCtx, cancelPub := context.WithTimeout(ctx, r.opts.PublishTimeout)
		sent, pubErr := r.pub.Publish(pubCtx, msgs)
		cancelPub()

		published := make([]uuid.UUID, 0, len(sent))
		for _, i := range sent {
			published = append(published, routable[i])
		}
		if err := reader.MarkPublished(ctx, published, time.Now()); err != nil {
			return err
		}
		moved = len(published)

		if pubErr != nil {
			// Yang gagal dicatat, lalu galatnya DITELAN di sini dengan sengaja:
			// mengembalikannya akan membatalkan transaksi ini, dan bersamanya
			// tanda terkirim untuk event yang benar-benar sampai. Event itu
			// akan dikirim ulang tanpa alasan.
			failed := make([]uuid.UUID, 0, len(routable)-len(published))
			ok := make(map[int]bool, len(sent))
			for _, i := range sent {
				ok[i] = true
			}
			for i, id := range routable {
				if !ok[i] {
					failed = append(failed, id)
				}
			}
			if err := reader.MarkFailed(ctx, failed, pubErr.Error()); err != nil {
				return err
			}
			r.log.WarnContext(ctx, "some outbox events could not be published",
				"published", len(published), "failed", len(failed), "error", pubErr)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("relaying a batch: %w", err)
	}
	return moved, nil
}

// toMessages memetakan baris outbox ke pesan Kafka.
//
// Ia mengembalikan tiga hal: pesannya, id baris yang bersesuaian menurut indeks,
// dan id baris yang tidak bisa dirutekan sama sekali.
func (r *Relay) toMessages(records []Record) (msgs []kafka.Message, routable, unroutable []uuid.UUID) {
	for _, rec := range records {
		topic, err := TopicFor(rec.EventType)
		if err != nil {
			unroutable = append(unroutable, rec.ID)
			continue
		}

		msgs = append(msgs, kafka.Message{
			Topic: topic,

			// Kuncinya aggregate_id, sehingga seluruh event satu agregat
			// mendarat di partisi yang sama dan urutannya terjaga.
			Key:   []byte(rec.AggregateID),
			Value: rec.Payload,
			Headers: map[string]string{
				"event_type":     rec.EventType,
				"aggregate_type": rec.AggregateType,
				"outbox_id":      rec.ID.String(),
			},
		})
		routable = append(routable, rec.ID)
	}
	return msgs, routable, unroutable
}
