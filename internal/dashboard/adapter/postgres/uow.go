package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/app"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// UnitOfWork memenuhi app.UnitOfWork dengan transaksi Postgres sungguhan.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) (*UnitOfWork, error) {
	if pool == nil {
		return nil, errors.New("nil connection pool")
	}
	return &UnitOfWork{pool: pool}, nil
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repositories) error) error {
	return pg.InTx(ctx, u.pool, func(q pg.Querier) error {
		return fn(&transactional{q: q})
	})
}

// transactional memberi proyeksi dan posisinya transaksi yang SAMA.
//
// Itulah gunanya: kalau keduanya bisa terpisah, posisi bisa maju melewati event
// yang belum diterapkan - dan pembangunan ulang akan mengira event itu sudah
// masuk, lalu berhenti sebelum selesai tanpa satu pun galat.
type transactional struct {
	q pg.Querier
}

var _ app.Repositories = (*transactional)(nil)

func (t *transactional) Dashboards() domain.Repository { return NewRepository(t.q) }

func (t *transactional) State() domain.StateRepository { return NewStateRepository(t.q) }
