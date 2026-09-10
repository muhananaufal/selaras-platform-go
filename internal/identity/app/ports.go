// Package app holds the identity use cases: the flows that change state,
// written in domain types and ports, without a single transport or database
// detail.
package app

import (
	"context"
	"errors"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ErrPasswordMismatch occurs when the confirmation does not equal the
// password.
var ErrPasswordMismatch = errors.New("password confirmation does not match")

// Repositories is the set of stores a use case sees inside one unit of
// work.
//
// They are gathered into one interface rather than handed over one by one,
// because a flow such as password reset writes to two places at once: the
// user's password, and the mark on the token that was just used. If the two
// sat in different transactions, there would be a gap in which the password
// has changed while the token can still be used again.
type Repositories interface {
	Users() domain.UserRepository
	PasswordResets() domain.PasswordResetRepository

	// Sagas and Events are used by account deletion.
	//
	// Both sit in the SAME unit of work as the saga write: a saga recorded
	// without its event hangs forever waiting for six units that were never
	// told, and an event without its saga deletes someone's data without a
	// single record that it was requested.
	Sagas() SagaRepository
	Events() EventWriter
}

// EventWriter writes events to the outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// UnitOfWork runs several writes as one unit.
//
// It lives here, not in an adapter, so a use case can demand atomicity
// without knowing there is a Postgres transaction behind it. Later, when
// the outbox arrives (F3-03), the event row is written through the same
// unit - and the use case does not have to change.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// ProfileCreator asks profile-svc to create an empty profile for a new user.
//
// ADR-002 rule 1: this call is best-effort. Its failure MUST NOT fail the
// registration - a user without a profile is a state that is already valid
// today (B7), and `user.registered` reconciles it later.
type ProfileCreator interface {
	// CreateEmptyProfile returns the id of the newly created profile.
	CreateEmptyProfile(ctx context.Context, userID domain.UserID) (string, error)
}

// AuthResult is what register and login bring back.
type AuthResult struct {
	UserID        string
	UserProfileID string
	AccessToken   string
}

// ProfileFinder fetches a user's profile id from profile-svc.
//
// ADR-002 rule 2: called once per login, not once per request. A profile
// that does not exist yet yields an empty string without an error - that is
// a valid state (B7), and every consumer of the claim has to handle it.
type ProfileFinder interface {
	FindProfileID(ctx context.Context, userID domain.UserID) (string, error)
}
