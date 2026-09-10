package app

import (
	"context"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// RecordAssessmentCommand is one assessment.completed event, already decoded.
type RecordAssessmentCommand struct {
	AssessmentID string
	UserID       string
	Slug         string
	Snapshot     map[string]any
	CompletedAt  time.Time
}

// RecordAssessment records an analysis result announced by assessment-svc
// (F4-06). This is the only way coaching gets to know an analysis:
// StartProgram resolves the slug from this record, not from a synchronous
// call.
//
// recorded is false if that analysis was already recorded; the outbox relay is
// at-least-once, so that is a normal state, not an error.
func (s *Service) RecordAssessment(
	ctx context.Context, cmd RecordAssessmentCommand,
) (recorded bool, err error) {
	ref, err := domain.NewAssessmentRef(
		cmd.AssessmentID, cmd.UserID, cmd.Slug, cmd.Snapshot, cmd.CompletedAt)
	if err != nil {
		return false, err
	}
	err = s.uow.Do(ctx, func(r Repositories) error {
		recorded, err = r.Assessments().Record(ctx, ref)
		return err
	})
	return recorded, err
}
