// Package deletion erases conversations when an account is deleted.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service is the name of this unit inside the saga.
const Service = "chat"

// Erase deletes every conversation of one user.
//
// The messages follow through ON DELETE CASCADE in the database, not deleted
// one by one in Go: the latter leaves remnants when the process dies halfway,
// and nobody will ever find those remnants - no conversation points at them
// any more.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	if _, err := q.Exec(ctx, `DELETE FROM conversations WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting the conversations: %w", err)
	}
	return nil
}
