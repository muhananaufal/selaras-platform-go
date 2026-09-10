package app_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// fakeSagas keeps sagas in memory.
type fakeSagas struct {
	byID    map[string]*domain.DeletionSaga
	closed  map[string]domain.SagaStatus
	failNow error
}

func newFakeSagas() *fakeSagas {
	return &fakeSagas{
		byID:   map[string]*domain.DeletionSaga{},
		closed: map[string]domain.SagaStatus{},
	}
}

func (f *fakeSagas) Create(_ context.Context, s *domain.DeletionSaga) error {
	if f.failNow != nil {
		return f.failNow
	}
	f.byID[s.ID.String()] = s
	return nil
}

func (f *fakeSagas) Find(_ context.Context, id domain.SagaID) (*domain.DeletionSaga, error) {
	s, ok := f.byID[id.String()]
	if !ok {
		return nil, domain.ErrSagaNotFound
	}
	return s, nil
}

func (f *fakeSagas) FindOutstandingForUser(
	_ context.Context, userID domain.UserID,
) (*domain.DeletionSaga, error) {
	for _, s := range f.byID {
		if s.UserID == userID && s.Status == domain.SagaRequested {
			return s, nil
		}
	}
	return nil, domain.ErrSagaNotFound
}

func (f *fakeSagas) Confirm(_ context.Context, id domain.SagaID, c domain.Confirmation) error {
	s, ok := f.byID[id.String()]
	if !ok {
		return domain.ErrSagaNotFound
	}
	for _, existing := range s.Confirmations {
		if existing.Service == c.Service {
			return nil
		}
	}
	s.Confirmations = append(s.Confirmations, c)
	return nil
}

func (f *fakeSagas) Close(
	_ context.Context, id domain.SagaID, status domain.SagaStatus, at time.Time,
) error {
	s, ok := f.byID[id.String()]
	if !ok {
		return domain.ErrSagaNotFound
	}
	s.Status = status
	s.FinishedAt = at
	f.closed[id.String()] = status
	return nil
}

func (f *fakeSagas) Outstanding(_ context.Context, _ int) ([]*domain.DeletionSaga, error) {
	out := make([]*domain.DeletionSaga, 0, len(f.byID))
	for _, s := range f.byID {
		if s.Status == domain.SagaRequested {
			out = append(out, s)
		}
	}
	return out, nil
}

// deletionHarness assembles the deletion use case together with an account
// that can be deleted.
type deletionHarness struct {
	uc      *app.DeleteAccount
	sagas   *fakeSagas
	users   *fakeUsers
	uow     *fakeUnitOfWork
	revokes *fakeRevocations
	userID  domain.UserID
}

const deletionPassword = "RahasiaKuat#2026"

func newDeletionHarness(t *testing.T) *deletionHarness {
	t.Helper()

	users := newFakeUsers()
	sagas := newFakeSagas()
	hasher := fakeHasher{}

	email, err := domain.NewEmail("hapus@contoh.test")
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}
	password, err := domain.NewPassword(deletionPassword)
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	user, err := domain.Register(email, hash, time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("Create: %v", err)
	}

	uow := &fakeUnitOfWork{users: users, sagas: sagas}
	revokes := &fakeRevocations{}

	uc, err := app.NewDeleteAccount(users, sagas, hasher, &fakeProfiles{id: uuid.NewString()},
		revokes, uow, time.Now, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatalf("NewDeleteAccount: %v", err)
	}

	return &deletionHarness{uc: uc, sagas: sagas, users: users, uow: uow, revokes: revokes, userID: user.ID()}
}

// TestTheWrongPasswordDeletesNothing is S2, the most directly damaging
// finding in the whole legacy system.
//
// There, DeleteAccountRequest REQUIRED the password field to be present and
// then never compared it with anything - authorize() returned true, the rule
// was just 'required|string', and DeleteUserAccountAction went straight to
// forceDelete(). Anyone holding a valid token - including one stolen from an
// unlocked device - could permanently delete the account by sending any
// string at all.
func TestTheWrongPasswordDeletesNothing(t *testing.T) {
	h := newDeletionHarness(t)

	_, err := h.uc.Execute(context.Background(), app.DeleteAccountCommand{
		UserID:   h.userID.String(),
		Password: "bukan-kata-sandinya",
	})
	if !errors.Is(err, app.ErrWrongPassword) {
		t.Fatalf("a wrong password was reported as %v", err)
	}

	// No saga, no event, and the account is still there.
	if got := len(h.sagas.byID); got != 0 {
		t.Errorf("%d sagas were started by a wrong password", got)
	}
	if h.uow.events != nil && len(h.uow.events.written) != 0 {
		t.Errorf("%d events were published by a wrong password", len(h.uow.events.written))
	}
	if h.users.count() != 1 {
		t.Error("the account was removed despite the wrong password")
	}
}

