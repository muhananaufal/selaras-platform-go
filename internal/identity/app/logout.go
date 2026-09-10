package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Logout ends a user's session.
//
// It bumps the token generation rather than putting one token on a
// blacklist. That is the shape ADR-012 demands through D1: this system
// knows only one active session per user, so "sign out" means every token
// ever issued stops being valid - and that is one increment, not as many
// deletions as there are tokens in circulation.
type Logout struct {
	uow         UnitOfWork
	revocations domain.RevocationPublisher
	now         func() time.Time
}

func NewLogout(
	uow UnitOfWork,
	revocations domain.RevocationPublisher,
	now func() time.Time,
) (*Logout, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Logout{uow: uow, revocations: revocations, now: now}, nil
}

func (l *Logout) Execute(ctx context.Context, userID domain.UserID) error {
	var generation int64

	if err := l.uow.Do(ctx, func(repos Repositories) error {
		users := repos.Users()
		user, err := users.FindByID(ctx, userID)
		if err != nil {
			return err
		}

		user.RevokeAllTokens(l.now())
		if err := users.Update(ctx, user); err != nil {
			return fmt.Errorf("revoking tokens: %w", err)
		}

		generation = user.TokenGeneration()
		return nil
	}); err != nil {
		return err
	}

	// The publish runs AFTER the store succeeded, and only then. Announcing a
	// generation that turned out not to be stored would sign the user out of
	// their session on the basis of a change that never happened, and the next
	// read from the source would let them back in - a sign-out inconsistent
	// with itself.
	//
	// Conversely, a failed publish MUST NOT undo the logout. The row is
	// already stored, so the revocation is real; all that lags is a cache, and
	// a checker that misses fetches it from the source.
	publishGenerationBestEffort(ctx, l.revocations, userID, generation)

	return nil
}
