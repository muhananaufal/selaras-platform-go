package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Publisher is what the relay needs from the broker, and nothing more.
//
// It is an interface so the relay can be tested without a broker, and so
// publishing failures can be made to happen on demand - which a real broker
// makes hard to do.
type Publisher interface {
	// Publish returns the indexes of the messages that were published
	// successfully.
	//
	// Per message, not one verdict for the whole batch: declaring a batch
	// failed as a whole when part of it was already accepted by the broker
	// would resend the successful part, and turn every transient failure into
	// an avoidable duplicate.
	Publish(ctx context.Context, msgs []kafka.Message) ([]int, error)
}

// RelayOptions mengatur ritme relay.
type RelayOptions struct {
	// Batch is the maximum number of events fetched per round.
	Batch int

	// Interval is the pause while the outbox is empty. While there is still
	// something in it, the relay goes straight into the next round - waiting
	// there only adds delay to work that is already waiting.
	Interval time.Duration

	// PublishTimeout bounds how long a single publish may wait.
	//
	// It exists because the Kafka client buffers and retries internally: with
	// the broker down, ProduceSync returns no error until its own retry budget
	// is exhausted - tens of seconds - and for all that time the relay hangs
	// without recording anything. Its outbox rows sit there with attempts at
	// zero and last_error empty, and whoever investigates finds no explanation
	// at all.
	//
	// This really happened: the F3-14 resilience test found it by killing a
	// real broker.
	PublishTimeout time.Duration
}

// Relay moves events from the outbox to the broker.
//
// Its guarantee is AT-LEAST-ONCE, and that is not a compromise that could
// have been avoided. Publishing to the broker and marking the row as sent
// are two different systems; one of them has to happen first:
//
//   - Mark first, then publish: a process that dies in between loses the
//     event FOREVER. Nothing will ever look for it again.
//   - Publish first, then mark: a process that dies in between publishes
//     it again when it comes back. A duplicate, not a loss.
//
// The second is what was chosen. A duplicate can be handled by its receiver
// through an idempotency key (F3-05); a loss cannot be handled by anyone.
type Relay struct {
	pool pg.Beginner
	pub  Publisher
	log  *slog.Logger
	opts RelayOptions
}

func NewRelay(pool pg.Beginner, pub Publisher, log *slog.Logger, opts RelayOptions) (*Relay, error) {
	if pool == nil {
		return nil, errors.New("nil pool")
	}
	if pub == nil {
		return nil, errors.New("nil publisher")
	}
	if log == nil {
		return nil, errors.New("nil logger")
	}
	if opts.Batch <= 0 {
		opts.Batch = 100
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.PublishTimeout <= 0 {
		opts.PublishTimeout = 10 * time.Second
	}
	return &Relay{pool: pool, pub: pub, log: log, opts: opts}, nil
}

// Run loops until ctx is done.
//
// It returns nil when stopped through ctx: a requested stop is not a
// failure, and reporting it as an error would make every clean shutdown
// look like a crash.
func (r *Relay) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "outbox relay started",
		"batch", r.opts.Batch, "interval", r.opts.Interval)

	for {
		moved, err := r.Once(ctx)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			r.log.InfoContext(ctx, "outbox relay stopped")
			return nil
		case err != nil:
			// One failed round does not kill the relay. A database that is
			// restarting or a broker that is electing a leader are transient
			// conditions, and a relay that died because of them would leave an
			// outbox piling up with nobody attending to it.
			r.log.ErrorContext(ctx, "outbox relay round failed", "error", err)
		}

		// Still something in it: go straight into the next round.
		if err == nil && moved >= r.opts.Batch {
			continue
		}

		select {
		case <-ctx.Done():
			r.log.InfoContext(ctx, "outbox relay stopped")
			return nil
		case <-time.After(r.opts.Interval):
		}
	}
}

// Once runs a single round and returns the number of events published.
//
// All of it inside ONE transaction. The FOR UPDATE SKIP LOCKED lock taken while
// reading only lasts for the transaction, so reading in one transaction and
// marking in another would release the lock between them - and a second relay
// would pick up the events the first is still sending.
func (r *Relay) Once(ctx context.Context) (int, error) {
	var moved int

	err := pg.InTx(ctx, r.pool, func(q pg.Querier) error {
		reader := NewReader(q)

		records, err := reader.Unpublished(ctx, r.opts.Batch)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}

		msgs, routable, unroutable := r.toMessages(records)

		// Events that cannot be routed will never be sendable. Bumping their
		// counter makes them findable; leaving them makes the relay read them
		// again every round, forever.
		if len(unroutable) > 0 {
			if err := reader.MarkFailed(ctx, unroutable, "no topic is defined for this event type"); err != nil {
				return err
			}
		}

		if len(msgs) == 0 {
			return nil
		}

		// Publishing is time-bounded, SEPARATELY from the transaction. Without
		// this bound, one round could hang for as long as the Kafka client
		// buffers and retries internally - and the failure would never be
		// recorded.
		pubCtx, cancelPub := context.WithTimeout(ctx, r.opts.PublishTimeout)
		sent, pubErr := r.pub.Publish(pubCtx, msgs)
		cancelPub()

		published := make([]uuid.UUID, 0, len(sent))
		for _, i := range sent {
			published = append(published, routable[i])
		}
		if err := reader.MarkPublished(ctx, published, time.Now()); err != nil {
			return err
		}
		moved = len(published)

		if pubErr != nil {
			// The failures are recorded, and then the error is SWALLOWED here on
			// purpose: returning it would roll back this transaction, and with it
			// the sent marks for the events that really did arrive. Those would be
			// resent for no reason.
			failed := make([]uuid.UUID, 0, len(routable)-len(published))
			ok := make(map[int]bool, len(sent))
			for _, i := range sent {
				ok[i] = true
			}
			for i, id := range routable {
				if !ok[i] {
					failed = append(failed, id)
				}
			}
			if err := reader.MarkFailed(ctx, failed, pubErr.Error()); err != nil {
				return err
			}
			r.log.WarnContext(ctx, "some outbox events could not be published",
				"published", len(published), "failed", len(failed), "error", pubErr)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("relaying a batch: %w", err)
	}
	return moved, nil
}

// toMessages maps outbox rows to Kafka messages.
//
// It returns three things: the messages, the row ids that correspond to them by
// index, and the ids of rows that cannot be routed at all.
func (r *Relay) toMessages(records []Record) (msgs []kafka.Message, routable, unroutable []uuid.UUID) {
	for _, rec := range records {
		topic, err := TopicFor(rec.EventType)
		if err != nil {
			unroutable = append(unroutable, rec.ID)
			continue
		}

		msgs = append(msgs, kafka.Message{
			Topic: topic,

			// The key is aggregate_id, so every event of one aggregate lands on the
			// same partition and its ordering is preserved.
			Key:   []byte(rec.AggregateID),
			Value: rec.Payload,
			Headers: map[string]string{
				"event_type":     rec.EventType,
				"aggregate_type": rec.AggregateType,
				"outbox_id":      rec.ID.String(),
			},
		})
		routable = append(routable, rec.ID)
	}
	return msgs, routable, unroutable
}
