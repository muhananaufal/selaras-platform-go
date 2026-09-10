package consumer

import (
	"context"
	"errors"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// AssessmentScope is the idempotency scope of this consumer.
const AssessmentScope = "coaching-assessments"

// Assessments reads assessment.completed and records the soft reference
// (F4-06), so StartProgram can resolve a slug without calling
// assessment-svc.
type Assessments struct {
	client *kgo.Client
	svc    *app.Service
	log    *slog.Logger
}

func NewAssessments(client *kgo.Client, svc *app.Service, log *slog.Logger) (*Assessments, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case svc == nil:
		return nil, errors.New("nil coaching service")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Assessments{client: client, svc: svc, log: log}, nil
}

// Run reads until ctx is done.
func (a *Assessments) Run(ctx context.Context) error {
	return loop(ctx, a.client, a.log, "coaching assessment", a.handle)
}

// handle records one event.
func (a *Assessments) handle(ctx context.Context, rec *kgo.Record) (err error) {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		a.log.ErrorContext(ctx, "an assessment event could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	payload, ok := env.GetPayload().(*eventsv1.Envelope_AssessmentCompleted)
	if !ok {
		// Other events on this topic are not this consumer's business; skipped,
		// not failed.
		return nil
	}
	done := payload.AssessmentCompleted

	recorded, err := a.svc.RecordAssessment(ctx, app.RecordAssessmentCommand{
		AssessmentID: done.GetAssessmentId(),
		UserID:       done.GetUserId(),
		Slug:         done.GetSlug(),
		// The snapshot read back by the program view (adapter/grpc/mapping.go):
		// the keys are kept the same there.
		Snapshot: map[string]any{
			"slug":            done.GetSlug(),
			"risk_percentage": done.GetRiskPercentage(),
			"risk_category":   done.GetRiskCategory(),
			"model_used":      done.GetModelUsed(),
		},
		CompletedAt: env.GetOccurredAt().AsTime(),
	})
	switch {
	case errors.Is(err, domain.ErrInvalidAssessment):
		// A malformed event will not heal by being retried; holding the offset
		// for it means this consumer rewinds itself forever.
		a.log.ErrorContext(ctx, "a malformed assessment event was dropped",
			"event_id", env.GetEventId(), "error", err)
		return nil
	case err != nil:
		return err
	case !recorded:
		a.log.DebugContext(ctx, "an assessment event arrived again and was ignored",
			"assessment_id", done.GetAssessmentId())
	}
	return nil
}
