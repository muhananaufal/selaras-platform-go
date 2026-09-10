// Package consumer reads LLM job results and stores them.
//
// It is the receiving side of the flow that starts in RequestPersonalization:
// the request leaves through the outbox, the worker does the work, the result
// comes back through the llm.results topic, and this is where it lands.
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
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/cache"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/idempotency"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Scope is this consumer's idempotency scope.
//
// It differs from llm-worker's on purpose: two consumers processing the same
// event must not cancel each other out. The worker having handled an event
// does not mean the result store has too.
const Scope = "assessment-results"

// Results reads llm.results and stores the reports.
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

// Run reads until ctx is done.
func (r *Results) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "assessment result consumer started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "assessment result consumer stopped")
			//nolint:nilerr // A requested stop is not a failure.
			return nil
		}

		fetches := r.client.PollFetches(ctx)
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "assessment result consumer stopped")
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

		// Offsets are committed once the work is done, not on a timer.
		if err := r.client.CommitUncommittedOffsets(ctx); err != nil {
			r.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// isMine reports whether this message belongs to assessment.
//
// The llm.results and llm.dlq topics are SHARED by every service that uses
// llm-worker. Without this filter, the assessment consumer would try to
// treat a coaching program as an assessment.
//
// The kind is read from the aggregate_type header the outbox relay fills in
// - without unpacking the payload, and without guessing from its shape.
func isMine(rec *kgo.Record) bool {
	for _, h := range rec.Headers {
		if h.Key != "aggregate_type" {
			continue
		}
		switch string(h.Value) {
		case "assessment":
			// Personalisation results and failures.
			return true
		case "user_profile":
			// The profile event that fills the cache (F2-16). It comes from
			// profile-svc through a different topic, but passes through the same
			// handler.
			return true
		default:
			return false
		}
	}

	// Without the header, the kind is unknown. It is SKIPPED, not accepted:
	// accepting it means guessing, and a wrong guess marks someone else's
	// assessment.
	return false
}

// handle processes one result.
func (r *Results) handle(ctx context.Context, rec *kgo.Record) (err error) {
	// Filtered first, before anything is unpacked.
	if !isMine(rec) {
		return nil
	}

	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		r.log.ErrorContext(ctx, "a result could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	// The consumer span becomes a child of the request that wrote this event
	// (F9-05); an error returned by the handler is recorded on the span.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	switch payload := env.GetPayload().(type) {
	case *eventsv1.Envelope_ProfileUpdated:
		return r.cacheProfile(ctx, &env, payload.ProfileUpdated)
	case *eventsv1.Envelope_PersonalizationCompleted:
		return r.complete(ctx, &env, payload.PersonalizationCompleted)
	case *eventsv1.Envelope_LlmJobFailed:
		return r.fail(ctx, &env, payload.LlmJobFailed, rec)
	default:
		// Other events on this topic are none of this consumer's business. They
		// are skipped, not failed - failing them would keep the offset from
		// advancing and block the whole queue with messages that were never its
		// own.
		return nil
	}
}

// complete stores the report.
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
		// A report that cannot be read will never be readable. It is recorded as
		// a failure, not retried forever - and the assessment is marked failed,
		// not left pending forever.
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
			// Already stored. The outbox relay is at-least-once, so a message
			// arriving twice is a normal state.
			return nil
		}

		if err := r.svc.StorePersonalization(ctx, done.GetAssessmentId(), report); err != nil {
			return err
		}

		id, err := domain.ParseID(done.GetAssessmentId())
		if err != nil {
			return err
		}

		// The status and the report are written in the same transaction. If
		// either could happen without the other, the client would see an existing
		// report with status pending - or status completed with no report.
		if _, err := r.statuses(q).SetPersonalizationStatus(ctx, id,
			domain.PersonalizationCompleted, nil, ""); err != nil {
			return err
		}
		return nil
	})
}

// fail marks a job that has given up.
func (r *Results) fail(
	ctx context.Context, env *eventsv1.Envelope, failed *eventsv1.LlmJobFailed, rec *kgo.Record,
) error {
	// LlmJobFailed does not carry the assessment id; what carries it is the
	// message's partition key, which the relay fills from the outbox row's
	// aggregate_id.
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

// markFailed marks an assessment as failed to personalise.
//
// The transition is restricted to pending only: a job already completed must
// not turn into failed because of an old event arriving late.
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

// cacheProfile stores a profile snapshot that arrived through an event
// (F2-16).
//
// It does NOT use the idempotency guard, and that is deliberate: this store is
// idempotent by shape - an UPSERT that only wins when the event is newer. An
// idempotency claim here would only add rows to sweep in order to hold back
// something already held back.
func (r *Results) cacheProfile(
	ctx context.Context, env *eventsv1.Envelope, updated *eventsv1.ProfileUpdated,
) error {
	if updated.GetUserId() == "" || updated.GetUserProfileId() == "" {
		// An event missing either id cannot be stored as a snapshot that can be
		// looked up. It is skipped and logged, not retried forever.
		r.log.ErrorContext(ctx, "a profile event was missing an id and was skipped",
			"event_id", env.GetEventId())
		return nil
	}

	observedAt := env.GetOccurredAt().AsTime()

	return pg.InTx(ctx, r.pool, func(q pg.Querier) error {
		stored, err := cache.NewProfiles(q).Store(ctx,
			updated.GetUserId(), updated.GetUserProfileId(),
			updated.DateOfBirth, sexOf(updated), updated.CountryOfResidence,
			updated.GetLanguage(), observedAt)
		if err != nil {
			return err
		}
		if !stored {
			// An event older than what is already stored. Not an error: a replayed
			// consumer produces many of these.
			r.log.DebugContext(ctx, "a profile event was older than the cached snapshot",
				"user_id", updated.GetUserId())
		}
		return nil
	})
}

// sexOf returns a pointer to the sex, or nil when it is not stated.
//
// The difference is real in the cache: NULL means "not filled in", an empty
// string means "known to be empty" - and the first may be looked up again at
// profile-svc.
func sexOf(updated *eventsv1.ProfileUpdated) *string {
	if updated.GetSex() == "" {
		return nil
	}
	sex := updated.GetSex()
	return &sex
}

// idempotencyKeyOf picks the key that holds back duplicates.
func idempotencyKeyOf(env *eventsv1.Envelope) string {
	if key := env.GetIdempotencyKey().GetValue(); key != "" {
		return key
	}
	return fmt.Sprintf("event:%s", env.GetEventId())
}
