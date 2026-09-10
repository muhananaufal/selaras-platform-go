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
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Scope is the idempotency scope of this projector.
const Scope = "dashboard-projector"

// Projector reads domain events and updates the read-model.
//
// It replaces four cache-invalidation listeners in the legacy system. The
// difference is not the count: a listener deletes the cache and hopes the next
// reader rebuilds it correctly, while a projector WRITES the shape that will be
// read. The first fails silently when someone forgets to add a fifth listener;
// the second fails visibly, because the shape never gets filled.
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
			//nolint:nilerr // A requested stop is not a failure.
			return nil
		}

		fetches := p.client.PollFetches(ctx)
		if ctx.Err() != nil {
			p.log.InfoContext(ctx, "dashboard projector stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			// A topic recreated on the broker (B26): resubscribed here, not through
			// a restart. franz-go deliberately does not recover on its own.
			if recovered := kafka.RecoverRecreatedTopics(p.client, errs); len(recovered) > 0 {
				p.log.WarnContext(ctx, "topics were recreated on the broker; subscribed again", "topics", recovered)
			}
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
			// The offset is HELD. A projection that skips one event is wrong FOREVER
			// - nothing sends it again, and nobody notices until someone compares
			// the dashboard with its source. Redelivery is safe: the projection is
			// idempotent.
			p.log.WarnContext(ctx, "holding offsets so failed events are redelivered",
				"handled", handled)
			// Not committing alone is NOT enough: franz-go redelivers nothing within
			// the same session, so the next batch would arrive, succeed, and commit
			// EVERYTHING consumed so far - including the record that just failed.
			// The consumer is rewound to it instead.
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
func (p *Projector) handle(ctx context.Context, rec *kgo.Record) (err error) {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		// An unreadable message will never become readable. It is skipped and
		// logged, rather than holding the offset forever.
		p.log.ErrorContext(ctx, "an event could not be decoded and was skipped",
			"topic", rec.Topic, "offset", rec.Offset, "error", err)
		return nil
	}

	// The consumer span becomes a child of the request that wrote this event
	// (F9-05); an error returned by the handler is recorded on its span.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	occurredAt := env.GetOccurredAt().AsTime()
	if occurredAt.IsZero() {
		// Without the event time, the ordering guard has nothing to compare
		// against and the lag measurement loses its basis.
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
		// Other events on the same topic are not this projection's business. They
		// are skipped, not failed - failing them holds the offset and clogs the
		// queue with messages that were never its own.
		return nil
	}
}

func (p *Projector) projectAssessment(
	ctx context.Context, env *eventsv1.Envelope,
	done *eventsv1.AssessmentCompleted, occurredAt time.Time,
) error {
	if done.GetUserId() == "" {
		// An event without an owner cannot be projected onto anyone's row. It is
		// logged, not guessed - guessing means writing someone's assessment onto
		// someone else's dashboard.
		p.log.ErrorContext(ctx, "a completed assessment named no user",
			"event_id", env.GetEventId(), "assessment_id", done.GetAssessmentId())
		return nil
	}
	if done.GetSlug() == "" {
		// The slug is its idempotency key. Without it, a redelivery cannot be
		// recognised.
		p.log.ErrorContext(ctx, "a completed assessment carried no slug",
			"event_id", env.GetEventId(), "user_id", done.GetUserId())
		return nil
	}

	return p.svc.ProjectAssessment(ctx, done.GetUserId(), &domain.Assessment{
		Slug: done.GetSlug(),
		// The assessment time is the time of the EVENT, not of its processing.
		// The latter would date an entirely rebuilt history on the day of the
		// rebuild.
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

	// Explicit presence: nil means this event did not count tasks, and the
	// stored number is left alone. GetCompletionPercentage would return zero
	// for both, so the field is checked directly.
	if updated.CompletionPercentage != nil {
		completion := updated.GetCompletionPercentage()
		program.Completion = &completion
	}

	return p.svc.ProjectProgram(ctx, updated.GetUserId(), program, occurredAt)
}

// forget removes the projection when an account is deleted.
//
// The read-model holds copies of personal data - risk percentages, health
// categories, analysis history. A copy left behind after the account is
// deleted is personal data nobody knows still exists.
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

// Drain projects everything available and then stops (F7-05).
//
// Unlike Run, which runs forever, Drain stops once idle has passed without a
// single message. Kafka has no "reached the end" a consumer can ask about
// without guessing; there is only "nothing more is coming", and that bound is
// left to the caller because a slow broker needs longer.
//
// It returns the number of messages READ, not applied: messages skipped
// because they are not this projection's business are still read, and a number
// that hides them makes "why only this many" unanswerable.
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

		// A poll with its own deadline, so a silent broker does not hold the
		// whole command forever.
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
			// A failure STOPS the rebuild, unlike Run which holds the offset and
			// retries. A projection rebuilt halfway and then reported complete is a
			// lie invisible until someone compares it with its source.
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
