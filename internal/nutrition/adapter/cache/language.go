// Package cache stores the user's language as it arrives through events.
//
// This is a CACHE, not the source of truth: profile-svc remains the owner. What
// lives here may be stale, may be lost, and may be rebuilt from the start of
// the topic.
package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// DefaultLanguage is used while the user's language is not known.
//
// The same as the legacy default. An unknown language yields a guide in
// this language, not a failure: someone who has never touched their profile
// is still entitled to menu advice.
const DefaultLanguage = "id"

// Languages reads and writes the cached language.
type Languages struct {
	db pg.Querier
}

func NewLanguages(db pg.Querier) *Languages { return &Languages{db: db} }

// Of returns a user's language, or the default.
//
// It NEVER returns an error because the cache is empty. An empty cache is a
// normal state - the consumer has not caught up, or the user has never saved
// their profile - and making it an error would stop guide generation over a
// copy that is allowed to be missing.
//
// A real database error is still returned: that is not an empty cache, that
// is broken storage, and staying silent would mean every user silently gets
// the default language without anyone knowing.
func (l *Languages) Of(ctx context.Context, userID string) (string, error) {
	const q = `SELECT language FROM user_languages WHERE user_id = $1`

	var language string
	switch err := l.db.QueryRow(ctx, q, userID).Scan(&language); {
	case errors.Is(err, pgx.ErrNoRows):
		return DefaultLanguage, nil
	case err != nil:
		return "", fmt.Errorf("reading the cached language: %w", err)
	}

	if strings.TrimSpace(language) == "" {
		return DefaultLanguage, nil
	}
	return language, nil
}

// Remember stores the language from an event.
//
// An event OLDER than what is already stored is ignored. Kafka guarantees
// order per partition, but partitions can change and consumers can be
// replayed - without this guard, a replay would restore someone's old
// language months after they changed it.
func (l *Languages) Remember(ctx context.Context, userID, language string, observedAt time.Time) error {
	if strings.TrimSpace(language) == "" {
		language = DefaultLanguage
	}

	const q = `
		INSERT INTO user_languages (user_id, language, observed_at, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id) DO UPDATE
		SET language = EXCLUDED.language,
		    observed_at = EXCLUDED.observed_at,
		    updated_at = now()
		WHERE user_languages.observed_at < EXCLUDED.observed_at`

	if _, err := l.db.Exec(ctx, q, userID, language, observedAt); err != nil {
		return fmt.Errorf("caching the language: %w", err)
	}
	return nil
}

// Forget removes a user's cache entry.
//
// Used by the account deletion saga: a copy left behind after the account is
// deleted is personal data nobody knows still exists.
func (l *Languages) Forget(ctx context.Context, userID string) error {
	if _, err := l.db.Exec(ctx, `DELETE FROM user_languages WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("forgetting the cached language: %w", err)
	}
	return nil
}
