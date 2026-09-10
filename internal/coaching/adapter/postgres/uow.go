package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// UnitOfWork implements app.UnitOfWork with a real Postgres transaction.
type UnitOfWork struct {
	pool   *pgxpool.Pool
	events app.EventWriterFor
}

// NewUnitOfWork assembles the unit of work.
//
// events is a FACTORY, not a ready-made writer: a writer built on the
// connection pool would commit on its own, and its event would survive even
// when the change that triggered it was rolled back.
func NewUnitOfWork(pool *pgxpool.Pool, events app.EventWriterFor) *UnitOfWork {
	return &UnitOfWork{pool: pool, events: events}
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// Do runs fn inside one transaction.
func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repositories) error) error {
	return pg.InTx(ctx, u.pool, func(q pg.Querier) error {
		return fn(&transactional{q: q, events: u.events})
	})
}

// transactional is the set of repositories that all share one transaction
// handle.
type transactional struct {
	q      pg.Querier
	events app.EventWriterFor
}

var _ app.Repositories = (*transactional)(nil)

func (t *transactional) Programs() domain.ProgramRepository {
	return NewProgramRepository(t.q)
}

func (t *transactional) Curricula() domain.CurriculumRepository {
	return NewCurriculumRepository(t.q)
}

func (t *transactional) Threads() domain.ThreadRepository {
	return NewThreadRepository(t.q)
}

func (t *transactional) Assessments() domain.AssessmentRepository {
	return NewAssessmentRepository(t.q)
}

func (t *transactional) Events() app.EventWriter {
	if t.events == nil {
		// Without an event writer, a use case that publishes something would
		// panic. A writer that does nothing is far more dangerous: it lets the
		// service run while silently announcing nothing.
		return refusingWriter{}
	}
	return t.events(t.q)
}

// refusingWriter refuses every event write.
//
// It is used when the service runs without an outbox. Refusing is far better
// than silence: a use case whose event is lost would leave a program waiting
// for its curriculum forever, and nobody would know why.
type refusingWriter struct{}

func (refusingWriter) Write(
	context.Context, string, string, *eventsv1.Envelope,
) error {
	return errors.New("this service was started without an outbox; nothing can be queued")
}
