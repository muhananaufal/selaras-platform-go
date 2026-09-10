package cache

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
)

// Source reads a profile from the cache, and falls back to the original source
// when it is not there yet.
//
// The order matters and must not be reversed: cache first, the gRPC call only
// as a net. Reversed, every calculation would still call profile-svc, and the
// cache would only add a place for data to go stale without removing a single
// call (ADR-007).
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
		// Without the net, a user whose profile has not changed since the cache
		// was installed could never compute their risk.
		return nil, errors.New("a cache without a source behind it locks out everyone it has not seen")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Source{cache: NewProfiles(pool), fallback: fallback, log: log}, nil
}

var _ app.ProfileSource = (*Source)(nil)

// Snapshot fetches a profile snapshot.
func (s *Source) Snapshot(ctx context.Context, userID string) (app.ProfileSnapshot, error) {
	snapshot, err := s.cache.Snapshot(ctx, userID)
	switch {
	case err == nil:
		return snapshot, nil

	case errors.Is(err, ErrNotCached):
		// Never entered the cache. That is an ordinary state - a user whose
		// profile has not changed since the cache was installed, or a cache just
		// rebuilt.
		return s.fallback.Snapshot(ctx, userID)

	default:
		// A broken cache must NOT stop the calculation. It is a cache; the
		// original is still there. The error is logged so it does not vanish
		// silently.
		s.log.WarnContext(ctx, "the profile cache could not be read; falling back to profile-svc",
			"error", err)
		return s.fallback.Snapshot(ctx, userID)
	}
}
