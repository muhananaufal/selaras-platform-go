// Package app composes the nutrition rules into use cases.
package app

import (
	"context"
	"errors"
	"time"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// EventWriter writes events to the outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// Repositories are the repositories that share one transaction.
type Repositories interface {
	Preferences() domain.PreferencesRepository
	Guides() domain.GuideRepository
	Events() EventWriter
}

// UnitOfWork runs a function inside one transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// LanguageSource names a user's language.
//
// It is an interface, not the cache type directly: all the use case needs is
// the answer, and where that answer comes from - a cache, a call, or a fixed
// value - is none of its business.
type LanguageSource interface {
	Of(ctx context.Context, userID string) (string, error)
}

// Service is the whole set of nutrition use cases.
type Service struct {
	preferences domain.PreferencesRepository
	guides      domain.GuideRepository
	languages   LanguageSource
	uow         UnitOfWork
	now         func() time.Time
}

func NewService(
	preferences domain.PreferencesRepository,
	guides domain.GuideRepository,
	languages LanguageSource,
	uow UnitOfWork,
	now func() time.Time,
) (*Service, error) {
	switch {
	case preferences == nil:
		return nil, errors.New("nil preferences repository")
	case guides == nil:
		return nil, errors.New("nil guide repository")
	case languages == nil:
		return nil, errors.New("nil language source")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	}

	return &Service{
		preferences: preferences,
		guides:      guides,
		languages:   languages,
		uow:         uow,
		now:         now,
	}, nil
}

// learningHistoryLimit is how many chosen menus go into the prompt.
//
// Five, the same as the legacy system. More only lengthens a prompt paid for
// per token without adding to what the model can infer about someone's taste.
const learningHistoryLimit = 5

// preferencesOrEmpty reads the preferences, treating their absence as empty.
//
// A user who has never opened the preferences page is not a mistake, and
// their hub still has to open.
func (s *Service) preferencesOrEmpty(
	ctx context.Context, repo domain.PreferencesRepository, user domain.UserID,
) (*domain.Preferences, error) {
	prefs, err := repo.FindByUser(ctx, user)
	switch {
	case errors.Is(err, domain.ErrPreferencesNotFound):
		return domain.NewPreferences(user, s.now())
	case err != nil:
		return nil, err
	}
	return prefs, nil
}
