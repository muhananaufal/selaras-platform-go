package cache

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
)

// Source membaca profil dari cache, dan jatuh ke sumber aslinya bila belum ada.
//
// Urutannya penting dan tidak boleh dibalik: cache lebih dulu, panggilan gRPC
// hanya sebagai jaring. Membalikkannya berarti setiap kalkulasi tetap memanggil
// profile-svc, dan cache-nya hanya menambah tempat data bisa basi tanpa
// menghilangkan satu pun panggilan (ADR-007).
type Source struct {
	cache    *Profiles
	fallback app.ProfileSource
	log      *slog.Logger
}

func NewSource(pool *pgxpool.Pool, fallback app.ProfileSource, log *slog.Logger) (*Source, error) {
	switch {
	case pool == nil:
		return nil, errors.New("nil pool")
	case fallback == nil:
		// Tanpa jaring, pengguna yang profilnya belum pernah berubah sejak
		// cache dipasang tidak akan pernah bisa menghitung risikonya.
		return nil, errors.New("a cache without a source behind it locks out everyone it has not seen")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Source{cache: NewProfiles(pool), fallback: fallback, log: log}, nil
}

var _ app.ProfileSource = (*Source)(nil)

// Snapshot mengambil cuplikan profil.
func (s *Source) Snapshot(ctx context.Context, userID string) (app.ProfileSnapshot, error) {
	snapshot, err := s.cache.Snapshot(ctx, userID)
	switch {
	case err == nil:
		return snapshot, nil

	case errors.Is(err, ErrNotCached):
		// Belum pernah masuk cache. Itu keadaan yang wajar - pengguna yang
		// profilnya belum berubah sejak cache dipasang, atau cache yang baru
		// dibangun ulang.
		return s.fallback.Snapshot(ctx, userID)

	default:
		// Cache yang rusak TIDAK boleh menghentikan kalkulasi. Ia cache; yang
		// aslinya masih ada. Galatnya dicatat supaya tidak hilang diam-diam.
		s.log.WarnContext(ctx, "the profile cache could not be read; falling back to profile-svc",
			"error", err)
		return s.fallback.Snapshot(ctx, userID)
	}
}
