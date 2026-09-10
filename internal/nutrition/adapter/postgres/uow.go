package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/app"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// EventWriterFor creates an event writer ON a single transaction.
//
// A factory, not a ready-made writer: a writer built on the connection pool
// would commit on its own, and its event would survive even when the change
// that triggered it was rolled back.
type EventWriterFor func(pg.Querier) app.EventWriter

// UnitOfWork implements app.UnitOfWork with a real Postgres transaction.
type UnitOfWork struct {
	pool   *pgxpool.Pool
	events EventWriterFor
}

func NewUnitOfWork(pool *pgxpool.Pool, events EventWriterFor) (*UnitOfWork, error) {
	switch {
	case pool == nil:
		return nil, errors.New("nil connection pool")
	case events == nil:
		return nil, errors.New("nil event writer factory")
	}
	return &UnitOfWork{pool: pool, events: events}, nil
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repositories) error) error {
	return pg.InTx(ctx, u.pool, func(q pg.Querier) error {
		return fn(&transactional{q: q, events: u.events})
	})
}

// transactional gives every repository the SAME transaction.
//
// That is its purpose: the preferences, the guide, and its outbox row have to
// succeed or fail together. Repositories each holding their own connection
// would let a guide be stored without its event, and that guide would wait for
// content nobody ever requested.
type transactional struct {
	q      pg.Querier
	events EventWriterFor
}

var _ app.Repositories = (*transactional)(nil)

func (t *transactional) Preferences() domain.PreferencesRepository {
	return NewPreferencesRepository(t.q)
}

func (t *transactional) Guides() domain.GuideRepository {
	return NewGuideRepository(t.q)
}

func (t *transactional) Events() app.EventWriter {
	return t.events(t.q)
}
