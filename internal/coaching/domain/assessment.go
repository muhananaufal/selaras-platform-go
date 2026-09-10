package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAssessmentNotFound = errors.New("risk assessment not found")
	ErrInvalidAssessment  = errors.New("an assessment reference needs an id, an owner, a slug, and a completion time")
)

// AssessmentRef is a soft reference to an analysis result owned by
// assessment-svc.
//
// It is filled from the assessment.completed event, not from a synchronous
// call: coaching only knows about analyses that have finished and have been
// announced. Its snapshot is copied into the program when the program starts,
// so the program stays explainable even if the analysis later changes or
// disappears.
type AssessmentRef struct {
	ID          string
	UserID      UserID
	Slug        string
	Snapshot    map[string]any
	CompletedAt time.Time
}

// NewAssessmentRef validates a reference read from an event.
func NewAssessmentRef(
	id, userID, slug string, snapshot map[string]any, completedAt time.Time,
) (*AssessmentRef, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrInvalidAssessment
	}
	owner, err := ParseUserID(userID)
	if err != nil {
		return nil, ErrInvalidAssessment
	}
	if slug == "" || completedAt.IsZero() {
		return nil, ErrInvalidAssessment
	}
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	return &AssessmentRef{
		ID: id, UserID: owner, Slug: slug, Snapshot: snapshot, CompletedAt: completedAt,
	}, nil
}

// BelongsTo says this analysis belongs to that user.
//
// Someone else's analysis is treated as one that does not exist (S9): the
// slug is a public id, and telling "not yours" apart from "does not exist"
// tells the asker that the slug exists.
func (a *AssessmentRef) BelongsTo(userID UserID) bool {
	return !a.UserID.IsZero() && a.UserID == userID
}

// AssessmentRepository stores analysis references.
type AssessmentRepository interface {
	// Record stores a new reference.
	//
	// recorded is false if that id is already stored. That is not an error:
	// the outbox relay is at-least-once, and an event arriving twice is a
	// normal state.
	Record(ctx context.Context, ref *AssessmentRef) (recorded bool, err error)

	// FindBySlug looks a reference up by its public slug;
	// ErrAssessmentNotFound if there is none.
	FindBySlug(ctx context.Context, slug string) (*AssessmentRef, error)
}
