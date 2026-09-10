// Package consumer reads arriving menu guides and changed languages.
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
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Scope is the idempotency scope of this consumer.
const Scope = "nutrition-results"

// Results reads menu guide results and profile updates.
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

// isMine says this message is nutrition's business.
//
// The llm.results and llm.dlq topics are SHARED by every service that uses
// llm-worker. Without this filter, the nutrition consumer would try to store a
// coaching curriculum as a menu guide - fail, hold the offset, and clog the
// queue for everyone. That really happened when coaching was added.
//
// The kind is read from the aggregate_type header the outbox relay fills in,
// without unpacking the content and without guessing from its shape.
//
// profile.updated is a separate topic and is NOT shared with anyone, so a
// message there is always the business of every subscriber. It is recognised
// through the same header. The value is "user_profile" - read from
// internal/profile/app/publish.go, not guessed from the topic name: guessing
// "profile" would make EVERY language update be silently skipped, and the cache
// would never fill without a single visible error.
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

	// Without the header, the kind is unknown. It is SKIPPED, not accepted:
	// accepting it means guessing, and a wrong guess writes someone else's
	// guide.
	return false
}

// Run membaca sampai ctx selesai.
func (r *Results) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "nutrition result consumer started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "nutrition result consumer stopped")
			//nolint:nilerr // A requested stop is not a failure.
			return nil
		}

		fetches := r.client.PollFetches(ctx)
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "nutrition result consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			// A topic recreated on the broker (B26): resubscribed here, not through
			// a restart. franz-go deliberately does not recover on its own.
			if recovered := kafka.RecoverRecreatedTopics(r.client, errs); len(recovered) > 0 {
				r.log.WarnContext(ctx, "topics were recreated on the broker; subscribed again", "topics", recovered)
			}
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

		var handled int
		rewinder := kafka.NewRewinder()

		fetches.EachRecord(func(rec *kgo.Record) {
			if ctx.Err() != nil {
				return
			}
			if err := r.handle(ctx, rec); err != nil {
				r.log.ErrorContext(ctx, "handling a nutrition result failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			// The offset is HELD so the failed one is redelivered. Skipping it means
			// that guide waits forever, and nobody knows.
			r.log.WarnContext(ctx, "holding offsets so failed results are redelivered",
				"handled", handled)
			// Not committing alone is NOT enough: franz-go redelivers nothing within
			// the same session, so the next batch would arrive, succeed, and commit
			// EVERYTHING consumed so far - including the record that just failed.
			// The consumer is rewound to it instead.
			rewinder.Rewind(r.client)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := r.client.CommitUncommittedOffsets(ctx); err != nil {
			r.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// handle processes one message.
func (r *Results) handle(ctx context.Context, rec *kgo.Record) (err error) {
	if !isMine(rec) {
		return nil
	}

	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		r.log.ErrorContext(ctx, "a nutrition result could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	// The consumer span becomes a child of the request that wrote this event
	// (F9-05); an error returned by the handler is recorded on its span.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	err = r.dispatch(ctx, &env, rec)
	if terminal(err) {
		// A result for something that no longer exists. Retrying it will never
		// succeed, and holding the offset for it means this consumer rewinds
		// itself every second, forever - that really happened after a test
		// account was deleted, and the trace is what exposed it.
		r.log.WarnContext(ctx, "a result arrived for a guide that no longer exists and was dropped",
			"event_id", env.GetEventId(), "error", err)
		return nil
	}
	return err
}

// terminal says an error will not heal by retrying.
//
// Only a missing owner. Other errors - Postgres unreachable, a transaction
// conflict - are still returned so the offset is held and the result comes
// back.
func terminal(err error) bool {
	return errors.Is(err, domain.ErrGuideNotFound)
}

// dispatch routes one event to its handling.
func (r *Results) dispatch(ctx context.Context, env *eventsv1.Envelope, rec *kgo.Record) error {
	switch payload := env.GetPayload().(type) {
	case *eventsv1.Envelope_MealGuideCompleted:
		return r.complete(ctx, env, payload.MealGuideCompleted)

	case *eventsv1.Envelope_LlmJobFailed:
		return r.fail(ctx, env, payload.LlmJobFailed, rec)

	case *eventsv1.Envelope_ProfileUpdated:
		return r.cacheLanguage(ctx, env, payload.ProfileUpdated)

	default:
		// Other events are not this consumer's business. They are skipped, not
		// failed - failing them keeps the offset from advancing and clogs the
		// whole queue with messages that were never its own.
		return nil
	}
}

// complete stores an arriving guide (F6-07).
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
		// An unreadable answer will never become readable. Retrying it forever
		// only clogs the queue, so the guide is marked FAILED - not left pending
		// forever, and not stored as-is as menu advice made of broken JSON.
		r.log.ErrorContext(ctx, "a completed meal guide was not valid JSON",
			"guide_id", guideID, "event_id", env.GetEventId())
		return r.svc.FailGuide(ctx, guideID)
	}

	return r.svc.StoreGuide(ctx, guideID, raw)
}

// fail marks a guide that will never arrive.
func (r *Results) fail(
	ctx context.Context, env *eventsv1.Envelope, failed *eventsv1.LlmJobFailed, rec *kgo.Record,
) error {
	// The guide id comes from the partition key, which the relay fills from
	// the aggregate_id of its outbox row.
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

// cacheLanguage copies the user's language into the cache.
func (r *Results) cacheLanguage(
	ctx context.Context, env *eventsv1.Envelope, updated *eventsv1.ProfileUpdated,
) error {
	userID := updated.GetUserId()
	if userID == "" {
		// An event carrying only a profile id cannot be looked up through the
		// identity verified on every request (ADR-023). It is logged, not
		// guessed.
		r.log.ErrorContext(ctx, "a profile update carried no user id",
			"event_id", env.GetEventId())
		return nil
	}

	observedAt := env.GetOccurredAt().AsTime()
	if observedAt.IsZero() {
		// Without the event time, the "never go backwards" guard has nothing to
		// compare against, and a replay would restore the old language.
		r.log.ErrorContext(ctx, "a profile update carried no timestamp",
			"event_id", env.GetEventId(), "user_id", userID)
		return nil
	}

	return cache.NewLanguages(r.pool).Remember(ctx, userID, updated.GetLanguage(), observedAt)
}
