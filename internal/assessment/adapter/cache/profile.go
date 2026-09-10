// Package cache stores profile snapshots that arrive through events (F2-16).
//
// This is a CACHE, not a source of truth: profile-svc remains the owner. What
// is here may be stale, may be missing, and may be rebuilt from the start of
// the topic. What it must NOT be is the only place a fact exists.
package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ErrNotCached means that profile has never entered the cache.
var ErrNotCached = errors.New("no cached snapshot for this user")

// Profiles reads and writes profile snapshots.
type Profiles struct {
	db pg.Querier
}

func NewProfiles(db pg.Querier) *Profiles { return &Profiles{db: db} }

// Snapshot fetches a snapshot from the cache.
func (p *Profiles) Snapshot(ctx context.Context, userID string) (app.ProfileSnapshot, error) {
	const q = `
		SELECT user_profile_id, date_of_birth, sex, country_of_residence, language
		FROM profile_snapshots
		WHERE user_id = $1`

	var (
		profileID string
		dob       *time.Time
		sex       *string
		country   *string
		language  string
	)

	err := p.db.QueryRow(ctx, q, userID).Scan(&profileID, &dob, &sex, &country, &language)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return app.ProfileSnapshot{}, ErrNotCached
	case err != nil:
		return app.ProfileSnapshot{}, fmt.Errorf("reading the cached profile: %w", err)
	}

	// Language does not go into ProfileSnapshot: the risk engine does not use
	// it, and keeping it there would invite unintended use. It stays stored in
	// the table for other consumers later.
	_ = language

	snapshot := app.ProfileSnapshot{UserProfileID: profileID}
	if sex != nil {
		snapshot.Sex = *sex
	}
	if country != nil {
		snapshot.CountryOfResidence = *country
	}
	if dob != nil {
		// Age is computed HERE from the date of birth, not copied from the event.
		// A stored age becomes wrong on the next birthday, and no event will ever
		// arrive to fix it.
		snapshot.Age = ageOn(*dob, time.Now())
	}
	return snapshot, nil
}

// Store saves a snapshot that arrived through an event.
//
// observedAt is the EVENT's time, not the write time. It is what keeps a
// late-arriving event from overwriting a newer one: Kafka guarantees order per
// partition, but consumers can be replayed and partitions can move.
func (p *Profiles) Store(
	ctx context.Context,
	userID, profileID string,
	dateOfBirth, sex, country *string,
	language string,
	observedAt time.Time,
) (bool, error) {
	if userID == "" || profileID == "" {
		return false, errors.New("a cached snapshot needs both ids")
	}
	if language == "" {
		language = "id"
	}

	const q = `
		INSERT INTO profile_snapshots
			(user_id, user_profile_id, date_of_birth, sex, country_of_residence, language, observed_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (user_id) DO UPDATE SET
			user_profile_id      = EXCLUDED.user_profile_id,
			date_of_birth        = EXCLUDED.date_of_birth,
			sex                  = EXCLUDED.sex,
			country_of_residence = EXCLUDED.country_of_residence,
			language             = EXCLUDED.language,
			observed_at          = EXCLUDED.observed_at,
			updated_at           = now()
		WHERE profile_snapshots.observed_at < EXCLUDED.observed_at`

	tag, err := p.db.Exec(ctx, q,
		userID, profileID, dateOrNil(dateOfBirth), strOrNil(sex), strOrNil(country),
		language, observedAt)
	if err != nil {
		return false, fmt.Errorf("storing the cached profile: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// dateOrNil turns an ISO date into a database value.
//
// An unreadable date becomes NULL, not an error: one malformed event must
// not stop the whole queue, and "not filled in" is a valid state.
func dateOrNil(iso *string) any {
	if iso == nil || *iso == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", *iso)
	if err != nil {
		return nil
	}
	return parsed
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// ageOn computes the age on a given date.
//
// A birthday not yet reached subtracts one. Without that, someone born in
// December counts as a year older for the first eleven months - and age is a
// direct input to the risk model.
//
// The comparison is month-and-day, NOT YearDay. The first version used
// YearDay and was wrong in leap years: 29 February shifts every following
// day by one, so someone born on 1 March counted as having had their
// birthday on 29 February - a year older, a day early, for everyone born
// after February, every four years.
func ageOn(birth, on time.Time) int {
	years := on.Year() - birth.Year()

	if on.Month() < birth.Month() ||
		(on.Month() == birth.Month() && on.Day() < birth.Day()) {
		years--
	}

	if years < 0 {
		// A date of birth in the future is impossible - the domain refuses it -
		// but the cache accepts whatever arrives through an event, and a negative
		// age is an impossible input to the risk model.
		return 0
	}
	return years
}
