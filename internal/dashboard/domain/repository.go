package domain

import (
	"context"
	"errors"
	"time"
)

// ErrNoDashboard means that user has no projection row at all yet.
//
// It is NOT a mistake: a user who has just registered has not produced a
// single event. Callers answer it with an empty dashboard, not a 404 - the
// page that welcomes a new user must not look broken.
var ErrNoDashboard = errors.New("no dashboard has been projected for this user")

// Repository reads and writes the read-model.
//
// It deliberately has no separate Create and Update. The projection receives
// events in an order that is not guaranteed, and every write has to apply
// whether the row exists or not - two methods mean the caller has to know
// which, and a wrong guess takes the projection down.
type Repository interface {
	// Find returns ErrNoDashboard when there is no row yet.
	Find(ctx context.Context, userID UserID) (*Dashboard, error)

	// ApplyAssessment enters one assessment into the projection.
	//
	// IDEMPOTENT on the slug: the same assessment applied twice yields the
	// same row, not two history rows and not a count raised by two (F7-03).
	// Events can arrive twice - the outbox relay is at-least-once - and the
	// second must not shift anything.
	ApplyAssessment(ctx context.Context, userID UserID, a *Assessment, occurredAt time.Time) error

	// ApplyProgram copies the state of a coaching program.
	//
	// A nil completion means this event does not carry it, and the stored
	// number is LEFT ALONE. Writing zero for "not carried" makes the dashboard
	// jump back to zero percent every time a program is paused.
	ApplyProgram(ctx context.Context, userID UserID, p *Program, occurredAt time.Time) error

	// Forget removes a user's projection, for the account deletion saga.
	Forget(ctx context.Context, userID UserID) error
}

// ProjectionState is the position of a projection.
type ProjectionState struct {
	Name          string
	LastEventAt   time.Time
	EventsApplied int64
	UpdatedAt     time.Time
}

// StateRepository stores projection positions.
//
// It is NOT a replacement for the Kafka offset - that still belongs to the
// consumer group. What lives here answers a different question: "up to which
// event has this projection been built", which the rebuild command uses to
// declare its result complete and the lag measurement uses to know how far
// behind it is.
type StateRepository interface {
	Get(ctx context.Context, name string) (ProjectionState, error)
	Advance(ctx context.Context, name string, eventAt time.Time) error
	Reset(ctx context.Context, name string) error
}
