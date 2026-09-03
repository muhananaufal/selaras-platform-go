// Package consumer membaca konfirmasi penghapusan dari keenam unit.
package consumer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
)

// Confirmations membaca user.deletion dan menutup saga yang sudah lengkap.
type Confirmations struct {
	client *kgo.Client
	uc     *app.DeleteAccount
	log    *slog.Logger
}

func NewConfirmations(
	client *kgo.Client, uc *app.DeleteAccount, log *slog.Logger,
) (*Confirmations, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case uc == nil:
		return nil, errors.New("nil deletion use case")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Confirmations{client: client, uc: uc, log: log}, nil
}

// Run membaca sampai ctx selesai.
func (c *Confirmations) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "deletion confirmation consumer started")

	for {
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "deletion confirmation consumer stopped")
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan.
			return nil
		}

		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "deletion confirmation consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				c.log.ErrorContext(ctx, "fetching confirmations failed",
					"topic", e.Topic, "error", e.Err)
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
			if err := c.handle(ctx, rec); err != nil {
				c.log.ErrorContext(ctx, "handling a confirmation failed",
					"offset", rec.Offset, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			// Offset DITAHAN. Konfirmasi yang hilang berarti saga menggantung
			// selamanya, dan akun yang seharusnya terhapus tidak pernah
			// terhapus - tanpa siapa pun tahu unit mana yang jawabannya hilang.
			c.log.WarnContext(ctx, "holding offsets so failed confirmations are redelivered",
				"handled", handled)
			// Tidak mengomit saja TIDAK cukup: franz-go tidak mengirim ulang
			// apa pun di dalam sesi yang sama, jadi batch berikutnya akan
			// datang, berhasil, lalu mengomit SELURUH yang sudah dikonsumsi -
			// termasuk record yang gagal tadi. Konsumen dimundurkan ke sana.
			rewinder.Rewind(c.client)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
			c.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

func (c *Confirmations) handle(ctx context.Context, rec *kgo.Record) error {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		c.log.ErrorContext(ctx, "a confirmation could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	confirmed := env.GetUserDeletionConfirmed()
	if confirmed == nil {
		// Permintaan penghapusan lewat di topic yang sama - identity-svc
		// menerbitkannya sendiri. Ia bukan urusan konsumen ini.
		return nil
	}

	if confirmed.GetSagaId() == "" || confirmed.GetService() == "" {
		c.log.ErrorContext(ctx, "a confirmation named no saga or no service",
			"event_id", env.GetEventId())
		return nil
	}

	return c.uc.ConfirmDeletion(ctx, confirmed.GetSagaId(), domain.Confirmation{
		Service:       confirmed.GetService(),
		Succeeded:     confirmed.GetSucceeded(),
		FailureReason: confirmed.GetFailureReason(),

		// Waktu PERISTIWANYA, bukan waktu pemrosesannya. Yang kedua membuat
		// urutan konfirmasi bergantung pada kapan konsumen ini kebetulan
		// membacanya.
		ConfirmedAt: env.GetOccurredAt().AsTime(),
	})
}
