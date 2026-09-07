package llmworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/idempotency"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
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

	// metrics boleh nil. Worker tanpa metrik mencatat lebih sedikit, tetapi
	// tidak berperilaku lain - dan memaksanya wajib akan membuat test harus
	// menyiapkan meter yang tidak diuji apa pun.
	metrics *Metrics

	// cooldown menentukan berapa lama worker berhenti mengambil pekerjaan
	// setelah penyedia menolak karena kuota (ADR-025). Argumennya jumlah
	// penolakan beruntun; nol setelah satu jawaban berhasil.
	cooldown func(consecutive int) time.Duration

	// quotaHits menghitung penolakan kuota beruntun. Hanya disentuh loop Run.
	quotaHits int
}

// DefaultQuotaCooldown adalah kebijakan bawaan: satu menit, berlipat dua
// setiap penolakan beruntun, paling lama lima belas menit.
//
// Satu menit karena kuota per-menit pulih dalam satu menit; lima belas menit
// karena kuota per-hari tidak pulih berapa pun lamanya menunggu, dan satu
// permintaan gagal setiap lima belas menit adalah harga yang murah untuk
// pekerjaan yang tidak pernah mati.
func DefaultQuotaCooldown(consecutive int) time.Duration {
	const base, ceiling = time.Minute, 15 * time.Minute
	if consecutive <= 1 {
		return base
	}
	d := base << (consecutive - 1)
	if d > ceiling || d <= 0 {
		return ceiling
	}
	return d
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
		cooldown: DefaultQuotaCooldown,
	}, nil
}

// WithQuotaCooldown mengganti kebijakan jeda kuota; dipakai test supaya tidak
// menunggu satu menit.
func (c *Consumer) WithQuotaCooldown(f func(consecutive int) time.Duration) *Consumer {
	if f != nil {
		c.cooldown = f
	}
	return c
}

