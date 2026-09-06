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

// AssessmentScope adalah ruang lingkup idempotensi konsumen ini.
const AssessmentScope = "coaching-assessments"

// Assessments membaca assessment.completed dan mencatat rujukan lunaknya
// (F4-06), supaya StartProgram bisa meresolusi slug tanpa memanggil
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

// Run membaca sampai ctx selesai.
func (a *Assessments) Run(ctx context.Context) error {
	return loop(ctx, a.client, a.log, "coaching assessment", a.handle)
}

// handle mencatat satu event.
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
		// Event lain di topic ini bukan urusan konsumen ini; dilewati, bukan
		// digagalkan.
		return nil
	}
	done := payload.AssessmentCompleted

	recorded, err := a.svc.RecordAssessment(ctx, app.RecordAssessmentCommand{
		AssessmentID: done.GetAssessmentId(),
		UserID:       done.GetUserId(),
		Slug:         done.GetSlug(),
		// Cuplikan yang dibaca kembali oleh tampilan program
		// (adapter/grpc/mapping.go): kuncinya dijaga sama di sana.
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
		// Event yang cacat tidak akan sembuh dengan diulang; menahan offset
		// untuknya berarti konsumen ini memundurkan diri selamanya.
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
