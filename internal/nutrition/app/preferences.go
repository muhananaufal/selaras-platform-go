package app

import (
	"context"
	"errors"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// UpdatePreferences menerapkan pembaruan PARSIAL (F6-05).
//
// Ia membaca, menambal, lalu menyimpan - di dalam SATU transaksi. Membaca di
// luar transaksi lalu menulis di dalamnya akan membiarkan dua permintaan
// bersamaan sama-sama membaca keadaan lama, dan yang menulis belakangan
// menghapus perubahan yang pertama tanpa ada yang tahu.
//
// Pengguna yang belum punya barisnya mendapat baris baru, bukan galat: menyimpan
// preferensi untuk pertama kali adalah hal yang paling wajar dilakukan seseorang
// di halaman ini.
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

// ShowPreferences membaca preferensi tanpa mengubahnya.
func (s *Service) ShowPreferences(ctx context.Context, userID string) (*domain.Preferences, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}
	return s.preferencesOrEmpty(ctx, s.preferences, user)
}