// WithMetrics memasang instrumen antrean (F3-15).
//
// Terpisah dari NewConsumer supaya test tidak perlu menyiapkan meter yang tidak
// menguji apa pun, dan supaya worker tetap bisa berjalan saat telemetri gagal
// disiapkan - metrik yang hilang jauh lebih ringan akibatnya daripada worker
// yang menolak start.
func (c *Consumer) WithMetrics(m *Metrics) *Consumer {
	c.metrics = m
	return c
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
			// Topic yang dibuat ulang di broker (B26): dilanggani ulang di sini,
			// bukan lewat restart. franz-go sengaja tidak pulih sendiri.
			if recovered := kafka.RecoverRecreatedTopics(c.client, errs); len(recovered) > 0 {
				c.log.WarnContext(ctx, "topics were recreated on the broker; subscribed again", "topics", recovered)
			}
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
		rewinder := kafka.NewRewinder()
		var pause time.Duration
		fetches.EachRecord(func(rec *kgo.Record) {
			// ctx.Err() diperiksa PER PESAN, bukan hanya per putaran. Satu
			// batch bisa memuat ratusan pesan yang masing-masing menunggu
			// jaringan puluhan detik; tanpa pemeriksaan ini, shutdown menunggu
			// seluruh batch selesai.
			if ctx.Err() != nil {
				return
			}
			err := c.handle(ctx, rec)
			var parked *parkedError
			switch {
			case err == nil:
			case errors.As(err, &parked):
				// Penyedia menolak karena kuota (ADR-025): pekerjaannya dilepas,
				// offsetnya ditahan, dan worker berhenti sejenak - bukan mencatat
				// kegagalan yang mendekatkannya ke mati.
				c.quotaHits++
				if d := c.cooldown(c.quotaHits); d > pause {
					pause = d
				}
				c.log.WarnContext(ctx, "the provider is out of quota; parking the queue",
					"consecutive", c.quotaHits, "cooldown", pause, "error", parked.cause)
				rewinder.Failed(rec)
			default:
				// Galat lain - Postgres tidak terjangkau saat klaim, misalnya -
				// menahan offset supaya pesannya datang lagi. Sebelum ini offset
				// tetap dikomit dan pekerjaannya hilang diam-diam.
				c.log.ErrorContext(ctx, "handling a job failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			// Sama seperti konsumen lain: tidak mengomit saja tidak cukup,
			// franz-go tidak mengirim ulang di dalam sesi yang sama.
			rewinder.Rewind(c.client)
			if pause < time.Second {
				pause = time.Second
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pause):
			}
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
func (c *Consumer) handle(ctx context.Context, rec *kgo.Record) (err error) {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		// Pesan yang tidak bisa dibaca tidak akan pernah bisa dibaca. Ia
		// dilewati, bukan diulang selamanya - dan dicatat supaya bisa
		// diselidiki.
		c.log.ErrorContext(ctx, "a message could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	// Span konsumen menjadi anak dari permintaan yang menulis event ini
	// (F9-05); galat yang dikembalikan handler tercatat di span-nya.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	key := idempotencyKeyOf(&env)
	if key == "" {
		c.log.ErrorContext(ctx, "a message carried no idempotency key and was skipped",
			"event_id", env.GetEventId())
		return nil
	}

	req, err := requestOf(&env)
	if err != nil {
		// Pesan yang jenisnya tidak dikenali tidak akan pernah bisa dikerjakan.
		// Ia dilewati, bukan diulang selamanya - tetapi yang menunggunya DIBERI
		// TAHU, bukan dibiarkan menunggu tanpa akhir.
		c.log.ErrorContext(ctx, "a message carried no usable LLM request",
			"event_id", env.GetEventId(), "error", err)
		if err := c.announceUnusable(ctx, &env, rec, err); err != nil {
			// Tetap dilewati, bukan ditahan: menahan offset untuk pesan yang
			// memang bukan milik siapa pun menyumbat antrean selamanya (lihat
			// test-nya). Kegagalan mengumumkannya dicatat, itu saja.
			c.log.ErrorContext(ctx, "an unusable request could not be announced and was skipped",
				"event_id", env.GetEventId(), "error", err)
		}
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
		c.metrics.Observe(ctx, OutcomeSkipped, 0)
		return nil
	}

	started := time.Now()

	// Tahap dua: kerjakan, dengan percobaan ulang DI DALAM PROSES.
	//
	// Pilihan ini disengaja, dan alternatifnya sudah dicoba lalu dibuang:
	// membiarkan offset tidak terkomit TIDAK membuat broker mengirim pesannya
	// lagi ke konsumen yang sama - ia hanya berpengaruh setelah rebalance atau
	// restart. Pekerjaan yang gagal akan berhenti selamanya di status failed,
	// batas tiga percobaan tidak pernah tercapai, dan antrean surat mati tidak
	// pernah menerima apa pun.
	//
	// Memundurkan offset lewat SetOffsets bisa dilakukan, tetapi franz-go
	// sendiri memperingatkan pemakaiannya di dalam loop PollFetches sebagai
	// "prone to odd interactions" [franz-go@v1.21.6/pkg/kgo/consumer.go:763-778].
	//
	// Jadi pesannya ditahan di sini sampai selesai atau menyerah. Partisinya
	// ikut tertahan selama itu - dan justru itu yang menjaga urutan per
	// agregat.
	return c.work(ctx, job, req, started)
}

// work mencoba pekerjaan sampai berhasil atau menyerah.
func (c *Consumer) work(
	ctx context.Context, job *Job, req *Request, started time.Time,
) error {
	for attempt := job.Attempts; attempt < MaxAttempts; attempt++ {
		answer, genErr := c.generate(ctx, req)
		if genErr == nil {
			c.quotaHits = 0
			err := c.recordSuccess(ctx, job, req, answer)
			if err == nil {
				c.metrics.Observe(ctx, OutcomeCompleted, time.Since(started))
			}
			return err
		}

		if ctx.Err() != nil {
			// Dimatikan di tengah percobaan. Klaimnya dilepas supaya pengiriman
			// berikutnya - setelah restart, dengan offset yang memang belum
			// dikomit - benar-benar mengerjakannya alih-alih melewatinya
			// sebagai duplikat.
			c.metrics.Observe(ctx, OutcomeAbandoned, time.Since(started))
			return c.abandon(ctx, job, genErr)
		}

		if errors.Is(genErr, llm.ErrRateLimited) {
			// Kuota, bukan kegagalan pekerjaan (ADR-025). Klaimnya dilepas
			// supaya pengiriman ulang mengerjakannya lagi, penghitung percobaan
			// tidak disentuh, dan loop Run yang memutuskan berapa lama diam.
			c.metrics.Observe(ctx, OutcomeParked, time.Since(started))
			if err := c.release(ctx, job); err != nil {
				return err
			}
			return &parkedError{cause: genErr}
		}

		dead := attempt+1 >= MaxAttempts
		if err := c.recordFailure(ctx, job, req, genErr, dead); err != nil {
			return err
		}
		if dead {
			c.metrics.Observe(ctx, OutcomeDead, time.Since(started))
			return nil
		}
		c.metrics.Observe(ctx, OutcomeFailed, time.Since(started))

		select {
		case <-ctx.Done():
			c.metrics.Observe(ctx, OutcomeAbandoned, time.Since(started))
			return c.abandon(ctx, job, genErr)
		case <-time.After(retryDelay(attempt)):
		}
	}
	return nil
}

// retryDelay adalah jeda sebelum percobaan berikutnya.
//
// Pendek, karena penyedianya sendiri sudah mencoba ulang dengan backoff yang
// lebih panjang di dalam. Yang ditangani di sini adalah kegagalan yang lolos
// dari lapisan itu - dan menunggu lama untuknya hanya menahan partisi.
func retryDelay(attempt int) time.Duration {
	return time.Duration(attempt+1) * 500 * time.Millisecond
}

// abandon melepas pekerjaan yang terhenti karena prosesnya dimatikan.
//
// Ia dijalankan dengan context terpisah: ctx pemanggil sudah dibatalkan, dan
// memakainya berarti pelepasannya sendiri gagal - meninggalkan klaim yang
// menutup kuncinya selamanya.
func (c *Consumer) abandon(ctx context.Context, job *Job, cause error) error {
	c.log.WarnContext(ctx, "a job was abandoned mid-flight and will be retried after restart",
		"job_id", job.ID, "attempts", job.Attempts, "error", cause)

	return c.release(ctx, job)
}

// release melepas klaim sebuah pekerjaan supaya pengiriman berikutnya
// mengerjakannya lagi, alih-alih dilewati sebagai duplikat.
//
// Context terpisah: pemanggilnya bisa datang dengan ctx yang sudah dibatalkan
// (shutdown), dan pelepasan yang gagal meninggalkan klaim yang menutup kuncinya
// selamanya.
func (c *Consumer) release(ctx context.Context, job *Job) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	return pg.InTx(releaseCtx, c.pool, func(q pg.Querier) error {
		guard, err := idempotency.NewGuard(q, Scope)
		if err != nil {
			return err
		}
		return guard.Release(releaseCtx, job.Key)
	})
}

// parkedError menandai pekerjaan yang dilepas karena kuota penyedia habis.
type parkedError struct{ cause error }

func (e *parkedError) Error() string {
	return "parked until the provider quota recovers: " + e.cause.Error()
}

func (e *parkedError) Unwrap() error { return e.cause }

// claim membuat pekerjaan baru bila kuncinya belum pernah dipakai.
func (c *Consumer) claim(
	ctx context.Context, key string, req *Request,
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

		// Pekerjaan yang sudah ada dengan kunci ini dipakai kembali, bukan
		// dibuat baru. Ia ada kalau percobaan sebelumnya terhenti di tengah -
		// proses mati saat sedang mencoba ulang - dan penghitung percobaannya
		// harus menumpuk di baris yang sama, kalau tidak batas tiga kali tidak
		// akan pernah tercapai.
		existing, found, err := c.jobs.ByKey(ctx, q, key)
		if err != nil {
			return err
		}
		if found {
			job = existing
			claimed = true
			return nil
		}

		job = &Job{
			Key:           key,
			Kind:          req.Kind,
			AggregateType: req.AggregateType,
			AggregateID:   req.AggregateID,
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
	ctx context.Context, req *Request,
) (*llm.Response, error) {
	// Templatnya dipilih permintaan, bukan ditetapkan di sini: satu worker
	// mengerjakan personalisasi, kurikulum, laporan kelulusan, dan balasan
	// chat, dan masing-masing punya prompt sendiri.
	tmpl, err := c.prompts.Latest(req.Template)
	if err != nil {
		return nil, err
	}

	// Data promptnya masih tipis: event-nya belum membawa profil dan riwayat.
	// Bidang yang belum ada dinyatakan APA ADANYA alih-alih diisi tebakan -
	// tebakan di sini akan sampai ke model sebagai fakta tentang seseorang.
	rendered, err := tmpl.Render(req.Data)
	if err != nil {
		return nil, err
	}

	// Panggilan ke penyedia adalah bagian terlama dari trace mana pun yang
	// melewati worker ini; ia diberi span sendiri supaya durasinya terbaca
	// terpisah dari klaim dan penyimpanan hasilnya.
	ctx, span := telemetry.StartSpan(ctx, "llm.generate",
		attribute.String("selaras.llm.provider", c.provider.Name()),
		attribute.String("selaras.llm.template", tmpl.ID()))
	answer, err := c.provider.Generate(ctx, llm.Request{
		Prompt:        rendered,
		PromptVersion: tmpl.ID(),
		JSON:          true,
		Temperature:   0.7,
	})
	if err == nil {
		// Token dicatat di tiga tempat yang masing-masing punya pembaca: span
		// (satu trace), metrik (agregat FinOps), dan log (satu pekerjaan).
		span.SetAttributes(
			attribute.Int("selaras.llm.tokens.input", answer.Usage.InputTokens),
			attribute.Int("selaras.llm.tokens.output", answer.Usage.OutputTokens),
			attribute.Int("selaras.llm.tokens.thoughts", answer.Usage.ThoughtsTokens))
		c.metrics.ObserveUsage(ctx, c.provider.Name(), tmpl.ID(), answer.Usage)
		c.log.InfoContext(ctx, "llm answer received",
			"provider", c.provider.Name(), "model", answer.Model, "template", tmpl.ID(),
			"tokens_input", answer.Usage.InputTokens, "tokens_output", answer.Usage.OutputTokens,
			"tokens_thoughts", answer.Usage.ThoughtsTokens, "finish_reason", answer.FinishReason)
	}
	telemetry.End(span, err)
	return answer, err
}

// recordSuccess menyimpan hasil dan menerbitkan event selesainya.
func (c *Consumer) recordSuccess(
	ctx context.Context, job *Job,
	req *Request, answer *llm.Response,
) error {
	if answer.Truncated() {
		// Jawaban terpotong bukan jawaban. Menyimpannya sebagai laporan utuh
		// akan menampilkan analisis setengah jadi seolah lengkap.
		// Jawaban terpotong tidak akan membaik dengan diulang: promptnya sama,
		// modelnya sama, batasnya sama. Ia langsung menyerah.
		return c.recordFailure(ctx, job, req,
			fmt.Errorf("%w: the provider stopped at %q", llm.ErrTruncated, answer.FinishReason), true)
	}

	return pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		if err := c.jobs.Complete(ctx, q, job.ID, job.CreatedAt,
			[]byte(answer.Text), answer.PromptVersion, answer.Model); err != nil {
			return err
		}

		return outbox.NewWriter(q).Write(ctx, req.AggregateType, req.AggregateID,
			completionEvent(job, req, answer))
	})
}

// completionEvent menyusun event hasil, sesuai jenis pekerjaannya.
//
// Jenis event yang berbeda mendarat di topic yang berbeda (lihat
// outbox.TopicFor), dan itu yang membuat konsumen assessment tidak perlu
// menyaring hasil coaching dan sebaliknya.
func completionEvent(job *Job, req *Request, answer *llm.Response) *eventsv1.Envelope {
	env := &eventsv1.Envelope{
		EventId:       job.ID.String(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
	}

	switch req.Kind {
	case KindCurriculum, KindGraduation:
		env.Payload = &eventsv1.Envelope_CurriculumCompleted{
			CurriculumCompleted: &eventsv1.CurriculumCompleted{
				ProgramId:      req.AggregateID,
				JobId:          job.ID.String(),
				CurriculumJson: answer.Text,
				PromptVersion:  answer.PromptVersion,
			},
		}

	case KindMealGuide:
		env.Payload = &eventsv1.Envelope_MealGuideCompleted{
			MealGuideCompleted: &eventsv1.MealGuideCompleted{
				GuideId:       req.AggregateID,
				JobId:         job.ID.String(),
				GuideJson:     answer.Text,
				PromptVersion: answer.PromptVersion,
			},
		}

	case KindChatReply:
		env.Payload = &eventsv1.Envelope_ChatReplyCompleted{
			ChatReplyCompleted: &eventsv1.ChatReplyCompleted{
				JobId:         job.ID.String(),
				ReplyJson:     answer.Text,
				PromptVersion: answer.PromptVersion,
			},
		}

	default:
		env.Payload = &eventsv1.Envelope_PersonalizationCompleted{
			PersonalizationCompleted: &eventsv1.PersonalizationCompleted{
				AssessmentId:  req.AggregateID,
				JobId:         job.ID.String(),
				ReportJson:    answer.Text,
				PromptVersion: answer.PromptVersion,
			},
		}
	}
	return env
}

// recordFailure mencatat kegagalan, dan menerbitkan event gagal saat pekerjaan
// itu sudah tidak akan dicoba lagi.
func (c *Consumer) recordFailure(
	ctx context.Context, job *Job,
	req *Request, cause error, dead bool,
) error {

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
		return outbox.NewWriter(q).Write(ctx, req.AggregateType, req.AggregateID, &eventsv1.Envelope{
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

// announceUnusable memberi tahu yang menunggu bahwa hasilnya tidak akan datang.
//
// Pesan yang jenisnya tidak dikenali tidak akan pernah bisa dikerjakan, dan
// melewatinya saja membuat agregat yang menunggunya menunggu SELAMANYA - tanpa
// galat, tanpa status yang berubah, tanpa apa pun selain satu baris log yang
// harus kebetulan dibaca seseorang.
//
// Itu bukan kemungkinan teoretis: ia terjadi saat nutrition-svc dinyalakan
// dengan llm-worker yang belum dibangun ulang. Worker lama tidak mengenal
// MealGuideRequested, melewatinya, dan panduan itu tetap pending selamanya.
// Kesenjangan versi seperti itu terjadi di setiap penggelaran bertahap.
//
// Yang diterbitkan adalah LlmJobFailed ke DLQ, dengan agregat diambil dari
// header dan kunci partisi pesannya - persis yang sudah dibaca setiap konsumen
// untuk mengenali kegagalan. Tanpa keduanya, tidak ada yang bisa diberi tahu,
// dan pesannya hanya dicatat.
func (c *Consumer) announceUnusable(
	ctx context.Context, env *eventsv1.Envelope, rec *kgo.Record, cause error,
) error {
	// Pesan yang tidak membawa agregat TIDAK diperiksa lagi di sini.
	//
	// outbox.Write sudah menolaknya dengan syarat yang sama persis, dan
	// menyalinnya ke sini hanya menghasilkan cabang kedua yang tidak bisa
	// dibedakan test mana pun - saya menulisnya, lalu mutasi membuktikan
	// menghapusnya tidak mengubah apa-apa. Satu tempat penegakan, bukan dua
	// yang akan menyimpang.
	//
	// Yang terjadi tanpa agregat: Write mengembalikan galat, galatnya dicatat
	// beserta offset dan event_id, dan offset tetap MAJU - worker ini memang
	// mengomit setelah setiap batch, karena percobaan ulangnya di dalam proses
	// (F3-13), bukan lewat pengiriman ulang. Antreannya tidak tersumbat.
	aggregateType := headerOf(rec, "aggregate_type")
	aggregateID := string(rec.Key)

	return pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		return outbox.NewWriter(q).Write(ctx, aggregateType, aggregateID, &eventsv1.Envelope{
			EventId:       uuid.NewString(),
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: 1,
			Payload: &eventsv1.Envelope_LlmJobFailed{
				LlmJobFailed: &eventsv1.LlmJobFailed{
					Reason: truncate("this worker does not understand the request: "+cause.Error(), 500),
				},
			},
		})
	})
}

// headerOf membaca satu header pesan.
func headerOf(rec *kgo.Record, key string) string {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
