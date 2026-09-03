package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// recorder adalah broker palsu yang mencatat apa yang benar-benar diterimanya.
//
// Ia mencatat berdasarkan header outbox_id, bukan berdasarkan urutan panggilan,
// supaya "hilang" dan "ganda" bisa dibedakan dengan tegas.
type recorder struct {
	mu       sync.Mutex
	received []string

	// failFrom membuat pesan mulai indeks ini gagal. -1 berarti semuanya lolos.
	failFrom int

	// panicAfter membuat publisher panik setelah sekian pesan tercatat, meniru
	// proses yang mati SETELAH broker menerima tetapi SEBELUM transaksinya
	// commit. Nol berarti tidak pernah panik.
	panicAfter int
}

func newRecorder() *recorder { return &recorder{failFrom: -1} }

func (r *recorder) Publish(_ context.Context, msgs []kafka.Message) ([]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var ok []int
	var err error
	for i, m := range msgs {
		if r.failFrom >= 0 && i >= r.failFrom {
			if err == nil {
				err = errors.New("the broker refused this one")
			}
			continue
		}
		r.received = append(r.received, m.Headers["outbox_id"])
		ok = append(ok, i)

		if r.panicAfter > 0 && len(r.received) >= r.panicAfter {
			// Diterima broker, lalu prosesnya mati. Persis titik yang membuat
			// relay ini at-least-once dan bukan exactly-once.
			panic("the process died after the broker accepted")
		}
	}
	return ok, err
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.received...)
}

func newRelay(t *testing.T, pool *pgxpool.Pool, pub outbox.Publisher, batch int) *outbox.Relay {
	t.Helper()
	relay, err := outbox.NewRelay(pool, pub,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		outbox.RelayOptions{Batch: batch, Interval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	return relay
}

// seedEvents menulis n event dan mengembalikan id baris outbox-nya.
func seedEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) []string {
	t.Helper()

	for i := 0; i < n; i++ {
		userID := uuid.New()
		if err := pg.InTx(ctx, pool, func(q pg.Querier) error {
			if err := insertUser(ctx, q, userID); err != nil {
				return err
			}
			return outbox.NewWriter(q).Write(ctx, "user", userID.String(), envelope(t, userID.String()))
		}); err != nil {
			t.Fatalf("seeding event %d: %v", i, err)
		}
	}

	rows, err := pool.Query(ctx, `SELECT id FROM outbox ORDER BY created_at`)
	if err != nil {
		t.Fatalf("reading seeded ids: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scanning id: %v", err)
		}
		ids = append(ids, id.String())
	}
	if len(ids) != n {
		t.Fatalf("seeded %d events but found %d", n, len(ids))
	}
	return ids
}

func unpublishedCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("counting unpublished: %v", err)
	}
	return n
}

// TestTheRelayMovesEveryEventOnce adalah keadaan normal.
func TestTheRelayMovesEveryEventOnce(t *testing.T) {
	pool, ctx := setup(t)
	ids := seedEvents(t, ctx, pool, 7)

	rec := newRecorder()
	relay := newRelay(t, pool, rec, 10)

	moved, err := relay.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if moved != 7 {
		t.Fatalf("moved %d events, want 7", moved)
	}

	assertAllSeen(t, ids, rec.seen())
	if left := unpublishedCount(t, ctx, pool); left != 0 {
		t.Fatalf("%d events are still unpublished", left)
	}
}

// TestAKillMidFlightLosesNothing adalah gate F3-04.
//
// Publisher-nya panik setelah tiga pesan diterima broker - proses yang mati
// setelah broker menerima tetapi sebelum transaksinya commit. Relay dinyalakan
// lagi, dan yang diperiksa adalah: setiap event sampai SETIDAKNYA sekali, dan
// tidak ada satu pun yang hilang.
func TestAKillMidFlightLosesNothing(t *testing.T) {
	pool, ctx := setup(t)
	ids := seedEvents(t, ctx, pool, 6)

	dying := newRecorder()
	dying.panicAfter = 3

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("the publisher was supposed to die mid-flight")
			}
		}()
		//nolint:errcheck // Yang diuji adalah paniknya, bukan nilai kembaliannya.
		_, _ = newRelay(t, pool, dying, 10).Once(ctx)
	}()

	// Transaksinya batal, jadi TIDAK ADA yang tertandai terkirim - meski broker
	// sudah menerima tiga. Itulah harga at-least-once, dan itu harga yang benar.
	if left := unpublishedCount(t, ctx, pool); left != 6 {
		t.Fatalf("after the crash %d events are unpublished, want all 6", left)
	}

	// Dinyalakan lagi.
	revived := newRecorder()
	if _, err := newRelay(t, pool, revived, 10).Once(ctx); err != nil {
		t.Fatalf("the revived relay failed: %v", err)
	}

	assertAllSeen(t, ids, revived.seen())
	if left := unpublishedCount(t, ctx, pool); left != 0 {
		t.Fatalf("%d events survived the restart unpublished", left)
	}

	// Dan duplikatnya nyata, bukan diabaikan: tiga event sampai dua kali di
	// seluruh riwayat. Ia dinyatakan di sini supaya jaminan yang sebenarnya -
	// at-least-once - tidak diam-diam berubah menjadi klaim exactly-once.
	total := len(dying.seen()) + len(revived.seen())
	if total != 9 {
		t.Fatalf("the broker saw %d deliveries in total, want 9 (6 plus the 3 that were re-sent)", total)
	}
}

