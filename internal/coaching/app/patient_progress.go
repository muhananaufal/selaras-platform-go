package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// Errors of a clinician's read (ADR-030).
var (
	// ErrNotPermitted: the caller holds no consent to read this patient.
	ErrNotPermitted = errors.New("no consent to read this patient's coaching progress")

	// ErrAccessUnavailable: whether the caller may read could not be settled
	// - OpenFGA was unreachable, no checker is configured, or the read could
	// not be recorded. The answer is then no: a patient's data is never
	// returned on a maybe (fail closed).
	ErrAccessUnavailable = errors.New("access to patient data cannot be checked right now")

	ErrInvalidPageSize  = errors.New("page size must not be negative")
	ErrInvalidPageToken = errors.New("invalid page token")
)

const (
	// AIP-158: unset means the default, more than the maximum is lowered to
	// it, negative is an error.
	defaultProgressPageSize = 20
	maxProgressPageSize     = 100

	// resourceCoachingProgress names this read in the access audit; clinic's
	// schema accepts exactly this value.
	resourceCoachingProgress = "coaching_progress"
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

// ProgramProgress is what a clinician sees of one program: the program's
// status and dates, and counts - never its discussions or task wording.
type ProgramProgress struct {
	Program        *domain.Program
	Weeks          []domain.WeekProgress
	TasksTotal     int
	TasksCompleted int
}

// ProgressPage is one page of a patient's programs, newest first.
type ProgressPage struct {
	Programs []ProgramProgress

	// NextPageToken is empty on the last page, and only there.
	NextPageToken string
}

// PatientProgress is a clinician reading a patient's coaching progress
// under the patient's consent (ADR-030).
//
// The order is the point:
//  1. the input is validated - a malformed request is not an access;
//  2. OpenFGA is asked, with higher consistency, whether the caller may
//     read this patient's coaching progress; anything but yes refuses;
//  3. the read is recorded in the outbox, committed, before anything is
//     read - a read whose record cannot be kept is refused;
//  4. only then are the programs read.
//
// The caller is the authenticated principal, never a field of the request:
// clinicianID comes from the verified token.
func (s *Service) PatientProgress(
	ctx context.Context, clinicianID, patientID string, pageSize int, pageToken string,
) (ProgressPage, error) {
	if _, err := domain.ParseUserID(clinicianID); err != nil {
		return ProgressPage{}, err
	}
	patient, err := domain.ParseUserID(patientID)
	if err != nil {
		return ProgressPage{}, err
	}
	size, after, err := progressPageOf(pageSize, pageToken)
	if err != nil {
		return ProgressPage{}, err
	}

	if s.access == nil {
		return ProgressPage{}, fmt.Errorf("%w: no access checker is configured", ErrAccessUnavailable)
	}
	ok, err := s.access.Check(ctx, "user:"+clinicianID, "can_view_coaching_progress", "patient:"+patientID)
	if err != nil {
		return ProgressPage{}, fmt.Errorf("%w: %w", ErrAccessUnavailable, err)
	}
	if !ok {
		return ProgressPage{}, ErrNotPermitted
	}

	record := accessRecorded(clinicianID, patientID, s.now())
	if err := s.uow.Do(ctx, func(r Repositories) error {
		return r.Events().Write(ctx, "patient", patientID, record)
	}); err != nil {
		return ProgressPage{}, fmt.Errorf("%w: recording the read: %w", ErrAccessUnavailable, err)
	}

	return s.progress(ctx, patient, size, after)
}

// progress reads one page of programs and their weekly counts: two
// queries for the page, not one per program.
func (s *Service) progress(
	ctx context.Context, patient domain.UserID, size int, after *domain.ProgramCursor,
) (ProgressPage, error) {
	// One row more than the page says whether another page exists, without a
	// count and without handing out a token to a page that turns out empty.
	programs, err := s.programs.ListForUser(ctx, patient, size+1, after)
	if err != nil {
		return ProgressPage{}, err
	}
	var next string
	if len(programs) > size {
		programs = programs[:size]
		last := programs[len(programs)-1]
		if next, err = encodeProgressToken(domain.ProgramCursor{CreatedAt: last.CreatedAt, ID: last.ID}); err != nil {
			return ProgressPage{}, err
		}
	}

	ids := make([]domain.ID, 0, len(programs))
	for _, p := range programs {
		ids = append(ids, p.ID)
	}
	weekly, err := s.curricula.WeeklyProgress(ctx, ids)
	if err != nil {
		return ProgressPage{}, err
	}

	out := ProgressPage{Programs: make([]ProgramProgress, 0, len(programs)), NextPageToken: next}
	for _, p := range programs {
		pp := ProgramProgress{Program: p, Weeks: weekly[p.ID]}
		for _, w := range pp.Weeks {
			pp.TasksTotal += w.Total
			pp.TasksCompleted += w.Completed
		}
		out.Programs = append(out.Programs, pp)
	}
	return out, nil
}

func progressPageOf(pageSize int, pageToken string) (int, *domain.ProgramCursor, error) {
	switch {
	case pageSize < 0:
		return 0, nil, ErrInvalidPageSize
	case pageSize == 0:
		pageSize = defaultProgressPageSize
	case pageSize > maxProgressPageSize:
		pageSize = maxProgressPageSize
	}
	if pageToken == "" {
		return pageSize, nil, nil
	}
	cursor, err := decodeProgressToken(pageToken)
	if err != nil {
		return 0, nil, err
	}
	return pageSize, &cursor, nil
}

// The page token is base64url of JSON, opaque to clients. It is not signed:
// the patient is checked on every page, so a crafted cursor only moves
// inside a history the caller may already read.
type progressToken struct {
	CreatedAt string `json:"t"`
	ID        string `json:"i"`
}

func encodeProgressToken(c domain.ProgramCursor) (string, error) {
	// RFC 3339 with nanoseconds loses nothing: Postgres keeps microseconds,
	// and the cursor has to match the stored value exactly or the next page
	// would repeat or skip the row it ended on.
	raw, err := json.Marshal(progressToken{CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano), ID: c.ID.String()})
	if err != nil {
		return "", fmt.Errorf("encoding page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeProgressToken(token string) (domain.ProgramCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return domain.ProgramCursor{}, fmt.Errorf("%w: not base64url", ErrInvalidPageToken)
	}
	var t progressToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return domain.ProgramCursor{}, fmt.Errorf("%w: not a page position", ErrInvalidPageToken)
	}
	at, err := time.Parse(time.RFC3339Nano, t.CreatedAt)
	if err != nil {
		return domain.ProgramCursor{}, fmt.Errorf("%w: unreadable position", ErrInvalidPageToken)
	}
	id, err := domain.ParseID(t.ID)
	if err != nil {
		return domain.ProgramCursor{}, fmt.Errorf("%w: unreadable position", ErrInvalidPageToken)
	}
	return domain.ProgramCursor{CreatedAt: at, ID: id}, nil
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
				Resource:        resourceCoachingProgress,
				AccessedAt:      timestamppb.New(now),
			},
		},
	}
}
