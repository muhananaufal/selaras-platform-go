// Package app memuat use case profile.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

// Service serves the whole profile flow.
//
// The three are small enough and related enough to live in one type;
// splitting them into three one-method structs only adds names without adding
// clarity.
type Service struct {
	profiles domain.ProfileRepository
	now      func() time.Time

	// The three are installed together through WithEvents, or not at all.
	// Partially installed would mean profile changes stored without being
	// announced - and nobody knows until the cache on the other side goes
	// stale.
	uow    UnitOfWork
	repos  ProfileRepositoryFor
	events EventWriterFor
}

func NewService(profiles domain.ProfileRepository, now func() time.Time) (*Service, error) {
	switch {
	case profiles == nil:
		return nil, errors.New("nil profile repository")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{profiles: profiles, now: now}, nil
}

// Get returns a user's profile.
//
// A profile that does not exist yet yields ErrProfileNotFound, and the
// caller decides what that means - for the gateway it is `data: null`, not
// an error, because "a user without a profile" is indeed a valid state
// (B7).
func (s *Service) Get(ctx context.Context, userID domain.UserID) (*domain.Profile, error) {
	return s.profiles.FindByUserID(ctx, userID)
}

// CreateEmpty creates an empty profile for a new user.
//
// Called by identity-svc after registration, and best-effort on the
// caller's side (ADR-002 rule 1). On this side it still has to be correct:
// a second profile for the same user is refused by the unique index.
func (s *Service) CreateEmpty(ctx context.Context, userID domain.UserID) (*domain.Profile, error) {
	profile, err := domain.NewEmptyProfile(userID, s.now())
	if err != nil {
		return nil, err
	}

	if err := s.profiles.Create(ctx, profile); err != nil {
		// Already having a profile is not a failure for the caller: identity-svc
		// may retry after an answer lost on the network, and the second attempt
		// has to produce the same thing as the first.
		if errors.Is(err, domain.ErrProfileExists) {
			return s.profiles.FindByUserID(ctx, userID)
		}
		return nil, err
	}
	return profile, nil
}

// Update applies changes, and creates the profile if it does not exist yet.
//
// That create-if-absent behaviour is kept from `updateOrCreate` in the
// legacy system, and ADR-022 explains why it is mandatory: without it, a
// user whose profile creation failed at registration could never have a
// profile - even though that failure is precisely what ADR-002 rule 1
// permits.
func (s *Service) Update(
	ctx context.Context,
	userID domain.UserID,
	changes domain.ProfileChanges,
) (*domain.Profile, error) {
	now := s.now()

	profile, err := s.profiles.FindByUserID(ctx, userID)
	switch {
	case err == nil:
		if err := profile.Apply(changes, now); err != nil {
			return nil, err
		}
		if err := s.profiles.Update(ctx, profile); err != nil {
			return nil, fmt.Errorf("saving profile: %w", err)
		}
		return profile, nil

	case errors.Is(err, domain.ErrProfileNotFound):
		created, err := domain.NewEmptyProfile(userID, now)
		if err != nil {
			return nil, err
		}
		// The changes are applied BEFORE saving, so a profile with refused values
		// never gets to exist. Saving first and then updating would leave an
		// empty profile behind every time the request is malformed.
		if err := created.Apply(changes, now); err != nil {
			return nil, err
		}
		if err := s.profiles.Create(ctx, created); err != nil {
			// Two concurrent requests can both find the profile absent. The loser
			// rereads and applies its changes on top of the winner, instead of
			// failing.
			if errors.Is(err, domain.ErrProfileExists) {
				return s.Update(ctx, userID, changes)
			}
			return nil, fmt.Errorf("creating profile: %w", err)
		}
		return created, nil

	default:
		return nil, fmt.Errorf("looking up profile: %w", err)
	}
}