// TestAPartialFailureKeepsTheRestPublished menjaga kegagalan sebagian tidak
// berubah menjadi kegagalan seluruhnya.
func TestAPartialFailureKeepsTheRestPublished(t *testing.T) {
	pool, ctx := setup(t)
	seedEvents(t, ctx, pool, 5)

	rec := newRecorder()
	rec.failFrom = 3 // dua terakhir gagal

	moved, err := newRelay(t, pool, rec, 10).Once(ctx)
	if err != nil {
		t.Fatalf("Once should absorb a publish failure, got: %v", err)
	}
	if moved != 3 {
		t.Fatalf("moved %d events, want 3", moved)
	}

	if left := unpublishedCount(t, ctx, pool); left != 2 {
		t.Fatalf("%d events are unpublished, want exactly the 2 that failed", left)
	}

	// Yang gagal tercatat gagal, bukan hanya tertinggal diam-diam.
	var attempts int
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(max(attempts), 0) FROM outbox WHERE published_at IS NULL`,
	).Scan(&attempts); err != nil {
		t.Fatalf("reading attempts: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("the failed rows record %d attempts, want 1", attempts)
	}

	// Percobaan berikutnya menyelesaikannya, dan TIDAK mengirim ulang yang
	// sudah berhasil.
	again := newRecorder()
	if _, err := newRelay(t, pool, again, 10).Once(ctx); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if got := len(again.seen()); got != 2 {
		t.Fatalf("the retry re-sent %d events, want only the 2 that had failed", got)
	}
}

// TestTheMessageKeyIsTheAggregate menjaga alasan keberadaan kunci partisi.
//
// Kunci menentukan partisi, dan partisi menentukan urutan. Dengan kunci yang
// unik per baris - id outbox, misalnya - setiap event mendarat di partisi acak
// dan urutan antar event satu agregat hilang: "profil diperbarui" bisa tiba
// setelah "profil dihapus". Tidak ada galat yang muncul saat itu terjadi.
func TestTheMessageKeyIsTheAggregate(t *testing.T) {
	pool, ctx := setup(t)

	userID := uuid.New()
	if err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		if err := insertUser(ctx, q, userID); err != nil {
			return err
		}
		return outbox.NewWriter(q).Write(ctx, "user", userID.String(), envelope(t, userID.String()))
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	keys := &keySpy{}
	if _, err := newRelay(t, pool, keys, 10).Once(ctx); err != nil {
		t.Fatalf("Once: %v", err)
	}

	if len(keys.keys) != 1 {
		t.Fatalf("the relay published %d messages, want 1", len(keys.keys))
	}
	if got := keys.keys[0]; got != userID.String() {
		t.Fatalf("the partition key is %q, want the aggregate id %q", got, userID)
	}
	if keys.topics[0] != outbox.TopicProfileUpdated {
		t.Fatalf("routed to %q, want %q", keys.topics[0], outbox.TopicProfileUpdated)
	}
}

// keySpy mencatat kunci dan topic tiap pesan.
type keySpy struct {
	keys   []string
	topics []string
}

func (k *keySpy) Publish(_ context.Context, msgs []kafka.Message) ([]int, error) {
	ok := make([]int, 0, len(msgs))
	for i, m := range msgs {
		k.keys = append(k.keys, string(m.Key))
		k.topics = append(k.topics, m.Topic)
		ok = append(ok, i)
	}
	return ok, nil
}

// TestNothingIsMarkedSentWhenTheBrokerRefusesEverything adalah penjaga terhadap
// urutan yang terbalik.
//
// Kalau baris ditandai terkirim SEBELUM broker mengakuinya, penerbitan yang
// gagal seluruhnya tetap meninggalkan outbox yang bersih - dan setiap event di
// dalamnya hilang tanpa jejak. Tidak ada yang akan mencarinya lagi.
func TestNothingIsMarkedSentWhenTheBrokerRefusesEverything(t *testing.T) {
	pool, ctx := setup(t)
	seedEvents(t, ctx, pool, 4)

	refusing := newRecorder()
	refusing.failFrom = 0 // semuanya ditolak

	moved, err := newRelay(t, pool, refusing, 10).Once(ctx)
	if err != nil {
		t.Fatalf("Once should absorb the failure, got: %v", err)
	}
	if moved != 0 {
		t.Fatalf("the relay claims it moved %d events; the broker took none", moved)
	}
	if left := unpublishedCount(t, ctx, pool); left != 4 {
		t.Fatalf("%d events are still unpublished, want all 4 - the rest were lost", left)
	}
	if got := len(refusing.seen()); got != 0 {
		t.Fatalf("the broker recorded %d deliveries, want 0", got)
	}
}

func assertAllSeen(t *testing.T, want, got []string) {
	t.Helper()

	seen := make(map[string]int, len(got))
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		if seen[id] == 0 {
			t.Errorf("event %s never reached the broker", id)
		}
	}
}
