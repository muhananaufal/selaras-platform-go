// Package consumer memproyeksikan event domain menjadi read-model dasbor.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/app"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
)

// Scope adalah ruang lingkup idempotensi proyektor ini.
const Scope = "dashboard-projector"

// Projector membaca event domain dan memperbarui read-model.
//
// Ia menggantikan empat listener invalidasi cache di sistem lama. Bedanya bukan
// jumlahnya: listener menghapus cache dan berharap yang berikutnya membacanya
// ulang dengan benar, sementara proyektor MENULIS bentuk yang akan dibaca.
// Yang pertama gagal diam-diam saat seseorang lupa menambahkan listener kelima;
// yang kedua gagal terlihat, karena bentuknya tidak akan pernah terisi.
type Projector struct {
	client *kgo.Client
	svc    *app.Service
	log    *slog.Logger
}

func NewProjector(client *kgo.Client, svc *app.Service, log *slog.Logger) (*Projector, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case svc == nil:
		return nil, errors.New("nil dashboard service")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Projector{client: client, svc: svc, log: log}, nil
}

// Run membaca sampai ctx selesai.
func (p *Projector) Run(ctx context.Context) error {
	p.log.InfoContext(ctx, "dashboard projector started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			p.log.InfoContext(ctx, "dashboard projector stopped")
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan.
			return nil
		}

		fetches := p.client.PollFetches(ctx)
		if ctx.Err() != nil {
			p.log.InfoContext(ctx, "dashboard projector stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				p.log.ErrorContext(ctx, "fetching events failed",
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
		rewinder := kafka.NewRewinder()

		fetches.EachRecord(func(rec *kgo.Record) {
			if ctx.Err() != nil {
				return
			}
			if err := p.handle(ctx, rec); err != nil {
				p.log.ErrorContext(ctx, "projecting an event failed",
					"topic", rec.Topic, "offset", rec.Offset,
					"partition", rec.Partition, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			// Offset DITAHAN. Proyeksi yang melewatkan satu event akan salah
			// SELAMANYA - tidak ada yang mengirimnya lagi, dan tidak ada yang
			// menyadarinya sampai seseorang membandingkan dasbor dengan
			// sumbernya. Pengiriman ulang aman: proyeksinya idempoten.
			p.log.WarnContext(ctx, "holding offsets so failed events are redelivered",
				"handled", handled)
			// Tidak mengomit saja TIDAK cukup: franz-go tidak mengirim ulang
			// apa pun di dalam sesi yang sama, jadi batch berikutnya akan
			// datang, berhasil, lalu mengomit SELURUH yang sudah dikonsumsi -
			// termasuk record yang gagal tadi. Konsumen dimundurkan ke sana.
			rewinder.Rewind(p.client)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := p.client.CommitUncommittedOffsets(ctx); err != nil {
			p.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// handle memproyeksikan satu event.
func (p *Projector) handle(ctx context.Context, rec *kgo.Record) error {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		// Pesan yang tidak bisa dibaca tidak akan pernah bisa dibaca. Ia
		// dilewati dan dicatat, bukan menahan offset selamanya.
		p.log.ErrorContext(ctx, "an event could not be decoded and was skipped",
			"topic", rec.Topic, "offset", rec.Offset, "error", err)
		return nil
	}

	occurredAt := env.GetOccurredAt().AsTime()
	if occurredAt.IsZero() {
		// Tanpa waktu peristiwa, penjaga urutan tidak punya apa pun untuk
		// dibandingkan dan pengukuran lag kehilangan dasarnya.
		p.log.ErrorContext(ctx, "an event carried no timestamp and was skipped",
			"event_id", env.GetEventId(), "topic", rec.Topic)
		return nil
	}

	switch payload := env.GetPayload().(type) {
	case *eventsv1.Envelope_AssessmentCompleted:
		return p.projectAssessment(ctx, &env, payload.AssessmentCompleted, occurredAt)

	case *eventsv1.Envelope_CoachingProgramUpdated:
		return p.projectProgram(ctx, &env, payload.CoachingProgramUpdated, occurredAt)

	case *eventsv1.Envelope_UserDeletionRequested:
		return p.forget(ctx, &env, payload.UserDeletionRequested)

	default:
		// Event lain di topic yang sama bukan urusan proyeksi ini. Ia dilewati,
		// bukan digagalkan - menggagalkannya menahan offset dan menyumbat
		// antrean dengan pesan yang memang bukan miliknya.
		return nil
	}
}

func (p *Projector) projectAssessment(
	ctx context.Context, env *eventsv1.Envelope,
	done *eventsv1.AssessmentCompleted, occurredAt time.Time,
) error {
	if done.GetUserId() == "" {
		// Event tanpa pemilik tidak bisa diproyeksikan ke baris siapa pun. Ia
		// dicatat, bukan ditebak - menebaknya berarti menulis penilaian
		// seseorang ke dasbor orang lain.
		p.log.ErrorContext(ctx, "a completed assessment named no user",
			"event_id", env.GetEventId(), "assessment_id", done.GetAssessmentId())
		return nil
	}
	if done.GetSlug() == "" {
		// Slug adalah kunci idempotensinya. Tanpa itu, pengiriman ulang tidak
		// bisa dikenali.
		p.log.ErrorContext(ctx, "a completed assessment carried no slug",
			"event_id", env.GetEventId(), "user_id", done.GetUserId())
		return nil
	}

	return p.svc.ProjectAssessment(ctx, done.GetUserId(), &domain.Assessment{
		Slug: done.GetSlug(),
		// Waktu penilaiannya adalah waktu PERISTIWANYA, bukan waktu
		// pemrosesannya. Yang kedua akan membuat seluruh riwayat yang dibangun
		// ulang bertanggal hari pembangunan ulang itu.
		AssessedAt:     occurredAt,
		RiskPercentage: done.GetRiskPercentage(),
		RiskCategory:   done.GetRiskCategory(),
		ModelUsed:      done.GetModelUsed(),
	}, occurredAt)
}

func (p *Projector) projectProgram(
	ctx context.Context, env *eventsv1.Envelope,
	updated *eventsv1.CoachingProgramUpdated, occurredAt time.Time,
) error {
	if updated.GetUserId() == "" {
		p.log.ErrorContext(ctx, "a program update named no user",
			"event_id", env.GetEventId(), "program_id", updated.GetProgramId())
		return nil
	}

	program := &domain.Program{
		Slug:       updated.GetSlug(),
		Title:      updated.GetTitle(),
		Status:     updated.GetStatus(),
		CurrentDay: int(updated.GetCurrentDay()),
		TotalDays:  int(updated.GetTotalDays()),
	}

	// Presence eksplisit: nil berarti event ini tidak menghitung tugas, dan
	// angka yang sudah tersimpan dibiarkan. GetCompletionPercentage akan
	// mengembalikan nol untuk keduanya, jadi bidangnya diperiksa langsung.
	if updated.CompletionPercentage != nil {
		completion := updated.GetCompletionPercentage()
		program.Completion = &completion
	}

	return p.svc.ProjectProgram(ctx, updated.GetUserId(), program, occurredAt)
}

// forget menghapus proyeksi saat akun dihapus.
//
// Read-model memuat salinan data pribadi - persentase risiko, kategori
// kesehatan, riwayat analisis. Salinan yang tertinggal setelah akun dihapus
// adalah data pribadi yang tidak seorang pun tahu masih ada.
func (p *Projector) forget(
	ctx context.Context, env *eventsv1.Envelope, req *eventsv1.UserDeletionRequested,
) error {
	if req.GetUserId() == "" {
		p.log.ErrorContext(ctx, "a deletion request named no user",
			"event_id", env.GetEventId(), "saga_id", req.GetSagaId())
		return nil
	}
	return p.svc.Forget(ctx, req.GetUserId())
}

// Drain memproyeksikan seluruh yang tersedia lalu berhenti (F7-05).
//
// Berbeda dari Run yang berjalan selamanya, Drain berhenti setelah idle
// berlalu tanpa satu pesan pun. Kafka tidak punya "sudah sampai akhir" yang
// bisa ditanyakan konsumen tanpa menebak; yang ada hanya "tidak ada lagi yang
// datang", dan batas itu diserahkan pemanggilnya karena broker yang lambat
// membutuhkan lebih lama.
//
// Ia mengembalikan jumlah pesan yang DIBACA, bukan yang diterapkan: pesan yang
// dilewati karena bukan urusan proyeksi ini tetap dibaca, dan angka yang
// menyembunyikannya membuat "kenapa hanya sekian" tidak bisa dijawab.
func (p *Projector) Drain(ctx context.Context, idle time.Duration) (int, error) {
	var read int
	lastMessage := time.Now()

	for {
		if ctx.Err() != nil {
			return read, ctx.Err()
		}
		if time.Since(lastMessage) > idle {
			p.log.InfoContext(ctx, "no more events arrived; the rebuild is complete",
				"messages_read", read, "idle_for", idle)
			return read, nil
		}

		// Poll dengan batas waktunya sendiri, supaya diamnya broker tidak
		// menahan seluruh perintah selamanya.
		pollCtx, cancel := context.WithTimeout(ctx, idle)
		fetches := p.client.PollFetches(pollCtx)
		cancel()

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				if errors.Is(e.Err, context.DeadlineExceeded) || errors.Is(e.Err, context.Canceled) {
					continue
				}
				return read, fmt.Errorf("fetching %s: %w", e.Topic, e.Err)
			}
		}

		var batch int
		var failure error
		fetches.EachRecord(func(rec *kgo.Record) {
			if failure != nil {
				return
			}
			// Kegagalan MENGHENTIKAN pembangunan ulang, tidak seperti Run yang
			// menahan offset dan mencoba lagi. Proyeksi yang dibangun ulang
			// separuh lalu dilaporkan selesai adalah kebohongan yang tidak
			// terlihat sampai seseorang membandingkannya dengan sumbernya.
			if err := p.handle(ctx, rec); err != nil {
				failure = fmt.Errorf("projecting %s offset %d: %w", rec.Topic, rec.Offset, err)
				return
			}
			batch++
		})
		if failure != nil {
			return read, failure
		}

		if batch > 0 {
			read += batch
			lastMessage = time.Now()
		}
	}
}
