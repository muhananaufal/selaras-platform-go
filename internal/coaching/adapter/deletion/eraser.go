// Package deletion erases coaching programs when an account is deleted.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service is the name of this unit inside the saga.
const Service = "coaching"

// Erase deletes every coaching program of one user.
//
// Weeks, tasks, threads, and messages follow through ON DELETE CASCADE.
// Deleting the five one by one in Go would leave remnants when the process
// dies halfway - and those remnants could never be found again, because the
// program that was the only path to them is gone.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	if _, err := q.Exec(ctx, `DELETE FROM coaching_programs WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting the coaching programs: %w", err)
	}
	return nil
}
