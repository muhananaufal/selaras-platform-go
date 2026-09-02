// Package outbox menulis event bersama perubahan bisnisnya, dalam satu
// transaksi.
//
// Inilah yang membuat "kirim event setelah menyimpan" berhenti menjadi
// harapan. Menulis ke basis data lalu menerbitkan ke broker adalah dua
// tindakan yang bisa gagal terpisah: proses yang mati di antaranya
// meninggalkan perubahan yang tersimpan dan event yang tidak pernah ada, dan
// tidak ada yang tahu sampai seseorang bertanya mengapa dashboard-nya tidak
// pernah berubah.
//
// Dengan outbox, event ditulis ke tabel yang sama dalam transaksi yang sama.
// Ia terkirim atau tidak sama sekali bersama perubahannya, dan relay yang
// terpisah yang memindahkannya ke broker - berkali-kali kalau perlu.
package outbox

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Schema adalah DDL tabel outbox, dipakai generator migrasi per service.
//
//go:embed schema.sql
var Schema string

// Record adalah satu baris outbox yang sudah dibaca kembali.
type Record struct {
	ID            uuid.UUID
	CreatedAt     time.Time
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       []byte
	Attempts      int
}

// Writer menulis event ke outbox.
//
// Ia menerima Querier, bukan kolam koneksi, dan itu bukan detail: satu-satunya
// cara outbox bermakna adalah kalau ia menulis lewat transaksi yang sama
// dengan perubahan bisnisnya.
type Writer struct {
	db pg.Querier
}

func NewWriter(db pg.Querier) *Writer { return &Writer{db: db} }

// Write menyimpan satu event.
//
// aggregateID menjadi kunci partisi Kafka nanti. Ia wajib: tanpa kunci, Kafka
// menyebar event ke partisi mana pun dan urutan antar event satu agregat
// hilang - "profil diperbarui" bisa tiba setelah "profil dihapus".
func (w *Writer) Write(
	ctx context.Context,
	aggregateType, aggregateID string,
	envelope *eventsv1.Envelope,
) error {
	if aggregateType == "" || aggregateID == "" {
		return errors.New("an outbox row needs an aggregate to key on")
	}
	if envelope == nil {
		return errors.New("nil envelope")
	}

	eventType := eventTypeOf(envelope)
	if eventType == "" {
		// Envelope tanpa payload tidak bisa dirutekan ke topic mana pun.
		// Menyimpannya berarti relay akan menemukannya, gagal, dan mencoba
		// lagi selamanya.
		return errors.New("the envelope carries no event")
	}

	payload, err := proto.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encoding the envelope: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generating an outbox id: %w", err)
	}

	const q = `
		INSERT INTO outbox (id, created_at, aggregate_type, aggregate_id, event_type, payload)
		VALUES ($1, $2, $3, $4, $5, $6)`

	// created_at diberikan eksplisit, bukan diserahkan ke now() basis data.
	// Ia kunci partisi, dan waktu yang datang dari satu tempat lebih mudah
	// dijelaskan daripada waktu yang bergantung pada jam server mana yang
	// kebetulan menjalankan kuerinya.
	if _, err := w.db.Exec(ctx, q,
		id, envelope.GetOccurredAt().AsTime(), aggregateType, aggregateID, eventType, payload,
	); err != nil {
		return fmt.Errorf("writing to the outbox: %w", err)
	}
	return nil
}

// Reader membaca event yang belum terkirim, untuk relay.
type Reader struct {
	db pg.Querier
}

func NewReader(db pg.Querier) *Reader { return &Reader{db: db} }

