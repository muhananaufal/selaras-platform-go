package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Galat saga penghapusan.
var (
	ErrSagaNotFound      = errors.New("no such deletion saga")
	ErrSagaAlreadyClosed = errors.New("this deletion saga has already finished")
	ErrUnknownService    = errors.New("that service is not part of the deletion saga")
)

// DeletionParticipants are the units that MUST confirm before an account is
// declared deleted.
//
// This list is a contract, not a record. The saga only completes once all six
// names have answered, so adding a seventh unit to the platform without adding
// it here would declare the account deleted while its data is still intact
// there - and nobody would know, because nobody is waiting for it.
//
// Conversely, adding the name of a unit that does not consume the deletion
// topic makes EVERY saga hang forever. That is a far more visible failure, and
// it is the deliberate choice: a hang can be investigated, data quietly left
// behind cannot.
var DeletionParticipants = []string{
	"profile",
	"assessment",
	"coaching",
	"chat",
	"nutrition",
	"dashboard",
}

// SagaID is the id of one deletion saga.
type SagaID struct{ v uuid.UUID }

func NewSagaID() (SagaID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return SagaID{}, fmt.Errorf("generating a saga id: %w", err)
	}
	return SagaID{v: v}, nil
}

func ParseSagaID(raw string) (SagaID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return SagaID{}, fmt.Errorf("%w: saga %q", ErrInvalidUserID, raw)
	}
	return SagaID{v: v}, nil
}

func (id SagaID) String() string { return id.v.String() }
func (id SagaID) IsZero() bool   { return id.v == uuid.Nil }

// SagaStatus is the state of the saga as a whole.
type SagaStatus string

const (
	SagaRequested SagaStatus = "requested"
	SagaCompleted SagaStatus = "completed"

	// SagaFailed means one or more units reported that they failed to delete.
	//
	// There is NO "compensating" state. Deletion cannot be undone - data
	// already gone from five units does not come back just because the sixth
	// failed - so the compensation is not restoring state but making the
	// failure visible and resolvable by a person.
	SagaFailed SagaStatus = "failed"
)

// Confirmation is one unit's answer.
type Confirmation struct {
	Service       string
	Succeeded     bool
	FailureReason string
	ConfirmedAt   time.Time
}

// DeletionSaga is one deletion request together with its answers.
type DeletionSaga struct {
	ID            SagaID
	UserID        UserID
	UserProfileID string

	Status      SagaStatus
	RequestedAt time.Time
	FinishedAt  time.Time

	Confirmations []Confirmation
}

// NewDeletionSaga memulai saga baru.
func NewDeletionSaga(userID UserID, userProfileID string, now time.Time) (*DeletionSaga, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: a deletion saga needs a user", ErrInvalidUserID)
	}

	id, err := NewSagaID()
	if err != nil {
		return nil, err
	}

	return &DeletionSaga{
		ID:            id,
		UserID:        userID,
		UserProfileID: userProfileID,
		Status:        SagaRequested,
		RequestedAt:   now,
		Confirmations: []Confirmation{},
	}, nil
}

// IsParticipant reports whether that unit name is actually part of the
// saga.
//
// A confirmation from an unknown name is REFUSED, not recorded silently: a
// misspelled name would forever look like a unit that has not answered,
// while the real unit has already deleted its data.
func IsParticipant(service string) bool {
	for _, name := range DeletionParticipants {
		if name == service {
			return true
		}
	}
	return false
}

// Outstanding names the units that have NOT answered yet, sorted.
//
// Sorted so two consecutive reads produce the same list - a runbook whose
// order changes every time it is read makes people think something moved.
func (s *DeletionSaga) Outstanding() []string {
	answered := make(map[string]struct{}, len(s.Confirmations))
	for _, c := range s.Confirmations {
		answered[c.Service] = struct{}{}
	}

	out := make([]string, 0, len(DeletionParticipants))
	for _, name := range DeletionParticipants {
		if _, ok := answered[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Failures names the units that reported FAILURE.
func (s *DeletionSaga) Failures() []Confirmation {
	out := make([]Confirmation, 0, len(s.Confirmations))
	for _, c := range s.Confirmations {
		if !c.Succeeded {
			out = append(out, c)
		}
	}
	return out
}

// Resolve determines the saga's state from the answers received so far.
//
// It changes NOTHING; it only concludes. The decision is separated from the
// write so it can be tested without a database, and so the rule lives in one
// place instead of being spread across consumers.
//
// The order of the checks matters: one failure outweighs five successes. A
// saga declaring itself complete while one unit failed is the most damaging
// lie in this whole flow - it means someone was told their data is gone when
// it is not.
func (s *DeletionSaga) Resolve() SagaStatus {
	if len(s.Failures()) > 0 {
		return SagaFailed
	}
	if len(s.Outstanding()) > 0 {
		return SagaRequested
	}
	return SagaCompleted
}

// Confirm records one unit's answer and returns the new state.
//
// A duplicate confirmation from the same unit is ignored: the outbox relay
// is at-least-once, and the same answer can arrive twice.
func (s *DeletionSaga) Confirm(c Confirmation) (SagaStatus, error) {
	if s.Status != SagaRequested {
		return s.Status, fmt.Errorf("%w: %s", ErrSagaAlreadyClosed, s.Status)
	}
	if !IsParticipant(c.Service) {
		return s.Status, fmt.Errorf("%w: %q", ErrUnknownService, c.Service)
	}
	if !c.Succeeded && c.FailureReason == "" {
		return s.Status, errors.New("a failed confirmation must say why")
	}

	for _, existing := range s.Confirmations {
		if existing.Service == c.Service {
			return s.Resolve(), nil
		}
	}

	s.Confirmations = append(s.Confirmations, c)
	return s.Resolve(), nil
}
