package app

import (
	"context"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// RecordAssessmentCommand adalah satu event assessment.completed, sudah dibaca.
type RecordAssessmentCommand struct {
	AssessmentID string
	UserID       string
	Slug         string
	Snapshot     map[string]any
	CompletedAt  time.Time
}

// RecordAssessment mencatat hasil analisis yang disiarkan assessment-svc
// (F4-06). Inilah satu-satunya cara coaching mengenal sebuah analisis:
// StartProgram meresolusi slug dari catatan ini, bukan dari panggilan sinkron.
//
// recorded bernilai false bila analisis itu sudah tercatat; relay outbox
// at-least-once, jadi itu keadaan yang normal, bukan galat.
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
