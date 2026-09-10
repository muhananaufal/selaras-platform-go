package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/app"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
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

func NewUnitOfWork(pool *pgxpool.Pool, events EventWriterFor) *UnitOfWork {
	return &UnitOfWork{pool: pool, events: events}
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repositories) error) error {
	return pg.InTx(ctx, u.pool, func(q pg.Querier) error {
		return fn(&transactional{q: q, events: u.events})
	})
}

type transactional struct {
	q      pg.Querier
	events EventWriterFor
}

var _ app.Repositories = (*transactional)(nil)

func (t *transactional) Conversations() domain.ConversationRepository {
	return NewRepository(t.q)
}

func (t *transactional) Events() app.EventWriter {
	if t.events == nil {
		// A writer that does nothing is far more dangerous than one that refuses:
		// it lets the service run while silently announcing nothing, and the user
		// waits for a reply forever.
		return refusingWriter{}
	}
	return t.events(t.q)
}

// refusingWriter refuses every event write.
type refusingWriter struct{}

func (refusingWriter) Write(context.Context, string, string, *eventsv1.Envelope) error {
	return errors.New("this service was started without an outbox; nothing can be queued")
}
