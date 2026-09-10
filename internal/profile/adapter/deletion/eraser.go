// Package deletion erases profile data when an account is deleted.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service is the name of this unit inside the saga.
//
// It MUST match one of the names in identity/domain.DeletionParticipants
// exactly. A name that does not match gets its confirmation refused, and
// the saga hangs forever waiting for a unit that has actually finished.
const Service = "profile"

// Erase deletes a user's profile.
//
// Keyed by user_id, not user_profile_id: the profile id may be empty when the
// saga starts - a profile never created is a valid state (B7) - and this unit
// is the owner, so it need not translate anything.
//
// IDEMPOTENT: a row that does not exist is not an error. The outbox relay is
// at-least-once, and the same request can arrive twice.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	if _, err := q.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting the profile: %w", err)
	}
	return nil
}
