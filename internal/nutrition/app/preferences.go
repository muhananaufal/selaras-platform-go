package app

import (
	"context"
	"errors"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// UpdatePreferences applies a PARTIAL update (F6-05).
//
// It reads, patches, then stores - inside ONE transaction. Reading outside the
// transaction and writing inside it would let two concurrent requests both read
// the old state, and the one writing later wipes the first one's change without
// anyone knowing.
//
// A user who has no row yet gets a new row, not an error: saving preferences for
// the first time is the most natural thing someone does on this page.
func (s *Service) UpdatePreferences(
	ctx context.Context, userID string, patch domain.PreferencesPatch,
) (*domain.Preferences, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	now := s.now()

	var updated *domain.Preferences
	err = s.uow.Do(ctx, func(r Repositories) error {
		repo := r.Preferences()

		existing, err := repo.FindByUser(ctx, user)
		switch {
		case err == nil:
			if err := existing.Apply(patch, now); err != nil {
				return err
			}
			if err := repo.Update(ctx, existing); err != nil {
				return err
			}
			updated = existing
			return nil

		case errors.Is(err, domain.ErrPreferencesNotFound):
			fresh, err := domain.NewPreferences(user, now)
			if err != nil {
				return err
			}
			if err := fresh.Apply(patch, now); err != nil {
				return err
			}
			if err := repo.Create(ctx, fresh); err != nil {
				return err
			}
			updated = fresh
			return nil

		default:
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ShowPreferences reads the preferences without changing them.
func (s *Service) ShowPreferences(ctx context.Context, userID string) (*domain.Preferences, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}
	return s.preferencesOrEmpty(ctx, s.preferences, user)
}
