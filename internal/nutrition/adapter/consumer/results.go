// Package consumer membaca panduan menu yang tiba dan bahasa yang berubah.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/cache"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/app"
)

// Scope adalah ruang lingkup idempotensi konsumen ini.
const Scope = "nutrition-results"

// Results membaca hasil panduan menu dan pembaruan profil.
type Results struct {
	client *kgo.Client
	svc    *app.Service
	pool   *pgxpool.Pool
	log    *slog.Logger
}

func NewResults(
	client *kgo.Client, svc *app.Service, pool *pgxpool.Pool, log *slog.Logger,
) (*Results, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case svc == nil:
		return nil, errors.New("nil nutrition service")
	case pool == nil:
		return nil, errors.New("nil connection pool")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Results{client: client, svc: svc, pool: pool, log: log}, nil
}

// isMine menyatakan pesan ini urusan nutrition.
//
// Topic llm.results dan llm.dlq dipakai BERSAMA seluruh service yang memakai
// llm-worker. Tanpa penyaringan ini, konsumen nutrition akan mencoba menyimpan
// kurikulum coaching sebagai panduan menu - gagal, menahan offset, dan
// menyumbat antrean untuk semua orang. Itu benar-benar terjadi saat coaching
// ditambahkan.
//
// Jenisnya dibaca dari header aggregate_type yang diisi relay outbox, tanpa
// membongkar isinya dan tanpa menebak dari bentuknya.
//
// profile.updated adalah topic terpisah dan TIDAK dibagi dengan siapa pun,
// sehingga pesan di sana selalu urusan setiap pelanggannya. Ia dikenali lewat
// header yang sama. Nilainya "user_profile" - dibaca dari internal/profile/app/
// publish.go, bukan ditebak dari nama topicnya: menebaknya "profile" akan
// membuat SETIAP pembaruan bahasa dilewati diam-diam, dan cache-nya tidak
// pernah terisi tanpa satu pun galat yang terlihat.
func isMine(rec *kgo.Record) bool {
	for _, h := range rec.Headers {
		if h.Key != "aggregate_type" {
			continue
		}
		switch string(h.Value) {
		case "meal_guide", "user_profile":
			return true
		default:
			return false
		}
	}

	// Tanpa header, jenisnya tidak diketahui. Ia DILEWATI, bukan diterima:
	// menerimanya berarti menebak, dan tebakan yang salah menulis panduan orang
	// lain.
	return false
}

// Run membaca sampai ctx selesai.
func (r *Results) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "nutrition result consumer started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "nutrition result consumer stopped")
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan.
			return nil
		}

		fetches := r.client.PollFetches(ctx)
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "nutrition result consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				r.log.ErrorContext(ctx, "fetching nutrition results failed",
					"topic", e.Topic, "partition", e.Partition, "error", e.Err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		var handled, failed int
		fetches.EachRecord(func(rec *kgo.Record) {
			if ctx.Err() != nil {
				return
			}
			if err := r.handle(ctx, rec); err != nil {
				r.log.ErrorContext(ctx, "handling a nutrition result failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
				failed++
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if failed > 0 {
			// Offset DITAHAN supaya yang gagal dikirim ulang. Melewatinya
			// berarti panduan itu menunggu selamanya, dan tidak ada yang tahu.
			r.log.WarnContext(ctx, "holding offsets so failed results are redelivered",
				"failed", failed, "handled", handled)
			continue
		}

		if err := r.client.CommitUncommittedOffsets(ctx); err != nil {
			r.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// handle memproses satu pesan.
func (r *Results) handle(ctx context.Context, rec *kgo.Record) error {
	if !isMine(rec) {
		return nil
	}

	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		r.log.ErrorContext(ctx, "a nutrition result could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	switch payload := env.GetPayload().(type) {
	case *eventsv1.Envelope_MealGuideCompleted:
		return r.complete(ctx, &env, payload.MealGuideCompleted)

	case *eventsv1.Envelope_LlmJobFailed:
		return r.fail(ctx, &env, payload.LlmJobFailed, rec)

	case *eventsv1.Envelope_ProfileUpdated:
		return r.cacheLanguage(ctx, &env, payload.ProfileUpdated)

	default:
		// Event lain bukan urusan konsumen ini. Ia dilewati, bukan digagalkan -
		// menggagalkannya membuat offset tidak maju dan seluruh antrean
		// tersumbat oleh pesan yang memang bukan miliknya.
		return nil
	}
}

// complete menyimpan panduan yang tiba (F6-07).
func (r *Results) complete(
	ctx context.Context, env *eventsv1.Envelope, done *eventsv1.MealGuideCompleted,
) error {
	guideID := done.GetGuideId()
	if guideID == "" {
		r.log.ErrorContext(ctx, "a completed meal guide named no guide",
			"event_id", env.GetEventId())
		return nil
	}

	raw := json.RawMessage(done.GetGuideJson())
	if len(raw) == 0 || !json.Valid(raw) {
		// Jawaban yang tidak bisa dibaca tidak akan pernah bisa dibaca.
		// Mengulanginya selamanya hanya menyumbat antrean, jadi panduannya
		// ditandai GAGAL - bukan dibiarkan pending selamanya, dan bukan pula
		// disimpan apa adanya sebagai saran menu berupa JSON rusak.
		r.log.ErrorContext(ctx, "a completed meal guide was not valid JSON",
			"guide_id", guideID, "event_id", env.GetEventId())
		return r.svc.FailGuide(ctx, guideID)
	}

	return r.svc.StoreGuide(ctx, guideID, raw)
}

// fail menandai panduan yang tidak akan pernah tiba.
func (r *Results) fail(
	ctx context.Context, env *eventsv1.Envelope, failed *eventsv1.LlmJobFailed, rec *kgo.Record,
) error {
	// Id panduan datang dari kunci partisi, yang diisi relay dari aggregate_id
	// baris outbox-nya.
	guideID := string(rec.Key)
	if guideID == "" {
		r.log.ErrorContext(ctx, "a failed job carried no guide key",
			"event_id", env.GetEventId(), "job_id", failed.GetJobId())
		return nil
	}

	r.log.WarnContext(ctx, "a meal guide will never arrive",
		"guide_id", guideID, "job_id", failed.GetJobId(), "reason", failed.GetReason())

	return r.svc.FailGuide(ctx, guideID)
}

// cacheLanguage menyalin bahasa pengguna ke cache.
func (r *Results) cacheLanguage(
	ctx context.Context, env *eventsv1.Envelope, updated *eventsv1.ProfileUpdated,
) error {
	userID := updated.GetUserId()
	if userID == "" {
		// Event yang hanya membawa id profil tidak bisa dicari lewat identitas
		// yang terverifikasi di setiap permintaan (ADR-023). Ia dicatat, bukan
		// ditebak.
		r.log.ErrorContext(ctx, "a profile update carried no user id",
			"event_id", env.GetEventId())
		return nil
	}

	observedAt := env.GetOccurredAt().AsTime()
	if observedAt.IsZero() {
		// Tanpa waktu event, penjaga "jangan mundur" tidak punya apa pun untuk
		// dibandingkan, dan pemutaran ulang akan mengembalikan bahasa lama.
		r.log.ErrorContext(ctx, "a profile update carried no timestamp",
			"event_id", env.GetEventId(), "user_id", userID)
		return nil
	}

	return cache.NewLanguages(r.pool).Remember(ctx, userID, updated.GetLanguage(), observedAt)
}
