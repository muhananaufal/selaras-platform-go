package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ErrWrongPassword refuses a deletion whose password does not match.
//
// It is distinct from ErrInvalidCredentials because the caller is ALREADY
// authenticated: no account can be enumerated from this answer, and a vague message
// only makes people think the app is broken when they mistyped.
var ErrWrongPassword = errors.New("the password does not match")

// ErrDeletionInProgress refuses a second request.
var ErrDeletionInProgress = errors.New("a deletion is already running for this account")

// SagaRepository stores deletion sagas.
type SagaRepository interface {
	Create(ctx context.Context, s *domain.DeletionSaga) error
	Find(ctx context.Context, id domain.SagaID) (*domain.DeletionSaga, error)

	// FindOutstandingForUser returns ErrSagaNotFound when that user is not
	// being deleted.
	FindOutstandingForUser(ctx context.Context, userID domain.UserID) (*domain.DeletionSaga, error)

	// Confirm records one unit's answer.
	//
	// IDEMPOTENT: the same answer twice leaves one row.
	Confirm(ctx context.Context, id domain.SagaID, c domain.Confirmation) error

	// Close closes the saga with its final state.
	Close(ctx context.Context, id domain.SagaID, status domain.SagaStatus, at time.Time) error

	// Outstanding names the sagas that have not finished, oldest first. Used
	// by the runbook and the verification command.
	Outstanding(ctx context.Context, limit int) ([]*domain.DeletionSaga, error)
}

// DeleteAccount starts and completes the account-deletion saga.
//
// It is separated from the other use cases because its dependencies differ: it
// is the only one that needs the saga store as well as the password comparer.
type DeleteAccount struct {
	users    domain.UserRepository
	sagas    SagaRepository
	hasher   domain.PasswordHasher
	profiles ProfileFinder

	// revocations announces the new token generation when an account is
	// deleted.
	//
	// Without it, tokens already issued REMAIN VALID until they expire on
	// their own - the gateway verifies signatures without asking anyone, and
	// its revocation cache still holds the old generation. The legacy system
	// deleted every token before deleting the account; skipping that here is a
	// regression, not a simplification.
	revocations domain.RevocationPublisher

	uow UnitOfWork
	now func() time.Time
	log *slog.Logger
}

