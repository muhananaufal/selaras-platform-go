package llmworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/idempotency"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Scope adalah ruang lingkup idempotensi worker ini.
//
// Ia tetap, dan itu penting: mengubahnya berarti seluruh pekerjaan yang sudah
// pernah dikerjakan terlihat belum pernah, dan semuanya dikerjakan ulang.
const Scope = "llm-worker"

// MaxAttempts adalah berapa kali sebuah pesan dicoba sebelum masuk antrean
// surat mati.
const MaxAttempts = 3

// notYetInTheEvent menandai bidang prompt yang belum dibawa eventnya.
//
// Ia dinyatakan apa adanya, bukan diisi tebakan. Tebakan di sini akan sampai ke
// model sebagai fakta tentang seseorang, dan laporan yang dihasilkannya akan
// terlihat seperti laporan biasa. F3-10 melengkapi eventnya.
const notYetInTheEvent = "not yet carried by the event"

// Consumer membaca llm.jobs dan mengerjakannya.
type Consumer struct {
	client   *kgo.Client
	pool     pg.Beginner
	provider llm.Provider
	prompts  *prompt.Library
	jobs     *Repository
	log      *slog.Logger
}

// NewConsumer merangkai worker.
func NewConsumer(
	client *kgo.Client,
	pool pg.Beginner,
	provider llm.Provider,
	prompts *prompt.Library,
	log *slog.Logger,
) (*Consumer, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case pool == nil:
		return nil, errors.New("nil pool")
	case provider == nil:
		return nil, errors.New("nil llm provider")
	case prompts == nil:
		return nil, errors.New("nil prompt library")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Consumer{
		client: client, pool: pool, provider: provider,
		prompts: prompts, jobs: NewRepository(), log: log,
	}, nil
}

