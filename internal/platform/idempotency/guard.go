// Package idempotency makes work that arrives twice happen only once.
//
// It is the mandatory counterpart of the outbox relay. The relay is
// at-least-once on purpose: marking a row as sent before the broker
// acknowledged it would lose the event forever, so it chooses to resend. The
// duplicates that result have to be stopped on the receiving side, and this is
// where they are stopped.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ErrAlreadyProcessed is returned when the key has already been used.
var ErrAlreadyProcessed = errors.New("this work has already been done")

// Guard makes sure a key is worked only once.
//
// It takes a Querier, not a connection pool, because the claim and the work
// MUST commit together. A claim that commits on its own and whose work then
// fails leaves a key recorded as done for work that never happened - and no
// retry can ever repair it.
type Guard struct {
	db    pg.Querier
	scope string
}

// NewGuard creates a guard for one scope.
//
// scope separates different consumers. Without it, a cache writer that has
// already handled an event would make the notification sender believe it has
// too - and the notification would never be sent.
func NewGuard(db pg.Querier, scope string) (*Guard, error) {
	if db == nil {
		return nil, errors.New("nil querier")
	}
	if scope == "" {
		return nil, errors.New("a guard without a scope would let one consumer silence another")
	}
	return &Guard{db: db, scope: scope}, nil
}

// Claim tries to claim a key.
//
// It returns true if the key is new, and false if it has already been used.
// The decision comes from the database's primary key through ON CONFLICT DO
// NOTHING - one statement, not SELECT then INSERT. The latter has a gap
// between the two, and two processes arriving together would both read "not
// yet" and both do the work.
func (g *Guard) Claim(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, errors.New("an empty idempotency key would collapse every job into one")
	}

	const q = `
		INSERT INTO processed_messages (key, scope, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO NOTHING`

	tag, err := g.db.Exec(ctx, q, g.scopedKey(key), g.scope, time.Now())
	if err != nil {
		return false, fmt.Errorf("claiming the idempotency key: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// SaveResult stores the result of work whose key has already been claimed.
//
// It is called in the same transaction as Claim. Storing it in another
// transaction means there is a moment when the key is recorded as done but the
// result does not exist yet - and a repeated request at that moment is answered
// "already done" with no answer to give.
func (g *Guard) SaveResult(ctx context.Context, key string, result []byte) error {
	if key == "" {
		return errors.New("empty idempotency key")
	}

	const q = `UPDATE processed_messages SET result = $2 WHERE key = $1`
	tag, err := g.db.Exec(ctx, q, g.scopedKey(key), result)
	if err != nil {
		return fmt.Errorf("saving the result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Saving a result for a key that was never claimed means the caller has
		// the order backwards. Staying silent would discard the result without a
		// trace.
		return fmt.Errorf("no claim exists for key %q", key)
	}
	return nil
}

// Result fetches the stored result.
//
// found is false if the key has never been claimed. A key that was claimed but
// whose result has not been stored returns found true with a nil result - two
// different states, and telling them apart matters: the first means "do the
// work", the second means "in progress or done without a stored result".
func (g *Guard) Result(ctx context.Context, key string) (result []byte, found bool, err error) {
	if key == "" {
		return nil, false, errors.New("empty idempotency key")
	}

	const q = `SELECT result FROM processed_messages WHERE key = $1`

	err = g.db.QueryRow(ctx, q, g.scopedKey(key)).Scan(&result)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("reading the stored result: %w", err)
	}
	return result, true, nil
}

// scopedKey folds the scope into the key.
//
// The separator is "\x1f" (unit separator), not a character that could appear
// inside a scope or a key. With an ordinary separator such as ":", scope "a" +
// key "b:c" and scope "a:b" + key "c" would produce the same key - and two
// unrelated jobs would cancel each other out.
func (g *Guard) scopedKey(key string) string {
	return g.scope + "\x1f" + key
}

// Sweep deletes records older than the given age.
//
// This table is not partitioned (see schema.sql), so growth is handled here.
// The age has to be longer than any retry window in the system: sweeping too
// early would let an old message that arrives late be worked a second time.
func Sweep(ctx context.Context, db pg.Querier, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, errors.New("sweeping with no age limit would erase every claim")
	}

	const q = `DELETE FROM processed_messages WHERE created_at < $1`
	tag, err := db.Exec(ctx, q, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("sweeping processed messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Release removes the claim on a key.
//
// It is the ONLY way failed work can be retried. Without it, a claim once
// taken closes its key forever: the next delivery is skipped as a duplicate,
// and work that failed once is never done again by anyone.
//
// It is dangerous in the wrong place, and the danger is exactly the inverse of
// its purpose: releasing the claim of work that SUCCEEDED means that work is
// done twice. It may only be called on a failure path that will genuinely be
// retried, in the same transaction that records the failure.
func (g *Guard) Release(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("empty idempotency key")
	}

	const q = `DELETE FROM processed_messages WHERE key = $1`
	if _, err := g.db.Exec(ctx, q, g.scopedKey(key)); err != nil {
		return fmt.Errorf("releasing the idempotency key: %w", err)
	}
	return nil
}
