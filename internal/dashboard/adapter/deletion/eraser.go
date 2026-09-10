// Package deletion erases the dashboard projection when an account is
// deleted.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service is the name of this unit inside the saga.
const Service = "dashboard"

// Erase deletes a user's read-model.
//
// The read-model owns not a single fact - all of it is copies - but those
// copies hold risk percentages, health categories, and analysis history. "Only
// a projection" is no reason to leave it: what remains after the account is
// deleted is still personal data, and harder to find precisely because nobody
// regards it as a source.
//
// The history first, then the row: the two are not linked by a foreign key -
// this table deliberately has none so the projection can accept events in any
// order - so the ordering is kept here.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	for _, table := range []string{"dashboard_assessments", "dashboards"} {
		if _, err := q.Exec(ctx, "DELETE FROM "+table+" WHERE user_id = $1", userID); err != nil {
			return fmt.Errorf("deleting %s: %w", table, err)
		}
	}
	return nil
}