// TestAnEmptyPasswordDeletesNothing closes the path that is easiest to miss.
//
// An empty password fails the minimum shape, so it fails BEFORE verification.
// What matters: it fails with the SAME answer, not with a validation error an
// attacker could tell apart from a wrong password.
func TestAnEmptyPasswordDeletesNothing(t *testing.T) {
	h := newDeletionHarness(t)

	_, err := h.uc.Execute(context.Background(), app.DeleteAccountCommand{
		UserID: h.userID.String(),
	})
	if !errors.Is(err, app.ErrWrongPassword) {
		t.Fatalf("an empty password was reported as %v", err)
	}
	if len(h.sagas.byID) != 0 {
		t.Error("an empty password started a saga")
	}
}

// TestTheRightPasswordStartsTheSagaButDeletesNothingYet is the order that
// keeps the account findable.
//
// The account is NOT deleted here. Deleting it first would remove the only
// place that knows a deletion is in progress, and a unit that fails to delete
// its data would have nobody to report to.
func TestTheRightPasswordStartsTheSagaButDeletesNothingYet(t *testing.T) {
	h := newDeletionHarness(t)

	saga, err := h.uc.Execute(context.Background(), app.DeleteAccountCommand{
		UserID:   h.userID.String(),
		Password: deletionPassword,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if saga.Status != domain.SagaRequested {
		t.Errorf("a fresh saga is %q", saga.Status)
	}
	if got := len(saga.Outstanding()); got != len(domain.DeletionParticipants) {
		t.Errorf("the saga waits on %d units, want %d", got, len(domain.DeletionParticipants))
	}

	// The account is still there - the six units have not answered.
	if h.users.count() != 1 {
		t.Error("the account was deleted before any unit confirmed")
	}

	// The event is announced, in the same transaction.
	if h.uow.events == nil || len(h.uow.events.written) != 1 {
		t.Fatalf("the saga was recorded without announcing it")
	}
	if h.uow.calls != 1 {
		t.Errorf("the saga and its event were written in %d transactions, want 1", h.uow.calls)
	}
}

// TestASecondRequestIsRefusedWhileTheFirstRuns keeps one saga per account.
func TestASecondRequestIsRefusedWhileTheFirstRuns(t *testing.T) {
	h := newDeletionHarness(t)

	cmd := app.DeleteAccountCommand{UserID: h.userID.String(), Password: deletionPassword}
	if _, err := h.uc.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := h.uc.Execute(context.Background(), cmd); !errors.Is(err, app.ErrDeletionInProgress) {
		t.Fatalf("the second request was reported as %v", err)
	}
	if got := len(h.sagas.byID); got != 1 {
		t.Errorf("%d sagas exist for one account", got)
	}
}

// TestTheAccountIsDeletedOnlyAfterEveryUnitConfirms is the F8 exit gate.
func TestTheAccountIsDeletedOnlyAfterEveryUnitConfirms(t *testing.T) {
	h := newDeletionHarness(t)

	saga, err := h.uc.Execute(context.Background(), app.DeleteAccountCommand{
		UserID: h.userID.String(), Password: deletionPassword,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	last := domain.DeletionParticipants[len(domain.DeletionParticipants)-1]
	for _, name := range domain.DeletionParticipants[:len(domain.DeletionParticipants)-1] {
		if err := h.uc.ConfirmDeletion(context.Background(), saga.ID.String(), domain.Confirmation{
			Service: name, Succeeded: true, ConfirmedAt: time.Now(),
		}); err != nil {
			t.Fatalf("ConfirmDeletion(%s): %v", name, err)
		}
		if h.users.count() != 1 {
			t.Fatalf("the account was deleted after only some units confirmed (%s)", name)
		}
	}

	if err := h.uc.ConfirmDeletion(context.Background(), saga.ID.String(), domain.Confirmation{
		Service: last, Succeeded: true, ConfirmedAt: time.Now(),
	}); err != nil {
		t.Fatalf("ConfirmDeletion(%s): %v", last, err)
	}

	if h.users.count() != 0 {
		t.Error("every unit confirmed but the account is still there")
	}
	if got := h.sagas.closed[saga.ID.String()]; got != domain.SagaCompleted {
		t.Errorf("the saga closed as %q", got)
	}
}

// TestAFailingUnitKeepsTheAccount is the compensation path (F8-03).
//
// Deletion cannot be undone - data already gone from five units does not come
// back - so the compensation is not restoring state. What it does: HOLD the
// account. Deleting the account while its data still exists in some unit means
// nothing can find that data any more: no live user_id to look it up by, and no
// person who can ask for it.
func TestAFailingUnitKeepsTheAccount(t *testing.T) {
	h := newDeletionHarness(t)

	saga, err := h.uc.Execute(context.Background(), app.DeleteAccountCommand{
		UserID: h.userID.String(), Password: deletionPassword,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for i, name := range domain.DeletionParticipants {
		c := domain.Confirmation{Service: name, Succeeded: true, ConfirmedAt: time.Now()}
		if i == len(domain.DeletionParticipants)-1 {
			c.Succeeded = false
			c.FailureReason = "the connection pool was exhausted"
		}
		if err := h.uc.ConfirmDeletion(context.Background(), saga.ID.String(), c); err != nil {
			t.Fatalf("ConfirmDeletion(%s): %v", name, err)
		}
	}

	if got := h.sagas.closed[saga.ID.String()]; got != domain.SagaFailed {
		t.Errorf("a saga with one failure closed as %q", got)
	}
	if h.users.count() != 1 {
		t.Error("the account was deleted even though a unit failed to delete its data")
	}
}

// TestARepeatedConfirmationDoesNotCloseTheSagaEarly is at-least-once.
func TestARepeatedConfirmationDoesNotCloseTheSagaEarly(t *testing.T) {
	h := newDeletionHarness(t)

	saga, err := h.uc.Execute(context.Background(), app.DeleteAccountCommand{
		UserID: h.userID.String(), Password: deletionPassword,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// One unit answers six times. Without the guard, that looks like six units
	// and the account is deleted while five units were never touched.
	for range len(domain.DeletionParticipants) {
		if err := h.uc.ConfirmDeletion(context.Background(), saga.ID.String(), domain.Confirmation{
			Service: "profile", Succeeded: true, ConfirmedAt: time.Now(),
		}); err != nil {
			t.Fatalf("ConfirmDeletion: %v", err)
		}
	}

	if h.users.count() != 1 {
		t.Fatal("one unit answering six times deleted the account")
	}
	if _, closed := h.sagas.closed[saga.ID.String()]; closed {
		t.Error("the saga closed on repeated answers from one unit")
	}
}

// TestDeletingAnAccountRevokesItsTokens is the regression found by the e2e
// test, not by reading code.
//
// The gateway verifies a token's signature without asking anyone; the only
// thing that stops an already issued token is a bumped generation. Without the
// bump, a deleted account KEPT answering requests until its token expired on
// its own - and the e2e test watched exactly that for a full forty seconds.
//
// The legacy system deleted every token before forceDelete(). Skipping it here
// is a regression, not a simplification.
func TestDeletingAnAccountRevokesItsTokens(t *testing.T) {
	h := newDeletionHarness(t)

	before, err := h.users.FindByID(context.Background(), h.userID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	generationBefore := before.TokenGeneration()

	saga, err := h.uc.Execute(context.Background(), app.DeleteAccountCommand{
		UserID: h.userID.String(), Password: deletionPassword,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, name := range domain.DeletionParticipants {
		if err := h.uc.ConfirmDeletion(context.Background(), saga.ID.String(), domain.Confirmation{
			Service: name, Succeeded: true, ConfirmedAt: time.Now(),
		}); err != nil {
			t.Fatalf("ConfirmDeletion(%s): %v", name, err)
		}
	}

	if h.users.count() != 0 {
		t.Fatal("the account survived a complete saga")
	}

	// The NEW generation is announced, and it is higher than before - that is
	// what makes the gateway refuse tokens already issued.
	if len(h.revokes.published) == 0 {
		t.Fatal("no new token generation was published; every outstanding token stays valid")
	}
	last := h.revokes.published[len(h.revokes.published)-1]
	if last.generation <= generationBefore {
		t.Errorf("the published generation is %d, not higher than %d",
			last.generation, generationBefore)
	}
	if last.userID != h.userID {
		t.Errorf("the revocation names user %s, want %s", last.userID, h.userID)
	}
}

// TestAnIncompleteSagaDoesNotRevokeAnything guards the other side.
//
// An incomplete saga must NOT revoke tokens: the deletion has not happened,
// and signing people out of their session for a deletion that may end in
// failure is harm without benefit.
func TestAnIncompleteSagaDoesNotRevokeAnything(t *testing.T) {
	h := newDeletionHarness(t)

	saga, err := h.uc.Execute(context.Background(), app.DeleteAccountCommand{
		UserID: h.userID.String(), Password: deletionPassword,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if err := h.uc.ConfirmDeletion(context.Background(), saga.ID.String(), domain.Confirmation{
		Service: "profile", Succeeded: true, ConfirmedAt: time.Now(),
	}); err != nil {
		t.Fatalf("ConfirmDeletion: %v", err)
	}

	if len(h.revokes.published) != 0 {
		t.Error("an unfinished saga revoked the account's tokens")
	}
}