// Unpublished mengambil sekumpulan event yang belum terkirim.
//
// FOR UPDATE SKIP LOCKED, dan keduanya diperlukan: FOR UPDATE menahan baris
// yang sedang dikirim sebuah relay, SKIP LOCKED membuat relay lain melewatinya
// alih-alih menunggu. Tanpa yang kedua, dua relay akan berbaris dan hanya satu
// yang bekerja; tanpa yang pertama, keduanya mengirim event yang sama.
//
// Pemanggil WAJIB menjalankannya di dalam transaksi - kunci itu dilepas saat
// transaksinya selesai, dan tanpa transaksi ia dilepas seketika.
func (r *Reader) Unpublished(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	const q = `
		SELECT id, created_at, aggregate_type, aggregate_id, event_type, payload, attempts
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := r.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("reading the outbox: %w", err)
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.CreatedAt, &rec.AggregateType,
			&rec.AggregateID, &rec.EventType, &rec.Payload, &rec.Attempts); err != nil {
			return nil, fmt.Errorf("scanning an outbox row: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating the outbox: %w", err)
	}
	return out, nil
}

// MarkPublished menandai event yang sudah terkirim.
func (r *Reader) MarkPublished(ctx context.Context, ids []uuid.UUID, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}

	const q = `UPDATE outbox SET published_at = $2 WHERE id = ANY($1) AND published_at IS NULL`
	if _, err := r.db.Exec(ctx, q, ids, at); err != nil {
		return fmt.Errorf("marking outbox rows published: %w", err)
	}
	return nil
}

// MarkFailed mencatat percobaan yang gagal.
//
// Ia menaikkan penghitung dan menyimpan galatnya, tetapi TIDAK menandai
// barisnya terkirim: event yang gagal harus dicoba lagi. Yang membedakan
// "gagal sementara" dari "gagal selamanya" adalah penghitungnya, dan itu
// keputusan relay - bukan keputusan di sini.
func (r *Reader) MarkFailed(ctx context.Context, ids []uuid.UUID, cause string) error {
	if len(ids) == 0 {
		return nil
	}

	const q = `UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE id = ANY($1)`
	if _, err := r.db.Exec(ctx, q, ids, truncate(cause, 1000)); err != nil {
		return fmt.Errorf("recording an outbox failure: %w", err)
	}
	return nil
}

// truncate menjaga pesan galat tetap masuk akal ukurannya.
//
// Galat dari pustaka jaringan bisa membawa dump koneksi yang panjang, dan
// tabel outbox bukan tempat menyimpannya.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// eventTypeOf membaca jenis event dari envelope.
//
// Ia dipakai untuk merutekan ke topic. Nama yang dikembalikan sengaja mengikuti
// nama bidang di kontrak, sehingga menambah event baru di proto langsung
// terbawa ke sini tanpa daftar kedua yang bisa tertinggal.
func eventTypeOf(e *eventsv1.Envelope) string {
	switch e.GetPayload().(type) {
	case *eventsv1.Envelope_ProfileUpdated:
		return "profile.updated"
	case *eventsv1.Envelope_AssessmentCompleted:
		return "assessment.completed"
	case *eventsv1.Envelope_PersonalizationRequested:
		return "personalization.requested"
	case *eventsv1.Envelope_PersonalizationCompleted:
		return "personalization.completed"
	case *eventsv1.Envelope_CoachingProgramUpdated:
		return "coaching.program.updated"
	case *eventsv1.Envelope_CurriculumRequested:
		return "curriculum.requested"
	case *eventsv1.Envelope_CurriculumCompleted:
		return "curriculum.completed"
	case *eventsv1.Envelope_ChatReplyRequested:
		return "chat.reply.requested"
	case *eventsv1.Envelope_ChatReplyCompleted:
		return "chat.reply.completed"
	case *eventsv1.Envelope_MealGuideRequested:
		return "meal.guide.requested"
	case *eventsv1.Envelope_MealGuideCompleted:
		return "meal.guide.completed"
	case *eventsv1.Envelope_UserDeletionRequested:
		return "user.deletion.requested"
	case *eventsv1.Envelope_UserDeletionConfirmed:
		return "user.deletion.confirmed"
	case *eventsv1.Envelope_LlmJobFailed:
		return "llm.job.failed"
	default:
		return ""
	}
}
