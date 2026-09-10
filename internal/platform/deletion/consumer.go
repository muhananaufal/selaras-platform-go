// Package deletion runs a unit's side of the account-deletion saga.
//
// The protocol is the same in all six units: read the request, delete that
// user's data, announce the confirmation. Only the deletion itself differs,
// and that is the one thing the caller supplies.
//
// Written once here rather than copied six times, and the reason is not
// brevity: six copies mean six chances that one of them stops confirming after
// a failure, or confirms success when it failed. The first leaves every saga
// hanging; the second declares an account deleted while its data is still
// there.
package deletion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Eraser deletes all of one user's data in a unit.
//
// It takes a Querier, not a connection pool: the deletion and its confirmation
// are written in ONE transaction. If the two could come apart, a unit could
// confirm success while its deletion was rolled back - and the account would
// be declared deleted with its data intact.
//
// userProfileID MAY be empty: a profile that was never created is a valid
// state (B7), and a unit whose data is keyed by profile has to handle it by
// deleting nothing rather than failing.
//
// It MUST be idempotent. The outbox relay is at-least-once, and the same
// request can arrive twice.
type Eraser func(ctx context.Context, q pg.Querier, userID, userProfileID string) error

// Consumer reads user.deletion and deletes this unit's data.
type Consumer struct {
	client  *kgo.Client
	pool    pg.Beginner
	service string
	erase   Eraser
	log     *slog.Logger
}

// NewConsumer assembles the deletion consumer for one unit.
//
// service is the name of this unit, and it MUST exactly match one of the
// names in identity/domain.DeletionParticipants. A name that does not match
// makes identity-svc refuse the confirmation, and the saga hangs forever
// waiting for a unit that has in fact finished.
func NewConsumer(
	client *kgo.Client, pool pg.Beginner, service string, erase Eraser, log *slog.Logger,
) (*Consumer, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case pool == nil:
		return nil, errors.New("nil connection pool")
	case service == "":
		return nil, errors.New("a deletion consumer needs the name of its own unit")
	case erase == nil:
		return nil, errors.New("nil eraser")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Consumer{client: client, pool: pool, service: service, erase: erase, log: log}, nil
}

// Run reads until ctx is done.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "deletion consumer started", "service", c.service)

	for {
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "deletion consumer stopped", "service", c.service)
			//nolint:nilerr // A requested stop is not a failure.
			return nil
		}

		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "deletion consumer stopped", "service", c.service)
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
				c.log.ErrorContext(ctx, "fetching deletion requests failed",
					"service", c.service, "topic", e.Topic, "error", e.Err)
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
				c.log.ErrorContext(ctx, "handling a deletion request failed",
					"service", c.service, "offset", rec.Offset, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			// The consumer is REWOUND to the failed request, not merely left
			// uncommitted.
			//
			// Not committing alone is NOT enough: franz-go does not resend anything
			// within the same session, so the next batch would arrive, succeed, and
			// commit everything consumed so far - including the request that failed.
			// That really happened: a deletion request was silently skipped past by
			// five confirmations passing on the same topic.
			c.log.WarnContext(ctx, "rewinding so failed deletions are redelivered",
				"service", c.service, "handled", handled)
			rewinder.Rewind(c.client)

			// A short pause so a failure that keeps repeating does not become a
			// tight loop flooding the log and the broker.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
			c.log.ErrorContext(ctx, "committing offsets failed",
				"service", c.service, "error", err)
		}
	}
}

// handle processes one deletion request.
func (c *Consumer) handle(ctx context.Context, rec *kgo.Record) (err error) {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		c.log.ErrorContext(ctx, "a deletion request could not be decoded and was skipped",
			"service", c.service, "offset", rec.Offset, "error", err)
		return nil
	}

	// The consumer span becomes a child of the request that wrote this event
	// (F9-05); an error returned by the handler is recorded on the span.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	req := env.GetUserDeletionRequested()
	if req == nil {
		// Other events on the same topic are none of this consumer's business -
		// confirmations from other units pass through here too.
		return nil
	}

	if req.GetSagaId() == "" || req.GetUserId() == "" {
		// Without both, there is nothing to delete and nobody to report to. It is
		// logged, not guessed.
		c.log.ErrorContext(ctx, "a deletion request named no saga or no user",
			"service", c.service, "event_id", env.GetEventId())
		return nil
	}

	// The deletion AND its confirmation in one transaction.
	//
	// This is the core of this saga's reliability. A confirmation that commits
	// without its deletion declares the account deleted with its data intact -
	// a lie that will never be noticed, because nothing looks for it any more.
	err = pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		if err := c.erase(ctx, q, req.GetUserId(), req.GetUserProfileId()); err != nil {
			return err
		}
		return outbox.NewWriter(q).Write(ctx, "user", req.GetUserId(),
			confirmed(req.GetSagaId(), c.service, nil))
	})
	if err == nil {
		c.log.InfoContext(ctx, "deleted this unit's data for a user",
			"service", c.service, "saga_id", req.GetSagaId())
		return nil
	}

	// A failed deletion is STILL confirmed - as a failure.
	//
	// Silence is the worst option: the saga hangs with nobody knowing which
	// unit is at fault, and all that is left is one log line someone would
	// have to happen to read. A failed confirmation ends the saga with status
	// failed, holds the account, and records the reason in the place that is
	// actually read when resolving it.
	c.log.ErrorContext(ctx, "could not delete this unit's data; reporting the failure",
		"service", c.service, "saga_id", req.GetSagaId(), "error", err)

	reportErr := pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		return outbox.NewWriter(q).Write(ctx, "user", req.GetUserId(),
			confirmed(req.GetSagaId(), c.service, err))
	})
	if reportErr != nil {
		// Even reporting the failure failed. The offset is held so the request
		// comes again - that is the only path left.
		return fmt.Errorf("deletion failed (%w) and reporting it also failed: %w", err, reportErr)
	}
	return nil
}

// confirmed composes the confirmation, for success and failure alike.
func confirmed(sagaID, service string, cause error) *eventsv1.Envelope {
	payload := &eventsv1.UserDeletionConfirmed{
		SagaId:    sagaID,
		Service:   service,
		Succeeded: cause == nil,
	}
	if cause != nil {
		// The reason is truncated: it goes into a column read by people, not into
		// an error store. A long pgx error can carry the whole SQL statement with
		// its parameters - and the parameter on this path is a user id.
		reason := truncate(cause.Error(), 500)
		payload.FailureReason = &reason
	}

	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
		Payload:       &eventsv1.Envelope_UserDeletionConfirmed{UserDeletionConfirmed: payload},
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
