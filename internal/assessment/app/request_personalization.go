package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// EventWriter writes events to the outbox.
//
// It is an interface so this use case imports no adapter, and so tests can
// inspect the events written without a database.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// EventWriterFor creates an event writer ON a single transaction.
//
// It is a factory, not a ready-made writer, and that is not needless
// complexity: a writer built on the connection pool would take its own
// connection and commit on its own, so its event would survive even when the
// business transaction was rolled back. This shape makes that mistake
// impossible to write.
type EventWriterFor func(pg.Querier) EventWriter

// StatusWriter writes the personalisation status inside a transaction.
//
// It is deliberately a narrow port, not the whole domain.Repository: this
// use case needs one operation, and a wider port would invite other writes
// into a transaction not intended for them.
type StatusWriter interface {
	SetPersonalizationStatus(
		ctx context.Context, id domain.ID,
		to domain.PersonalizationStatus, from []domain.PersonalizationStatus, failure string,
	) (bool, error)
}

// StatusWriterFor creates a status writer on a single transaction.
type StatusWriterFor func(pg.Querier) StatusWriter

// UnitOfWork runs a function inside one transaction.
//
// It is needed here because a personalisation request produces TWO writes that
// have to happen together: the mark that this assessment is being
// personalised, and the event requesting it. If either could happen without
// the other, the system would have an assessment waiting forever or a job
// nobody is waiting for.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(pg.Querier) error) error
}

// PersonalizationRequest is the request.
type PersonalizationRequest struct {
	Slug   string
	UserID string

	// IdempotencyKey comes from the caller. Empty means the key is derived
	// from the assessment, so two requests for the same assessment still
	// produce one job.
	IdempotencyKey string
}

// PersonalizationTicket is the answer returned immediately.
type PersonalizationTicket struct {
	JobID string

	// AlreadyRunning says this request started no new job because one is
	// already running or already done. The caller still gets the same job_id -
	// a repeated request is not an error.
	AlreadyRunning bool
}

// RequestPersonalization asks for a personalisation report to be produced.
//
// It does NOT call the LLM provider. It writes an event and returns - that is
// the whole reason this phase exists. Calling the provider from the request
// path means the user waits tens of seconds, one provider failure becomes an
// HTTP failure, and nothing can retry without the user pressing the button
// again.
func (s *Service) RequestPersonalization(
	ctx context.Context, uow UnitOfWork, events EventWriterFor, req PersonalizationRequest,
) (*PersonalizationTicket, error) {
	if uow == nil || events == nil || s.statusWriter == nil {
		// Accepting a request without any one of the three means returning a
		// ticket for work that will never be recorded or never be done. Refusing
		// is far more honest.
		return nil, errors.New("personalisation needs a unit of work, an event writer, and a status writer")
	}

	// Ownership is checked through the same path as an ordinary read: the
	// profile id is resolved from the already verified user_id, not accepted
	// from the caller (ADR-023).
	assessment, err := s.Get(ctx, req.Slug, req.UserID)
	if err != nil {
		return nil, err
	}

	if assessment.ResultDetails != nil {
		// The report already exists. Asking again is not wrong, but there is no
		// need to start a second paid job.
		return &PersonalizationTicket{
			JobID:          assessment.ID.String(),
			AlreadyRunning: true,
		}, nil
	}

	key := req.IdempotencyKey
	if key == "" {
		// Derived from the assessment, not randomised. A random key turns every
		// repeated request into a new job, and a user who presses the button
		// twice pays twice.
		key = "personalization:" + assessment.ID.String()
	}

	jobID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generating a job id: %w", err)
	}

	envelope := &eventsv1.Envelope{
		EventId:        jobID.String(),
		OccurredAt:     timestamppb.New(s.now()),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: key},
		Payload: &eventsv1.Envelope_PersonalizationRequested{
			PersonalizationRequested: &eventsv1.PersonalizationRequested{
				AssessmentId: assessment.ID.String(),
				Slug:         assessment.Slug,
				JobId:        jobID.String(),
			},
		},
	}

	// Two writes, one transaction: the mark that this assessment is being
	// worked on, and the event requesting it.
	//
	// If either could happen without the other, the system would have an
	// assessment waiting forever (status pending without an event) or a job
	// nobody is waiting for (an event without a status). The event writer is
	// built FROM this transaction, not used from outside it.
	if err := uow.Do(ctx, func(q pg.Querier) error {
		if _, err := s.statusWriter(q).SetPersonalizationStatus(ctx, assessment.ID,
			domain.PersonalizationPending,
			[]domain.PersonalizationStatus{domain.PersonalizationNotRequested, domain.PersonalizationFailed},
			""); err != nil {
			return err
		}
		return events(q).Write(ctx, "assessment", assessment.ID.String(), envelope)
	}); err != nil {
		return nil, fmt.Errorf("requesting personalisation: %w", err)
	}

	return &PersonalizationTicket{JobID: jobID.String()}, nil
}

// StorePersonalization stores the report that comes back from the worker
// (F3-11).
//
// It is idempotent with itself: an already stored report is not overwritten.
// Events can arrive twice - the outbox relay is at-least-once - and
// overwriting an existing report with the one arriving later would replace
// content the user may already have read.
func (s *Service) StorePersonalization(
	ctx context.Context, assessmentID string, report map[string]any,
) error {
	if len(report) == 0 {
		return errors.New("an empty report is not a report")
	}

	id, err := domain.ParseID(assessmentID)
	if err != nil {
		return err
	}

	stored, err := s.assessments.SetResultDetails(ctx, id, report)
	if err != nil {
		return err
	}
	if !stored {
		// Not an error: the report already exists, and that is the correct state.
		return nil
	}
	return nil
}

// RepositoryFor creates an assessment repository on a single transaction.
//
// It exists because Start has to write the assessment AND its announcement
// event in one transaction (E10). A repository built on the connection pool
// would commit on its own, and the assessment would survive even when its
// event was rolled back - the dashboard would then never learn the assessment
// exists.
type RepositoryFor func(pg.Querier) domain.Repository
