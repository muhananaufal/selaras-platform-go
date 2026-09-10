package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func sagaOwner(t *testing.T) domain.UserID {
	t.Helper()

	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	return id
}

func newSaga(t *testing.T) *domain.DeletionSaga {
	t.Helper()

	s, err := domain.NewDeletionSaga(sagaOwner(t), uuid.NewString(), time.Now())
	if err != nil {
		t.Fatalf("NewDeletionSaga: %v", err)
	}
	return s
}

func ok(service string) domain.Confirmation {
	return domain.Confirmation{Service: service, Succeeded: true, ConfirmedAt: time.Now()}
}

// TestASagaIsOnlyCompleteWhenEveryUnitHasAnswered is the rule that keeps the
// promise to the user.
//
// A saga declaring itself complete while one unit has not answered means
// someone was told their data is gone while it is still there - in a unit
// nobody addresses any more.
func TestASagaIsOnlyCompleteWhenEveryUnitHasAnswered(t *testing.T) {
	s := newSaga(t)

	if got := len(s.Outstanding()); got != len(domain.DeletionParticipants) {
		t.Fatalf("a fresh saga is waiting on %d units, want %d",
			got, len(domain.DeletionParticipants))
	}

	// All but the last one.
	last := domain.DeletionParticipants[len(domain.DeletionParticipants)-1]
	for _, name := range domain.DeletionParticipants[:len(domain.DeletionParticipants)-1] {
		status, err := s.Confirm(ok(name))
		if err != nil {
			t.Fatalf("Confirm(%s): %v", name, err)
		}
		if status != domain.SagaRequested {
			t.Fatalf("after %s answered the saga is %q; %v are still outstanding",
				name, status, s.Outstanding())
		}
	}

	if got := s.Outstanding(); len(got) != 1 || got[0] != last {
		t.Fatalf("the outstanding list is %v, want just [%s]", got, last)
	}

	status, err := s.Confirm(ok(last))
	if err != nil {
		t.Fatalf("Confirm(%s): %v", last, err)
	}
	if status != domain.SagaCompleted {
		t.Errorf("with every unit answered the saga is %q", status)
	}
	if got := s.Outstanding(); len(got) != 0 {
		t.Errorf("a completed saga still waits on %v", got)
	}
}

// TestOneFailureBeatsFiveSuccesses guards the order of its conclusion.
func TestOneFailureBeatsFiveSuccesses(t *testing.T) {
	s := newSaga(t)

	for _, name := range domain.DeletionParticipants[:len(domain.DeletionParticipants)-1] {
		if _, err := s.Confirm(ok(name)); err != nil {
			t.Fatalf("Confirm(%s): %v", name, err)
		}
	}

	last := domain.DeletionParticipants[len(domain.DeletionParticipants)-1]
	status, err := s.Confirm(domain.Confirmation{
		Service:       last,
		Succeeded:     false,
		FailureReason: "the connection pool was exhausted",
		ConfirmedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if status != domain.SagaFailed {
		t.Fatalf("with one unit failing the saga is %q, want failed", status)
	}
	if got := s.Failures(); len(got) != 1 || got[0].Service != last {
		t.Errorf("the failures are %v", got)
	}
	// The reason travels along, because that is what a person reads when
	// resolving it.
	if s.Failures()[0].FailureReason == "" {
		t.Error("the failure carries no reason")
	}
}

// TestTheSameConfirmationTwiceChangesNothing is at-least-once.
//
// The outbox relay can deliver the same answer twice. Without this guard, six
// units could look like seven answers, and the saga would declare itself
// complete while one unit was never touched.
func TestTheSameConfirmationTwiceChangesNothing(t *testing.T) {
	s := newSaga(t)

	for range 3 {
		if _, err := s.Confirm(ok("profile")); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
	}

	if got := len(s.Confirmations); got != 1 {
		t.Errorf("three deliveries of one answer became %d confirmations", got)
	}
	if got := len(s.Outstanding()); got != len(domain.DeletionParticipants)-1 {
		t.Errorf("%d units are outstanding, want %d",
			got, len(domain.DeletionParticipants)-1)
	}
}

// TestAConfirmationFromAStrangerIsRefused guards the participant list.
//
// A misspelled name would forever look like a unit that has not answered,
// while the real unit has already deleted its data - the saga hangs, and
// the cause is visible nowhere.
func TestAConfirmationFromAStrangerIsRefused(t *testing.T) {
	s := newSaga(t)

	if _, err := s.Confirm(ok("profiles")); !errors.Is(err, domain.ErrUnknownService) {
		t.Fatalf("a misspelled unit name was reported as %v", err)
	}
	if len(s.Confirmations) != 0 {
		t.Error("the refused confirmation was recorded anyway")
	}
}

// TestAFailureMustSayWhy keeps the runbook usable.
func TestAFailureMustSayWhy(t *testing.T) {
	s := newSaga(t)

	if _, err := s.Confirm(domain.Confirmation{
		Service: "profile", Succeeded: false,
	}); err == nil {
		t.Fatal("a failure with no reason was accepted; nobody can act on it")
	}
}

// TestAClosedSagaRefusesLateAnswers guards its final state.
func TestAClosedSagaRefusesLateAnswers(t *testing.T) {
	s := newSaga(t)
	s.Status = domain.SagaCompleted

	if _, err := s.Confirm(ok("profile")); !errors.Is(err, domain.ErrSagaAlreadyClosed) {
		t.Fatalf("a late answer to a finished saga was reported as %v", err)
	}
}

// TestEveryParticipantIsAServiceThatActuallyConsumesTheTopic guards against
// a list drifting from reality.
//
// The participant list is a CONTRACT: the saga only completes once every
// name has answered. A name that never answers makes every saga hang
// forever; a missing name declares the account deleted while its data is
// still intact.
//
// This test cannot check the real consumers from here - that is the e2e
// test's job - but it catches the most likely mistakes: a duplicate name,
// an empty name, and a list that shrank by accident.
func TestEveryParticipantIsAServiceThatActuallyConsumesTheTopic(t *testing.T) {
	seen := make(map[string]struct{}, len(domain.DeletionParticipants))

	for _, name := range domain.DeletionParticipants {
		if name == "" {
			t.Error("the participant list holds an empty name")
		}
		if _, dup := seen[name]; dup {
			t.Errorf("%q appears twice; the saga would wait for one answer and count it twice", name)
		}
		seen[name] = struct{}{}
	}

	// Six units hold user data: profile, assessment, coaching, chat,
	// nutrition, and dashboard. The number is written here so an accidental
	// shrink shows up.
	if len(domain.DeletionParticipants) != 6 {
		t.Errorf("the saga has %d participants, want 6: %v",
			len(domain.DeletionParticipants), domain.DeletionParticipants)
	}
}
