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

// EventWriterFor membuat penulis event DI ATAS satu transaksi.
//
// Pabrik, bukan penulis yang sudah jadi: penulis yang dibangun di atas kolam
// koneksi akan commit sendiri, dan eventnya bertahan meski perubahan yang
// memicunya batal.
type EventWriterFor func(pg.Querier) app.EventWriter

// UnitOfWork memenuhi app.UnitOfWork dengan transaksi Postgres sungguhan.
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
		// Penulis yang tidak melakukan apa-apa jauh lebih berbahaya daripada
		// yang menolak: ia membuat service berjalan sambil diam-diam tidak
		// menyiarkan apa pun, dan pengguna menunggu balasan selamanya.
		return refusingWriter{}
	}
	return t.events(t.q)
}

// refusingWriter menolak setiap penulisan event.
type refusingWriter struct{}

func (refusingWriter) Write(context.Context, string, string, *eventsv1.Envelope) error {
	return errors.New("this service was started without an outbox; nothing can be queued")
}