// Run membaca sampai ctx selesai.
//
// Matinya rapi, dan "rapi" punya arti yang tepat di sini: pekerjaan yang sedang
// berjalan diselesaikan, offset-nya dikomit, DAN BARU kemudian loop-nya
// berhenti. Berhenti di tengah tanpa mengomit berarti pekerjaan yang sudah
// selesai dikerjakan lagi oleh proses berikutnya - tidak merusak, karena
// idempotensi menahannya, tetapi membuang waktu penyedia yang berbayar.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "llm worker started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "llm worker stopped")
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan; lihat komentar Run.
			return nil
		}

		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			// Pembatalan saat menunggu bukan kegagalan. PollFetches
			// mengembalikan galat konteks di sini, dan melaporkannya sebagai
			// kerusakan akan membuat setiap shutdown yang rapi terlihat buruk.
			c.log.InfoContext(ctx, "llm worker stopped")
			//nolint:nilerr // Idem: PollFetches mengembalikan galat konteks saat dibatalkan.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				c.log.ErrorContext(ctx, "fetching from kafka failed",
					"topic", e.Topic, "partition", e.Partition, "error", e.Err)
			}
			// Broker yang sedang memilih leader adalah keadaan sementara. Jeda
			// pendek supaya loop tidak berputar penuh terhadap galat yang sama.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		var handled int
		fetches.EachRecord(func(rec *kgo.Record) {
			// ctx.Err() diperiksa PER PESAN, bukan hanya per putaran. Satu
			// batch bisa memuat ratusan pesan yang masing-masing menunggu
			// jaringan puluhan detik; tanpa pemeriksaan ini, shutdown menunggu
			// seluruh batch selesai.
			if ctx.Err() != nil {
				return
			}
			if err := c.handle(ctx, rec); err != nil {
				c.log.ErrorContext(ctx, "handling a job failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
			}
			handled++
		})

		if handled == 0 {
			continue
		}

		// Offset dikomit SETELAH pekerjaannya selesai, bukan berdasarkan waktu.
		//
		// Auto-commit menandai pesan selesai berdasarkan jam: pekerjaan yang
		// gagal di tengah jalan tetap tercatat selesai, dan pesannya tidak
		// pernah datang lagi. Itulah sebabnya klien ini dibuat dengan
		// DisableAutoCommit.
		if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
			// Gagal mengomit berarti pesan yang sama akan datang lagi.
			// Idempotensi menahannya, jadi ini bukan kerusakan - tetapi ia
			// harus terlihat, bukan hilang.
			c.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// handle mengerjakan satu pesan.
//
// Alurnya: klaim -> kerjakan -> simpan hasil dan event keluarnya, DALAM SATU
// transaksi untuk bagian yang menyentuh basis data. Panggilan ke penyedia
// sengaja berada DI LUAR transaksi: ia bisa menunggu puluhan detik, dan
// transaksi yang menganga selama itu menahan koneksi serta kunci tanpa alasan.
func (c *Consumer) handle(ctx context.Context, rec *kgo.Record) error {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		// Pesan yang tidak bisa dibaca tidak akan pernah bisa dibaca. Ia
		// dilewati, bukan diulang selamanya - dan dicatat supaya bisa
		// diselidiki.
		c.log.ErrorContext(ctx, "a message could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	key := idempotencyKeyOf(&env)
	if key == "" {
		c.log.ErrorContext(ctx, "a message carried no idempotency key and was skipped",
			"event_id", env.GetEventId())
		return nil
	}

	req, err := personalizationOf(&env)
	if err != nil {
		c.log.ErrorContext(ctx, "a message was not a personalisation request",
			"event_id", env.GetEventId(), "error", err)
		return nil
	}

	// Tahap satu: klaim. Kalau kuncinya sudah pernah dipakai, pekerjaannya
	// sudah dikerjakan dan tidak ada yang perlu dilakukan - inilah yang
	// menahan duplikat dari relay yang at-least-once.
	claimed, job, err := c.claim(ctx, key, req)
	if err != nil {
		return err
	}
	if !claimed {
		c.log.InfoContext(ctx, "a job arrived again and was skipped",
			"idempotency_key", key, "event_id", env.GetEventId())
		return nil
	}

	// Tahap dua: kerjakan, di luar transaksi.
	answer, genErr := c.generate(ctx, req)

	// Tahap tiga: simpan hasilnya beserta event keluarnya, dalam satu transaksi.
	if genErr != nil {
		return c.recordFailure(ctx, job, req, genErr)
	}
	return c.recordSuccess(ctx, job, req, answer)
}

// claim membuat pekerjaan baru bila kuncinya belum pernah dipakai.
func (c *Consumer) claim(
	ctx context.Context, key string, req *eventsv1.PersonalizationRequested,
) (claimed bool, job *Job, err error) {
	err = pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		guard, err := idempotency.NewGuard(q, Scope)
		if err != nil {
			return err
		}

		ok, err := guard.Claim(ctx, key)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		job = &Job{
			Key:           key,
			Kind:          KindPersonalization,
			AggregateType: "assessment",
			AggregateID:   req.GetAssessmentId(),
		}
		if err := c.jobs.Create(ctx, q, job); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil {
		return false, nil, fmt.Errorf("claiming the job: %w", err)
	}
	return claimed, job, nil
}

// generate memanggil penyedia.
func (c *Consumer) generate(
	ctx context.Context, req *eventsv1.PersonalizationRequested,
) (*llm.Response, error) {
	tmpl, err := c.prompts.Latest("personalization")
	if err != nil {
		return nil, err
	}

	// Data promptnya masih tipis: assessment-svc belum mengirim profil dan
	// jawabannya di dalam event (F3-10 melengkapinya). Yang ada sekarang sudah
	// cukup untuk membuktikan alurnya, dan bidang yang belum ada dinyatakan
	// apa adanya alih-alih diisi tebakan yang akan sampai ke model.
	rendered, err := tmpl.Render(map[string]any{
		"Profile":        notYetInTheEvent,
		"Answers":        notYetInTheEvent,
		"ModelUsed":      notYetInTheEvent,
		"RiskPercentage": notYetInTheEvent,
		"Age":            notYetInTheEvent,
		"Language":       "Bahasa Indonesia",
	})
	if err != nil {
		return nil, err
	}

	return c.provider.Generate(ctx, llm.Request{
		Prompt:        rendered,
		PromptVersion: tmpl.ID(),
		JSON:          true,
		Temperature:   0.7,
	})
}

// recordSuccess menyimpan hasil dan menerbitkan event selesainya.
func (c *Consumer) recordSuccess(
	ctx context.Context, job *Job,
	req *eventsv1.PersonalizationRequested, answer *llm.Response,
) error {
	if answer.Truncated() {
		// Jawaban terpotong bukan jawaban. Menyimpannya sebagai laporan utuh
		// akan menampilkan analisis setengah jadi seolah lengkap.
		return c.recordFailure(ctx, job, req,
			fmt.Errorf("%w: the provider stopped at %q", llm.ErrTruncated, answer.FinishReason))
	}

	return pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		if err := c.jobs.Complete(ctx, q, job.ID, job.CreatedAt,
			[]byte(answer.Text), answer.PromptVersion, answer.Model); err != nil {
			return err
		}

		return outbox.NewWriter(q).Write(ctx, "assessment", req.GetAssessmentId(), &eventsv1.Envelope{
			EventId:       job.ID.String(),
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: 1,
			Payload: &eventsv1.Envelope_PersonalizationCompleted{
				PersonalizationCompleted: &eventsv1.PersonalizationCompleted{
					AssessmentId:  req.GetAssessmentId(),
					JobId:         job.ID.String(),
					ReportJson:    answer.Text,
					PromptVersion: answer.PromptVersion,
				},
			},
		})
	})
}

