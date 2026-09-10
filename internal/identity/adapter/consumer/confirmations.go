// Package consumer reads deletion confirmations from the six units.
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
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Confirmations reads user.deletion and closes sagas that are complete.
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

// Run reads until ctx is done.
func (c *Confirmations) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "deletion confirmation consumer started")

	for {
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "deletion confirmation consumer stopped")
			//nolint:nilerr // A requested stop is not a failure.
			return nil
		}

		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "deletion confirmation consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			// A topic recreated on the broker (B26): resubscribed here, not through
			// a restart. franz-go deliberately does not recover on its own.
			if recovered := kafka.RecoverRecreatedTopics(c.client, errs); len(recovered) > 0 {
				c.log.WarnContext(ctx, "topics were recreated on the broker; subscribed again", "topics", recovered)
			}
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
			// The offset is HELD. A lost confirmation means the saga hangs forever,
			// and an account that should have been deleted never is - with nobody
			// knowing which unit's answer went missing.
			c.log.WarnContext(ctx, "holding offsets so failed confirmations are redelivered",
				"handled", handled)
			// Not committing alone is NOT enough: franz-go does not resend anything
			// within the same session, so the next batch would arrive, succeed, and
			// commit EVERYTHING consumed so far - including the record that failed.
			// The consumer is rewound to it.
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

func (c *Confirmations) handle(ctx context.Context, rec *kgo.Record) (err error) {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		c.log.ErrorContext(ctx, "a confirmation could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	// The consumer span becomes a child of the request that wrote this event
	// (F9-05); an error returned by the handler is recorded on the span.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	confirmed := env.GetUserDeletionConfirmed()
	if confirmed == nil {
		// Deletion requests pass on the same topic - identity-svc publishes them
		// itself. They are none of this consumer's business.
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

		// The EVENT's time, not the processing time. The latter would make the
		// order of confirmations depend on when this consumer happened to read
		// them.
		ConfirmedAt: env.GetOccurredAt().AsTime(),
	})
}
