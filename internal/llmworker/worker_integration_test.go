package llmworker_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
	"github.com/muhananaufal/selaras-platform-go/internal/llmworker"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func brokers(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("TEST_KAFKA_BROKERS")
	if addr == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_KAFKA_BROKERS is not set; integration tests must not be skipped in CI")
		}
		t.Skip("TEST_KAFKA_BROKERS is not set; start the stack with 'task up' to run this test")
	}
	return addr
}

type harness struct {
	pool      *pgxpool.Pool
	provider  *llm.Fake
	consumer  *llmworker.Consumer
	client    *kgo.Client
	closeOnce sync.Once
	producer  *kgo.Client
	topic     string
	group     string
	brokers   string
	ctx       context.Context
}

// newHarness menyiapkan worker terhadap Kafka dan Postgres yang sungguhan.
//
// Setiap test memakai TOPIC-nya sendiri, bukan llm.jobs yang dipakai bersama.
// Topic bersama membuat test saling mewarisi pesan: yang satu membaca pekerjaan
// milik yang lain, dan hasilnya bergantung pada urutan jalankan.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessInGroup(t, "worker-test-"+uuid.NewString())
}

// newHarnessInGroup memakai group konsumen yang ditentukan pemanggil.
//
// Kebanyakan test tidak peduli namanya asal unik; yang menguji commit offset
// peduli, karena ia perlu menyambungkan konsumen kedua ke group yang sama.
func newHarnessInGroup(t *testing.T, group string) *harness {
	t.Helper()

	addr := brokers(t)
	pool := pgtest.Open(t, "llm")
	pgtest.Truncate(t, pool, "llm_jobs", "processed_messages", "outbox")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	topic := "llm.jobs.test." + uuid.NewString()

	producer, err := kafka.NewProducer(kafka.Config{Brokers: addr, ClientID: "worker-test"})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	t.Cleanup(producer.Close)

	if _, err := kafka.EnsureTopics(ctx, producer,
		[]kafka.Topic{{Name: topic, Partitions: 1}}, 1); err != nil {
		t.Fatalf("creating the test topic: %v", err)
	}

	consumerClient, err := kafka.NewConsumer(
		kafka.Config{Brokers: addr, ClientID: "worker-test"},
		group, topic)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	prompts, err := prompt.Load()
	if err != nil {
		t.Fatalf("prompt.Load: %v", err)
	}

	provider := llm.NewFake()
	consumer, err := llmworker.NewConsumer(consumerClient, pool, provider, prompts,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	h := &harness{
		pool: pool, provider: provider, consumer: consumer, client: consumerClient,
		producer: producer, topic: topic, group: group, brokers: addr, ctx: ctx,
	}
	t.Cleanup(h.leaveGroup)
	return h
}

// send menerbitkan satu permintaan personalisasi.
func (h *harness) send(t *testing.T, assessmentID, idempotencyKey string) {
	t.Helper()

	env := &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_PersonalizationRequested{
			PersonalizationRequested: &eventsv1.PersonalizationRequested{
				AssessmentId: assessmentID,
				Slug:         "slug-" + assessmentID,
			},
		},
	}
	if idempotencyKey != "" {
		env.IdempotencyKey = &commonv1.IdempotencyKey{Value: idempotencyKey}
	}

	payload, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if _, err := kafka.NewPublisher(h.producer).Publish(h.ctx, []kafka.Message{{
		Topic: h.topic,
		Key:   []byte(assessmentID),
		Value: payload,
	}}); err != nil {
		t.Fatalf("publishing: %v", err)
	}
}

