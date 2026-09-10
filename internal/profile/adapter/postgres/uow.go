package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/app"
)

// UnitOfWork implements app.UnitOfWork with a real Postgres transaction.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork { return &UnitOfWork{pool: pool} }

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// Do runs fn inside one transaction.
func (u *UnitOfWork) Do(ctx context.Context, fn func(pg.Querier) error) error {
	return pg.InTx(ctx, u.pool, fn)
}
