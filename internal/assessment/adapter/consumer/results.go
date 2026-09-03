// Package consumer membaca hasil pekerjaan LLM dan menyimpannya.
//
// Ia sisi penerima dari alur yang dimulai di RequestPersonalization: permintaan
// keluar lewat outbox, worker mengerjakannya, hasilnya kembali lewat topic
// llm.results, dan di sinilah ia mendarat.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/idempotency"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Scope adalah ruang lingkup idempotensi konsumen ini.
//
// Ia berbeda dari milik llm-worker dengan sengaja: dua konsumen yang memproses
// event yang sama tidak boleh saling meniadakan. Worker yang sudah menangani
// sebuah event tidak berarti penyimpan hasil juga sudah.
const Scope = "assessment-results"

// Results membaca llm.results dan menyimpan laporannya.
type Results struct {
	client   *kgo.Client
	pool     pg.Beginner
	svc      *app.Service
	statuses app.StatusWriterFor
	log      *slog.Logger
}

func NewResults(
	client *kgo.Client,
	pool pg.Beginner,
	svc *app.Service,
	statuses app.StatusWriterFor,
	log *slog.Logger,
) (*Results, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case pool == nil:
		return nil, errors.New("nil pool")
	case svc == nil:
		return nil, errors.New("nil assessment service")
	case statuses == nil:
		return nil, errors.New("nil status writer")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Results{client: client, pool: pool, svc: svc, statuses: statuses, log: log}, nil
}

// Run membaca sampai ctx selesai.
func (r *Results) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "assessment result consumer started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "assessment result consumer stopped")
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan.
			return nil
		}

		fetches := r.client.PollFetches(ctx)
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "assessment result consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				r.log.ErrorContext(ctx, "fetching results failed",
					"topic", e.Topic, "partition", e.Partition, "error", e.Err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		var handled int
		fetches.EachRecord(func(rec *kgo.Record) {
			if ctx.Err() != nil {
				return
			}
			if err := r.handle(ctx, rec); err != nil {
				r.log.ErrorContext(ctx, "handling a result failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
			}
			handled++
		})

		if handled == 0 {
			continue
		}

		// Offset dikomit setelah pekerjaannya selesai, bukan berdasarkan waktu.
		if err := r.client.CommitUncommittedOffsets(ctx); err != nil {
			r.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// handle memproses satu hasil.
func (r *Results) handle(ctx context.Context, rec *kgo.Record) error {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		r.log.ErrorContext(ctx, "a result could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	switch payload := env.GetPayload().(type) {
	case *eventsv1.Envelope_PersonalizationCompleted:
		return r.complete(ctx, &env, payload.PersonalizationCompleted)
	case *eventsv1.Envelope_LlmJobFailed:
		return r.fail(ctx, &env, payload.LlmJobFailed, rec)
	default:
		// Event lain di topic ini bukan urusan konsumen ini. Ia dilewati, bukan
		// digagalkan - menggagalkannya akan membuat offset tidak maju dan
		// seluruh antrean tersumbat oleh pesan yang memang bukan miliknya.
		return nil
	}
}

// complete menyimpan laporannya.
func (r *Results) complete(
	ctx context.Context, env *eventsv1.Envelope, done *eventsv1.PersonalizationCompleted,
) error {
	if done.GetAssessmentId() == "" {
		r.log.ErrorContext(ctx, "a completed result named no assessment",
			"event_id", env.GetEventId())
		return nil
	}

	var report map[string]any
	if err := json.Unmarshal([]byte(done.GetReportJson()), &report); err != nil {
		// Laporan yang tidak bisa dibaca tidak akan pernah bisa dibaca. Ia
		// dicatat sebagai kegagalan, bukan diulang selamanya - dan penilaiannya
		// ditandai failed, bukan dibiarkan pending selamanya.
		r.log.ErrorContext(ctx, "a completed result was not valid JSON",
			"assessment_id", done.GetAssessmentId(), "error", err)
		return r.markFailed(ctx, done.GetAssessmentId(), "the worker returned a report that is not valid JSON")
	}

	key := idempotencyKeyOf(env)

	return pg.InTx(ctx, r.pool, func(q pg.Querier) error {
		guard, err := idempotency.NewGuard(q, Scope)
		if err != nil {
			return err
		}

		claimed, err := guard.Claim(ctx, key)
		if err != nil {
			return err
		}
		if !claimed {
			// Sudah pernah disimpan. Relay outbox at-least-once, jadi pesan
			// yang tiba dua kali adalah keadaan yang normal.
			return nil
		}

		if err := r.svc.StorePersonalization(ctx, done.GetAssessmentId(), report); err != nil {
			return err
		}

		id, err := domain.ParseID(done.GetAssessmentId())
		if err != nil {
			return err
		}

		// Status dan laporannya ditulis di transaksi yang sama. Kalau salah
		// satunya bisa terjadi tanpa yang lain, klien melihat laporan yang ada
		// dengan status pending - atau status completed tanpa laporan.
		if _, err := r.statuses(q).SetPersonalizationStatus(ctx, id,
			domain.PersonalizationCompleted, nil, ""); err != nil {
			return err
		}
		return nil
	})
}

// fail menandai pekerjaan yang sudah menyerah.
func (r *Results) fail(
	ctx context.Context, env *eventsv1.Envelope, failed *eventsv1.LlmJobFailed, rec *kgo.Record,
) error {
	// LlmJobFailed tidak membawa id penilaian; yang membawanya adalah kunci
	// partisi pesannya, yang diisi relay dari aggregate_id baris outbox.
	assessmentID := string(rec.Key)
	if assessmentID == "" {
		r.log.ErrorContext(ctx, "a failure event carried no assessment key",
			"event_id", env.GetEventId(), "job_id", failed.GetJobId())
		return nil
	}

	r.log.WarnContext(ctx, "a personalisation job gave up",
		"assessment_id", assessmentID, "job_id", failed.GetJobId(), "reason", failed.GetReason())

	return r.markFailed(ctx, assessmentID, failed.GetReason())
}

// markFailed menandai penilaian sebagai gagal dipersonalisasi.
//
// Perpindahannya dibatasi dari pending saja: pekerjaan yang sudah selesai tidak
// boleh berubah menjadi gagal karena event lama yang tiba terlambat.
func (r *Results) markFailed(ctx context.Context, assessmentID, reason string) error {
	id, err := domain.ParseID(assessmentID)
	if err != nil {
		return err
	}

	return pg.InTx(ctx, r.pool, func(q pg.Querier) error {
		_, err := r.statuses(q).SetPersonalizationStatus(ctx, id,
			domain.PersonalizationFailed,
			[]domain.PersonalizationStatus{domain.PersonalizationPending},
			reason)
		return err
	})
}

// idempotencyKeyOf memilih kunci yang menahan duplikat.
func idempotencyKeyOf(env *eventsv1.Envelope) string {
	if key := env.GetIdempotencyKey().GetValue(); key != "" {
		return key
	}
	return fmt.Sprintf("event:%s", env.GetEventId())
}