// runUntil menjalankan worker sampai kondisinya terpenuhi atau waktunya habis.
//
// Ia mengembalikan galat Run, sehingga "matinya rapi" bisa diperiksa - bukan
// hanya "berhentinya".
func (h *harness) runUntil(t *testing.T, timeout time.Duration, done func() bool) error {
	t.Helper()

	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	result := make(chan error, 1)
	go func() { result <- h.consumer.Run(ctx) }()

	deadline := time.After(timeout)
	for {
		if done() {
			cancel()
			select {
			case err := <-result:
				return err
			case <-time.After(15 * time.Second):
				t.Fatal("Run did not return after its context was cancelled")
			}
		}

		select {
		case err := <-result:
			t.Fatalf("Run returned early: %v", err)
		case <-deadline:
			cancel()
			<-result
			t.Fatal("the worker did not finish the work in time")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (h *harness) countJobs(t *testing.T, aggregateID string) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM llm_jobs WHERE aggregate_id = $1`, aggregateID).Scan(&n); err != nil {
		t.Fatalf("counting jobs: %v", err)
	}
	return n
}

// leaveGroup menutup klien konsumen, sehingga ia keluar dari consumer group.
//
// Ia harus dipanggil sebelum konsumen lain di group yang sama menyambung:
// selama anggota lama masih hidup, ia memegang partisinya dan anggota baru
// tidak mendapat apa-apa - yang akan membuat test "tidak menerima apa pun"
// lulus tanpa memeriksa offset sama sekali.
func (h *harness) leaveGroup() {
	h.closeOnce.Do(h.client.Close)
}

// statusOf membaca status pekerjaan, atau string kosong bila belum ada.
func (h *harness) statusOf(t *testing.T, aggregateID string) string {
	t.Helper()

	var status string
	err := h.pool.QueryRow(h.ctx,
		`SELECT status FROM llm_jobs WHERE aggregate_id = $1`, aggregateID).Scan(&status)
	if err != nil {
		return ""
	}
	return status
}

// restart mengganti klien konsumen dengan yang baru di group yang sama.
//
// Ia meniru proses yang mati lalu dinyalakan lagi: anggota lama keluar dari
// group, anggota baru masuk, dan pesan yang offsetnya belum dikomit dikirimkan
// lagi kepadanya.
func (h *harness) restart(t *testing.T) {
	t.Helper()

	h.leaveGroup()

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: h.brokers, ClientID: "worker-test-restarted"},
		h.group, h.topic)
	if err != nil {
		t.Fatalf("restarting the consumer: %v", err)
	}

	prompts, err := prompt.Load()
	if err != nil {
		t.Fatalf("prompt.Load: %v", err)
	}

	consumer, err := llmworker.NewConsumer(client, h.pool, h.provider, prompts,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	h.client = client
	h.consumer = consumer
	h.closeOnce = sync.Once{}
	t.Cleanup(h.leaveGroup)
}

// TestAJobIsDoneAndItsResultStored adalah jalur normal, ujung ke ujung lewat
// broker sungguhan.
func TestAJobIsDoneAndItsResultStored(t *testing.T) {
	h := newHarness(t)
	assessmentID := uuid.NewString()

	h.send(t, assessmentID, "key-"+assessmentID)

	// Yang ditunggu adalah STATUS AKHIRNYA, bukan keberadaan barisnya.
	//
	// Baris pekerjaan dibuat di transaksi klaim; hasilnya ditulis di transaksi
	// berikutnya, setelah penyedia menjawab. Menunggu barisnya ada berarti
	// berhenti di antara keduanya, dan pembacaan setelahnya akan melihat
	// pending - kadang lulus, kadang tidak, tergantung penjadwalan.
	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusCompleted
	}); err != nil {
		t.Fatalf("Run returned %v; a cancelled worker should stop cleanly", err)
	}

	var status, promptVersion, model string
	var result []byte
	if err := h.pool.QueryRow(h.ctx,
		`SELECT status, coalesce(prompt_version,''), coalesce(model,''), result
		 FROM llm_jobs WHERE aggregate_id = $1`, assessmentID,
	).Scan(&status, &promptVersion, &model, &result); err != nil {
		t.Fatalf("reading the job: %v", err)
	}

	if status != llmworker.StatusCompleted {
		t.Fatalf("the job is %q, want completed", status)
	}
	if promptVersion != "personalization@1" {
		t.Fatalf("the job recorded prompt version %q", promptVersion)
	}
	if model == "" {
		t.Fatal("the job recorded no model")
	}

	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("the stored result is not JSON: %v", err)
	}

	// Dan eventnya masuk outbox dalam transaksi yang sama - bukan diterbitkan
	// langsung, yang akan membuatnya bisa hilang saat prosesnya mati.
	var eventType string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT event_type FROM outbox WHERE aggregate_id = $1`, assessmentID,
	).Scan(&eventType); err != nil {
		t.Fatalf("reading the outbox: %v", err)
	}
	if eventType != outbox.EventPersonalizationCompleted {
		t.Fatalf("the outbox holds %q, want personalization.completed", eventType)
	}
}

// TestTheSameJobTwiceIsDoneOnce adalah gate F3 lewat jalur yang sesungguhnya.
//
// Dua pesan dengan kunci idempotensi yang sama, lewat broker sungguhan. Yang
// dihitung bukan berapa pesan yang tiba, melainkan berapa kali penyedia
// dipanggil - itulah yang berbiaya.
func TestTheSameJobTwiceIsDoneOnce(t *testing.T) {
	h := newHarness(t)
	assessmentID := uuid.NewString()
	key := "key-" + assessmentID

	h.send(t, assessmentID, key)
	h.send(t, assessmentID, key)

	if err := h.runUntil(t, 60*time.Second, func() bool {
		// Ditunggu sampai KEDUA pesan terbaca, bukan sampai satu pekerjaan
		// terbentuk - berhenti terlalu cepat akan membuat test ini lulus
		// tanpa pernah melihat pesan kedua.
		var seen int
		if err := h.pool.QueryRow(h.ctx,
			`SELECT count(*) FROM processed_messages`).Scan(&seen); err != nil {
			t.Fatalf("counting claims: %v", err)
		}
		return seen == 1 && h.statusOf(t, assessmentID) == llmworker.StatusCompleted
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	// Jeda pendek supaya pesan kedua sempat diproses sebelum diperiksa.
	time.Sleep(2 * time.Second)

	if got := h.countJobs(t, assessmentID); got != 1 {
		t.Fatalf("two deliveries produced %d jobs, want 1", got)
	}
	if got := h.provider.CallCount(); got != 1 {
		t.Fatalf("the provider was called %d times, want 1", got)
	}

	var events int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1`, assessmentID).Scan(&events); err != nil {
		t.Fatalf("counting events: %v", err)
	}
	if events != 1 {
		t.Fatalf("two deliveries produced %d events, want 1", events)
	}
}

// TestAFailingProviderLeavesTheJobFailed menjaga kegagalan terlihat.
func TestAFailingProviderLeavesTheJobFailed(t *testing.T) {
	h := newHarness(t)
	h.provider.Err = llm.ErrRateLimited

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusFailed
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	var lastError string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT coalesce(last_error,'') FROM llm_jobs WHERE aggregate_id = $1`,
		assessmentID).Scan(&lastError); err != nil {
		t.Fatalf("reading the failure: %v", err)
	}
	if lastError == "" {
		t.Fatal("the job failed without recording why")
	}

	// Kegagalan yang masih akan dicoba lagi TIDAK menerbitkan event gagal.
	// Menerbitkannya akan membuat pemanggil mengira pekerjaannya sudah
	// menyerah padahal belum.
	var events int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1`, assessmentID).Scan(&events); err != nil {
		t.Fatalf("counting events: %v", err)
	}
	if events != 0 {
		t.Fatalf("a retryable failure published %d events, want 0", events)
	}
}

// TestATruncatedAnswerIsNotStoredAsAReport menjaga laporan setengah jadi.
func TestATruncatedAnswerIsNotStoredAsAReport(t *testing.T) {
	h := newHarness(t)
	h.provider.FinishReason = "MAX_TOKENS"

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	if err := h.runUntil(t, 60*time.Second, func() bool {
		s := h.statusOf(t, assessmentID)
		return s != "" && s != llmworker.StatusPending
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	var status string
	var result []byte
	if err := h.pool.QueryRow(h.ctx,
		`SELECT status, result FROM llm_jobs WHERE aggregate_id = $1`, assessmentID,
	).Scan(&status, &result); err != nil {
		t.Fatalf("reading the job: %v", err)
	}

	if status == llmworker.StatusCompleted {
		t.Fatal("an answer that stopped at MAX_TOKENS was stored as a finished report")
	}
	if result != nil {
		t.Fatal("a truncated answer was stored as a result")
	}
}

// TestTheOffsetIsCommittedSoWorkIsNotRepeated adalah gate F3-06 yang
// sesungguhnya: "offset ter-commit dengan benar".
//
// Cara membuktikannya bukan dengan memeriksa apakah fungsinya dipanggil,
// melainkan dengan menanyakannya kepada broker: konsumen KEDUA di group yang
// SAMA tidak boleh menerima pesan yang sudah dikerjakan konsumen pertama.
// Kalau offsetnya tidak terkomit, ia akan menerimanya lagi - ConsumeResetOffset
// group ini mulai dari awal topic.
func TestTheOffsetIsCommittedSoWorkIsNotRepeated(t *testing.T) {
	addr := brokers(t)
	h := newHarnessInGroup(t, "offset-test-"+uuid.NewString())

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusCompleted
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	// Konsumen pertama keluar dari group SEBELUM yang kedua menyambung.
	// Tanpa ini, yang kedua tidak mendapat partisi sama sekali dan test ini
	// akan lulus tanpa pernah menyentuh offset.
	h.leaveGroup()

	// Konsumen kedua, group yang sama, topic yang sama.
	second, err := kafka.NewConsumer(
		kafka.Config{Brokers: addr, ClientID: "offset-check"}, h.group, h.topic)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer second.Close()

	pollCtx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()

	fetches := second.PollFetches(pollCtx)
	var replayed int
	fetches.EachRecord(func(_ *kgo.Record) { replayed++ })

	if replayed != 0 {
		t.Fatalf("a second consumer in the same group received %d messages again; "+
			"the offset was never committed and every restart redoes paid work", replayed)
	}
}

// TestAFailingJobIsRetriedAndThenDeadLettered adalah gate F3-13.
//
// Sebelum perbaikan ini, pekerjaan yang gagal SEKALI berhenti selamanya: klaim
// idempotensinya sudah terpakai, offset-nya sudah dikomit, dan tidak ada yang
// akan mengirim pesannya lagi. Batas tiga percobaan tidak pernah tercapai, dan
// antrean surat mati tidak pernah menerima apa pun.
//
// Yang diperiksa di sini adalah tiga hal berurutan: pekerjaannya benar-benar
// dicoba tiga kali, penghitungnya menumpuk di BARIS YANG SAMA, dan pada
// percobaan ketiga ia menjadi dead beserta event kegagalannya.
func TestAFailingJobIsRetriedAndThenDeadLettered(t *testing.T) {
	h := newHarness(t)
	h.provider.Err = llm.ErrRateLimited

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	if err := h.runUntil(t, 90*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusDead
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	// Satu baris, bukan tiga: percobaan ulang memakai kembali pekerjaan yang
	// sama, sehingga penghitungnya bermakna.
	if got := h.countJobs(t, assessmentID); got != 1 {
		t.Fatalf("three attempts produced %d job rows, want 1", got)
	}

	var attempts int
	var lastError string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT attempts, coalesce(last_error,'') FROM llm_jobs WHERE aggregate_id = $1`,
		assessmentID).Scan(&attempts, &lastError); err != nil {
		t.Fatalf("reading the job: %v", err)
	}
	if attempts != llmworker.MaxAttempts {
		t.Fatalf("the job records %d attempts, want %d", attempts, llmworker.MaxAttempts)
	}
	if lastError == "" {
		t.Fatal("the job gave up without recording why")
	}

	// Dan penyedia benar-benar dipanggil tiga kali - bukan sekadar penghitung
	// yang naik tanpa pekerjaan yang terjadi.
	if got := h.provider.CallCount(); got != llmworker.MaxAttempts {
		t.Fatalf("the provider was called %d times, want %d", got, llmworker.MaxAttempts)
	}

	// Event kegagalan terbit SEKALI, dan hanya setelah menyerah.
	var events int
	var eventType string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*), coalesce(max(event_type),'') FROM outbox WHERE aggregate_id = $1`,
		assessmentID).Scan(&events, &eventType); err != nil {
		t.Fatalf("counting events: %v", err)
	}
	if events != 1 {
		t.Fatalf("%d failure events were published, want exactly 1", events)
	}
	if eventType != outbox.EventLLMJobFailed {
		t.Fatalf("the outbox holds %q, want llm.job.failed", eventType)
	}
}

// TestAnAbandonedJobResumesAfterRestart menutup jalur yang paling sulit benar.
//
// Worker dimatikan di tengah rangkaian percobaan. Yang harus terjadi setelah
// dinyalakan lagi ada tiga, dan ketiganya mudah salah:
//
//  1. Pesannya datang lagi - offsetnya memang belum dikomit.
//  2. Ia TIDAK dilewati sebagai duplikat - klaimnya dilepas saat menyerah.
//  3. Penghitung percobaannya MENUMPUK di baris yang sama, bukan mulai dari nol
//     di baris baru. Kalau tidak, batas tiga kali tidak akan pernah tercapai
//     dan pekerjaan yang selalu gagal akan dicoba selamanya.
func TestAnAbandonedJobResumesAfterRestart(t *testing.T) {
	h := newHarness(t)
	h.provider.Err = llm.ErrRateLimited

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	// Dihentikan segera setelah kegagalan pertama tercatat.
	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.attemptsOf(t, assessmentID) >= 1
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	if got := h.attemptsOf(t, assessmentID); got >= llmworker.MaxAttempts {
		t.Skipf("the worker reached %d attempts before it could be stopped; the race is too tight to test here", got)
	}

	// Klaimnya harus sudah dilepas, kalau tidak pengiriman berikutnya akan
	// dilewati sebagai duplikat dan pekerjaannya berhenti selamanya.
	var claims int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM processed_messages`).Scan(&claims); err != nil {
		t.Fatalf("counting claims: %v", err)
	}
	if claims != 0 {
		t.Fatalf("%d claims survived the shutdown; the job can never be retried", claims)
	}

	// Dinyalakan lagi, dan kali ini dibiarkan sampai menyerah.
	h.restart(t)
	if err := h.runUntil(t, 90*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusDead
	}); err != nil {
		t.Fatalf("the restarted worker returned %v", err)
	}

	if got := h.countJobs(t, assessmentID); got != 1 {
		t.Fatalf("the restart produced %d job rows, want 1 - the attempt counter is meaningless across rows", got)
	}
	if got := h.attemptsOf(t, assessmentID); got != llmworker.MaxAttempts {
		t.Fatalf("the job records %d attempts, want %d", got, llmworker.MaxAttempts)
	}
}