func NewDeleteAccount(
	users domain.UserRepository,
	sagas SagaRepository,
	hasher domain.PasswordHasher,
	profiles ProfileFinder,
	revocations domain.RevocationPublisher,
	uow UnitOfWork,
	now func() time.Time,
	log *slog.Logger,
) (*DeleteAccount, error) {
	switch {
	case users == nil:
		return nil, errors.New("nil user repository")
	case sagas == nil:
		return nil, errors.New("nil saga repository")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case profiles == nil:
		return nil, errors.New("nil profile finder")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &DeleteAccount{
		users: users, sagas: sagas, hasher: hasher, profiles: profiles,
		revocations: revocations, uow: uow, now: now, log: log,
	}, nil
}

// DeleteAccountCommand is the deletion request.
type DeleteAccountCommand struct {
	// UserID comes from the verified token, not from the request body
	// (ADR-023).
	UserID string

	// Password is VERIFIED, not merely required to be present.
	//
	// The legacy system required it in the validation rules and then never
	// compared it (finding S2): anyone holding a valid token could permanently
	// delete the account by sending any string at all.
	Password string
}

// Execute starts the account-deletion saga (F8-01).
//
// It deletes NOTHING itself. What it does: makes sure the person is who they
// say, records the saga, and announces the request. Six units delete their own
// data and then confirm, and the account is deleted only once all six have
// answered.
//
// The order is deliberate. Deleting the account first would remove the only
// place that knows a deletion is in progress, and a unit that fails to delete
// its data would have nobody to report to.
func (d *DeleteAccount) Execute(
	ctx context.Context, cmd DeleteAccountCommand,
) (*domain.DeletionSaga, error) {
	userID, err := domain.ParseUserID(cmd.UserID)
	if err != nil {
		return nil, err
	}

	user, err := d.users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	// The password is checked BEFORE anything else.
	//
	// An account that signed in through a social provider has no password. It
	// cannot prove itself this way, and accepting a deletion without any proof
	// would reopen the very hole being closed - so that path is REFUSED with a
	// message naming the reason, not waved through.
	if user.PasswordHash() == "" {
		return nil, fmt.Errorf(
			"%w: this account signs in through a provider and has no password to confirm with",
			ErrWrongPassword)
	}

	candidate, err := domain.NewPassword(cmd.Password)
	if err != nil {
		// Even a password that fails the minimum shape must NOT skip
		// verification; it fails here with the same answer.
		return nil, ErrWrongPassword
	}

	matches, _, err := d.hasher.Verify(user.PasswordHash(), candidate)
	if err != nil {
		return nil, fmt.Errorf("verifying the password: %w", err)
	}
	if !matches {
		return nil, ErrWrongPassword
	}

	// A second request is refused, not the start of a second saga. Two
	// confirmation sequences for one account would make the second think
	// itself incomplete - its units have already answered the first.
	switch _, err := d.sagas.FindOutstandingForUser(ctx, userID); {
	case err == nil:
		return nil, ErrDeletionInProgress
	case !errors.Is(err, domain.ErrSagaNotFound):
		return nil, err
	}

	// The profile id is copied NOW, while the profile still exists.
	//
	// Several units key their data on it, not on user_id. Once profile-svc has
	// deleted its row, nothing can translate it any more - and a unit that has
	// not deleted yet loses the only way to find the data it has to delete.
	//
	// A failed lookup does NOT stop the deletion: a profile that was never
	// created is a valid state (B7), and refusing to delete an account because
	// its profile is missing would trap people in an account they cannot
	// leave.
	profileID, err := d.profiles.FindProfileID(ctx, userID)
	if err != nil {
		d.log.WarnContext(ctx, "could not resolve the profile id before deletion; the saga continues without it",
			"user_id", userID.String(), "error", err)
		profileID = ""
	}

	now := d.now()
	saga, err := domain.NewDeletionSaga(userID, profileID, now)
	if err != nil {
		return nil, err
	}

	// The saga and its event are written in ONE transaction (E10). If the two
	// could come apart, the system could have a saga that was never announced -
	// hanging forever waiting for six units that were never told - or an
	// announcement without a saga, deleting a user's data without a single
	// record that it was requested.
	if err := d.uow.Do(ctx, func(r Repositories) error {
		if err := r.Sagas().Create(ctx, saga); err != nil {
			return err
		}
		return r.Events().Write(ctx, "user", userID.String(), deletionRequested(saga, now))
	}); err != nil {
		return nil, fmt.Errorf("starting the deletion saga: %w", err)
	}

	d.log.InfoContext(ctx, "an account deletion saga started",
		"saga_id", saga.ID.String(), "awaiting", saga.Outstanding())

	return saga, nil
}

// ConfirmDeletion records one unit's answer and closes the saga once it is
// complete.
func (d *DeleteAccount) ConfirmDeletion(
	ctx context.Context, sagaID string, c domain.Confirmation,
) error {
	id, err := domain.ParseSagaID(sagaID)
	if err != nil {
		return err
	}

	now := d.now()

	// generation is filled inside the transaction and announced afterwards - a
	// publish running inside the transaction would announce a change that can
	// still be rolled back.
	var (
		generation  int64
		deletedUser domain.UserID
	)

	err = d.uow.Do(ctx, func(r Repositories) error {
		saga, err := r.Sagas().Find(ctx, id)
		if err != nil {
			return err
		}

		// An answer arriving after the saga was closed is not a failure: the
		// relay is at-least-once, and the second copy arrived after the first
		// closed it.
		status, err := saga.Confirm(c)
		if errors.Is(err, domain.ErrSagaAlreadyClosed) {
			return nil
		}
		if err != nil {
			return err
		}

		if err := r.Sagas().Confirm(ctx, id, c); err != nil {
			return err
		}
		if status == domain.SagaRequested {
			return nil
		}

		if err := r.Sagas().Close(ctx, id, status, now); err != nil {
			return err
		}

		// The account is deleted ONLY once all six units confirmed success.
		//
		// A failed saga leaves the account intact - and that is deliberate.
		// Deleting the account while its data still exists in some unit means
		// nothing can find that data any more: no live user_id to look it up by,
		// and no person who can ask for it.
		if status != domain.SagaCompleted {
			d.log.ErrorContext(ctx, "a deletion saga finished with failures; the account is kept",
				"saga_id", id.String(), "failures", saga.Failures())
			return nil
		}

		// Tokens are revoked BEFORE the row disappears.
		//
		// The gateway verifies signatures without asking anyone; the only thing
		// that stops an already issued token is a bumped generation. Bumping it
		// after the row is gone is impossible - there is nothing left to bump -
		// and the token would keep being accepted until the gateway's revocation
		// cache expires on its own.
		//
		// The legacy system deleted every token before forceDelete(). Skipping
		// that step is a regression, and the e2e test caught it: a deleted
		// account kept answering requests for forty seconds.
		user, err := r.Users().FindByID(ctx, saga.UserID)
		if err != nil {
			return fmt.Errorf("reading the account before deleting it: %w", err)
		}
		user.RevokeAllTokens(now)
		if err := r.Users().Update(ctx, user); err != nil {
			return fmt.Errorf("revoking tokens before deletion: %w", err)
		}
		generation = user.TokenGeneration()
		deletedUser = saga.UserID

		if err := r.Users().Delete(ctx, saga.UserID); err != nil {
			return fmt.Errorf("deleting the account after every unit confirmed: %w", err)
		}

		d.log.InfoContext(ctx, "an account was deleted after every unit confirmed",
			"saga_id", id.String())
		return nil
	})
	if err != nil {
		return err
	}

	// The publish runs AFTER the commit, and its failure does not undo the
	// deletion: the row is already gone, so the revocation is real. All that
	// lags is a cache, and a gateway that misses asks the source - which now
	// answers "no such account", and it fails closed (ADR-020).
	if generation > 0 {
		publishGenerationBestEffort(ctx, d.revocations, deletedUser, generation)
	}
	return nil
}

// deletionRequested composes the event that announces the request.
func deletionRequested(s *domain.DeletionSaga, now time.Time) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: 1,

		// The idempotency key derives from the saga: one saga, one announcement,
		// forever.
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "user-deletion:" + s.ID.String()},

		Payload: &eventsv1.Envelope_UserDeletionRequested{
			UserDeletionRequested: &eventsv1.UserDeletionRequested{
				SagaId:        s.ID.String(),
				UserId:        s.UserID.String(),
				UserProfileId: s.UserProfileID,
			},
		},
	}
}

// LogOutstandingSagas logs the sagas that are hanging.
//
// Called at start-up. A saga left hanging by a previous process will never
// complete on its own - its units were already contacted, and the ones that did
// not answer will not be asked again - so the only way it becomes visible is if
// someone is told.
func (d *DeleteAccount) LogOutstandingSagas(ctx context.Context, log *slog.Logger) {
	sagas, err := d.sagas.Outstanding(ctx, 50)
	if err != nil {
		log.ErrorContext(ctx, "could not read outstanding deletion sagas", "error", err)
		return
	}
	if len(sagas) == 0 {
		return
	}

	log.WarnContext(ctx, "there are unfinished account deletions; see docs/runbook/account-deletion.md",
		"count", len(sagas))
	for _, saga := range sagas {
		log.WarnContext(ctx, "an account deletion is unfinished",
			"saga_id", saga.ID.String(),
			"requested_at", saga.RequestedAt,
			"awaiting", saga.Outstanding(),
			"failures", len(saga.Failures()))
	}
}
