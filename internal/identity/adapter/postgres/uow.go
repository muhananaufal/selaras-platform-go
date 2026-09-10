package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// UnitOfWork satisfies app.UnitOfWork with a real Postgres transaction.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork { return &UnitOfWork{pool: pool} }

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// Do runs fn inside one transaction.
//
// The repositories handed to fn are built ON that transaction, not on the
// connection pool. Built on the pool, every write would take its own
// connection and commit on its own - the unit of work would look right, the
// transaction would be empty, and not a single test would notice until a
// failure that should have rolled something back.
func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repositories) error) error {
	return pg.InTx(ctx, u.pool, func(q pg.Querier) error {
		return fn(&transactional{q: q})
	})
}

// transactional is the set of repositories that all share one transaction
// handle.
type transactional struct {
	q pg.Querier
}

var _ app.Repositories = (*transactional)(nil)

func (t *transactional) Users() domain.UserRepository {
	return NewUserRepository(t.q)
}

func (t *transactional) PasswordResets() domain.PasswordResetRepository {
	return NewPasswordResetRepository(t.q)
}

func (t *transactional) Sagas() app.SagaRepository {
	return NewSagaRepository(t.q)
}

// Events writes to the outbox INSIDE the same transaction.
//
// A writer built on the connection pool would commit on its own, and its event
// would survive even when the saga was rolled back - deleting someone's data
// without a single record that it was requested.
func (t *transactional) Events() app.EventWriter {
	return outbox.NewWriter(t.q)
}
