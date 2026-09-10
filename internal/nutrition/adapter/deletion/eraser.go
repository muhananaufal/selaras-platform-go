// Package deletion erases culinary data when an account is deleted.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service is the name of this unit inside the saga.
const Service = "nutrition"

// Erase deletes the preferences, the menu guides, and the cached language.
//
// All three, not two. The language cache is easy to forget because it is "only
// a cache" - but it is still a row keyed by someone's user_id, and the legacy
// system left exactly this kind of cache intact after account deletion (two
// cache-clearing lines in DeleteUserAccountAction were written and then
// commented out).
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	for _, table := range []string{
		"culinary_preferences",
		"daily_meal_guides",
		"user_languages",
	} {
		if _, err := q.Exec(ctx, "DELETE FROM "+table+" WHERE user_id = $1", userID); err != nil {
			return fmt.Errorf("deleting %s: %w", table, err)
		}
	}
	return nil
}
