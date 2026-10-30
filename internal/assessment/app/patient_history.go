package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Errors of a clinician's read (ADR-030).
var (
	// ErrNotPermitted: the caller holds no consent to read this patient.
	ErrNotPermitted = errors.New("no consent to read this patient's assessments")

	// ErrInvalidUserID: the clinician or patient id is not a user id.
	ErrInvalidUserID = errors.New("invalid user id")

	// ErrAccessUnavailable: whether the caller may read could not be settled
	// - OpenFGA was unreachable, no checker is configured, or the read could
	// not be recorded. The answer is then no: a patient's data is never
	// returned on a maybe (fail closed).
	ErrAccessUnavailable = errors.New("access to patient data cannot be checked right now")
)

// AccessChecker answers whether user has relation to object (authz.Client).
type AccessChecker interface {
	Check(ctx context.Context, user, relation, object string) (bool, error)
}

// WithAccessChecker sets the checker for clinicians' reads. Without one,
// every such read is refused.
func (s *Service) WithAccessChecker(c AccessChecker) *Service {
	s.access = c
	return s
}

// resourceAssessments names this read in the access audit.
const resourceAssessments = "risk_assessments"

// PatientHistory is a clinician reading a patient's assessment history
// under the patient's consent (ADR-030).
//
// The order is the point:
//  1. the input is validated - a malformed request is not an access;
//  2. OpenFGA is asked, with higher consistency, whether the caller may
//     read this patient's assessments; anything but yes refuses;
//  3. the read is recorded in the outbox, committed, before anything is
//     read - a read whose record cannot be kept is refused;
//  4. only then is the history read.
//
// The caller is the authenticated principal, never a field of the request:
// the caller's id comes in as clinicianID from the verified token.
func (s *Service) PatientHistory(
	ctx context.Context, uow UnitOfWork, events EventWriterFor,
	clinicianID, patientID string, pageSize int, pageToken string,
) (HistoryPage, error) {
	for _, id := range []string{clinicianID, patientID} {
		if _, err := uuid.Parse(id); err != nil {
			return HistoryPage{}, fmt.Errorf("%w: %q", ErrInvalidUserID, id)
		}
	}
	size, after, err := pageOf(pageSize, pageToken)
	if err != nil {
		return HistoryPage{}, err
	}

	if s.access == nil {
		return HistoryPage{}, fmt.Errorf("%w: no access checker is configured", ErrAccessUnavailable)
	}
	ok, err := s.access.Check(ctx, "user:"+clinicianID, "can_view_assessments", "patient:"+patientID)
	if err != nil {
		return HistoryPage{}, fmt.Errorf("%w: %w", ErrAccessUnavailable, err)
	}
	if !ok {
		return HistoryPage{}, ErrNotPermitted
	}

	if uow == nil || events == nil {
		return HistoryPage{}, fmt.Errorf("%w: the read cannot be recorded", ErrAccessUnavailable)
	}
	record := accessRecorded(clinicianID, patientID, s.now())
	if err := uow.Do(ctx, func(q pg.Querier) error {
		return events(q).Write(ctx, "patient", patientID, record)
	}); err != nil {
		return HistoryPage{}, fmt.Errorf("%w: recording the read: %w", ErrAccessUnavailable, err)
	}

	return s.patientHistory(ctx, patientID, size, after)
}

// patientHistory reads the patient's history by the patient's user id, with
// no profile lookup at all.
//
// Resolving the profile id first is what went wrong before: the general
// profile source calls profile-svc with the caller's token - the clinician's,
// which profile-svc refuses for another user - and the event-fed cache alone
// lags behind a profile just completed, so a patient with an assessment read
// as having none. The assessment carries its owner's user id (migration
// 0008), so the read needs nothing but the id the consent was checked for.
func (s *Service) patientHistory(ctx context.Context, patientID string, size int, after *domain.HistoryCursor) (HistoryPage, error) {
	return page(size, func(limit int) ([]*domain.Assessment, error) {
		return s.assessments.ListForUser(ctx, patientID, limit, after)
	})
}

func accessRecorded(clinicianID, patientID string, now time.Time) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_ClinicianAccessRecorded{
			ClinicianAccessRecorded: &eventsv1.ClinicianAccessRecorded{
				ClinicianUserId: clinicianID,
				PatientUserId:   patientID,
				Resource:        resourceAssessments,
				AccessedAt:      timestamppb.New(now),
			},
		},
	}
}