// attemptsOf membaca penghitung percobaan, atau -1 bila pekerjaannya belum ada.
func (h *harness) attemptsOf(t *testing.T, aggregateID string) int {
	t.Helper()

	var attempts int
	err := h.pool.QueryRow(h.ctx,
		`SELECT attempts FROM llm_jobs WHERE aggregate_id = $1`, aggregateID).Scan(&attempts)
	if err != nil {
		return -1
	}
	return attempts
}

// TestAnUnusableRequestTellsWhoeverIsWaiting adalah F6 yang menemukannya.
//
// nutrition-svc dinyalakan sementara llm-worker belum dibangun ulang. Worker
// lama tidak mengenal MealGuideRequested, melewatinya, dan panduan itu tetap
// pending SELAMANYA - tanpa galat, tanpa status yang berubah, hanya satu baris
// log yang harus kebetulan dibaca seseorang. Kesenjangan versi seperti itu
// terjadi di setiap penggelaran bertahap.
//
// Kini yang menunggunya diberi tahu lewat DLQ, memakai agregat dari header dan
// kunci partisi pesannya - persis yang sudah dibaca setiap konsumen.
func TestAnUnusableRequestTellsWhoeverIsWaiting(t *testing.T) {
	h := newHarness(t)

	guideID := uuid.NewString()

	// Envelope yang TIDAK memuat permintaan LLM apa pun. Bentuknya sah,
	// kuncinya ada, tetapi payload-nya bukan sesuatu yang worker ini kenal -
	// persis keadaan worker lama yang bertemu jenis pekerjaan baru.
	env := &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.Now(),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "unusable:" + guideID},
		Payload: &eventsv1.Envelope_ProfileUpdated{
			ProfileUpdated: &eventsv1.ProfileUpdated{UserId: uuid.NewString()},
		},
	}
	payload, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if _, err := kafka.NewPublisher(h.producer).Publish(h.ctx, []kafka.Message{{
		Topic:   h.topic,
		Key:     []byte(guideID),
		Value:   payload,
		Headers: map[string]string{"aggregate_type": "meal_guide"},
	}}); err != nil {
		t.Fatalf("publishing: %v", err)
	}

	if err := h.runUntil(t, 30*time.Second, func() bool {
		return h.dlqEventsFor(t, guideID) > 0
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Satu event gagal, membawa agregat yang benar sehingga konsumen yang
	// menunggu bisa mengenalinya.
	if got := h.dlqEventsFor(t, guideID); got != 1 {
		t.Fatalf("the waiting aggregate was told %d times, want exactly 1", got)
	}

	var aggregateType, reason string
	if err := h.pool.QueryRow(h.ctx, `
		SELECT aggregate_type, coalesce(last_error, '') FROM outbox WHERE aggregate_id = $1`,
		guideID).Scan(&aggregateType, &reason); err != nil {
		t.Fatalf("reading the outbox row: %v", err)
	}
	if aggregateType != "meal_guide" {
		t.Errorf("the failure names aggregate type %q; consumers filter on it", aggregateType)
	}

	// Dan TIDAK ada pekerjaan yang dibuat: tidak ada yang bisa dikerjakan.
	if got := h.countJobs(t, guideID); got != 0 {
		t.Errorf("%d jobs were created for a request nobody understands", got)
	}
}

// TestAnUnusableRequestWithNoAggregateTypeDoesNotBlockTheQueue menjaga jalur tanpa alamat.
//
// Tanpa header aggregate_type, tidak ada konsumen yang bisa mengenali event
// gagalnya - setiap konsumen menyaring justru pada header itu. Menulis baris
// outbox yang tidak bisa dikenali siapa pun lebih buruk daripada tidak menulis:
// ia menambah pesan yang setiap konsumen bongkar lalu buang.
//
// Pesan TANPA KUNCI sengaja tidak diuji di sini: publisher platform ini
// menolaknya lebih dulu ("a message with no key would lose its ordering"), jadi
// keadaan itu tidak bisa dibuat lewat jalur yang sesungguhnya. Penjagaannya di
// kode tetap ada untuk produsen lain, dan itu dinyatakan - bukan diuji dengan
// jalur palsu yang membuktikan hal lain.
func TestAnUnusableRequestWithNoAggregateTypeDoesNotBlockTheQueue(t *testing.T) {
	addr := brokers(t)
	h := newHarnessInGroup(t, "unaddressed-"+uuid.NewString())

	env := &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.Now(),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "unaddressed:" + uuid.NewString()},
		Payload: &eventsv1.Envelope_ProfileUpdated{
			ProfileUpdated: &eventsv1.ProfileUpdated{UserId: uuid.NewString()},
		},
	}
	payload, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	before := h.totalOutboxRows(t)

	// Kunci ADA - publisher platform ini mensyaratkannya - tetapi headernya
	// tidak, dan tanpa header itu tidak ada konsumen yang bisa mengenalinya.
	if _, err := kafka.NewPublisher(h.producer).Publish(h.ctx, []kafka.Message{{
		Topic: h.topic,
		Key:   []byte(uuid.NewString()),
		Value: payload,
	}}); err != nil {
		t.Fatalf("publishing: %v", err)
	}

	// Worker dijalankan untuk waktu tertentu lalu dihentikan.
	//
	// runUntil tidak dipakai di sini: ia menunggu sesuatu TERJADI, sementara
	// yang harus dibuktikan justru bahwa tidak terjadi apa-apa. Memakainya akan
	// menggagalkan test dengan "did not finish the work in time" - kalimat yang
	// menggambarkan keberhasilan sebagai kegagalan.
	runCtx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()

	stopped := make(chan error, 1)
	go func() { stopped <- h.consumer.Run(runCtx) }()

	select {
	case err := <-stopped:
		// Berhenti karena waktunya habis adalah yang diharapkan; berhenti
		// karena galat berarti worker macet pada pesan ini.
		if err != nil {
			t.Fatalf("the worker stopped with an error on an unaddressable message: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after its context expired")
	}

	if after := h.totalOutboxRows(t); after != before {
		t.Errorf("%d outbox rows were written for a message nobody can be told about", after-before)
	}

	// Dan yang sesungguhnya membedakan: OFFSET-NYA MAJU.
	//
	// Tanpa penjaganya, announceUnusable meneruskan agregat kosong ke
	// outbox.Write, yang menolaknya dengan galat yang sama - handle
	// mengembalikan galat, offset DITAHAN, dan pesan yang tidak bisa
	// dialamatkan itu dikirim ulang selamanya, menyumbat antrean untuk semua
	// orang. Jumlah baris outbox tidak bisa membedakan keduanya; hanya ini
	// yang bisa.
	h.leaveGroup()

	second, err := kafka.NewConsumer(
		kafka.Config{Brokers: addr, ClientID: "unaddressed-check"}, h.group, h.topic)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer second.Close()

	pollCtx, cancelPoll := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancelPoll()

	var replayed int
	second.PollFetches(pollCtx).EachRecord(func(_ *kgo.Record) { replayed++ })

	if replayed != 0 {
		t.Fatalf("a second consumer received the unaddressable message %d times again; "+
			"its offset was never committed, and it will block the queue forever", replayed)
	}
}

// dlqEventsFor menghitung event gagal untuk sebuah agregat.
func (h *harness) dlqEventsFor(t *testing.T, aggregateID string) int {
	t.Helper()

	var n int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1 AND event_type = $2`,
		aggregateID, outbox.EventLLMJobFailed).Scan(&n); err != nil {
		t.Fatalf("counting dlq events: %v", err)
	}
	return n
}

func (h *harness) totalOutboxRows(t *testing.T) int {
	t.Helper()

	var n int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatalf("counting outbox rows: %v", err)
	}
	return n
}
