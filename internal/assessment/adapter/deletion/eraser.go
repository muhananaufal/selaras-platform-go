// Package deletion removes risk assessments when an account is deleted.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service is this unit's name inside the saga.
const Service = "assessment"

// Erase deletes a user's assessments and profile snapshot.
//
// This unit has TWO owner keys, and that is no oversight: assessments are keyed
// by user_profile_id because that is the aggregate's owner, while the profile
// cache is keyed by user_id because that is the identity verified on every
// request (F2-16). Both have to be deleted.
//
// userProfileID MAY be empty. That happens when the user's profile could not be
// found when the saga started - a valid state (B7). In that case there are no
// assessments to delete, and the right thing is to delete NOTHING, not to delete
// with an empty key. An empty key on a UUID column would be refused by Postgres,
// and that refusal would fail the whole saga for a user who genuinely has no
// assessments.
func Erase(ctx context.Context, q pg.Querier, userID, userProfileID string) error {
	if userProfileID != "" {
		if _, err := q.Exec(ctx,
			`DELETE FROM risk_assessments WHERE user_profile_id = $1`, userProfileID); err != nil {
			return fmt.Errorf("deleting the risk assessments: %w", err)
		}
	}

	// The profile snapshot is a COPY of personal data - date of birth, sex,
	// country. A copy left behind after the account is deleted is personal data
	// nobody knows still exists.
	if _, err := q.Exec(ctx,
		`DELETE FROM profile_snapshots WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting the cached profile snapshot: %w", err)
	}
	return nil
}