// recordFailure mencatat kegagalan, dan menerbitkan event gagal saat pekerjaan
// itu sudah tidak akan dicoba lagi.
func (c *Consumer) recordFailure(
	ctx context.Context, job *Job,
	req *eventsv1.PersonalizationRequested, cause error,
) error {
	dead := job.Attempts+1 >= MaxAttempts

	c.log.ErrorContext(ctx, "a job failed",
		"job_id", job.ID, "attempts", job.Attempts+1, "dead", dead, "error", cause)

	return pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		if err := c.jobs.Fail(ctx, q, job.ID, job.CreatedAt, cause.Error(), dead); err != nil {
			return err
		}
		if !dead {
			return nil
		}

		// Event gagal hanya diterbitkan saat pekerjaannya benar-benar berhenti
		// dicoba. Menerbitkannya di setiap kegagalan akan membuat pemanggil
		// mengira pekerjaannya sudah menyerah padahal masih akan diulang.
		return outbox.NewWriter(q).Write(ctx, "assessment", req.GetAssessmentId(), &eventsv1.Envelope{
			EventId:       job.ID.String(),
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: 1,
			Payload: &eventsv1.Envelope_LlmJobFailed{
				LlmJobFailed: &eventsv1.LlmJobFailed{
					JobId:  job.ID.String(),
					Reason: truncate(cause.Error(), 500),
				},
			},
		})
	})
}

// idempotencyKeyOf memilih kunci yang dipakai untuk menahan duplikat.
//
// Kunci yang dikirim pemanggil menang; kalau tidak ada, event_id yang dipakai.
// Keduanya perlu: yang pertama membuat permintaan ulang dari pengguna yang sama
// tidak menghasilkan dua pekerjaan, yang kedua membuat pengiriman ulang dari
// relay tidak menghasilkan dua pekerjaan.
func idempotencyKeyOf(env *eventsv1.Envelope) string {
	if key := env.GetIdempotencyKey().GetValue(); key != "" {
		return key
	}
	return env.GetEventId()
}
