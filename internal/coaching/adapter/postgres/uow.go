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

// UnitOfWork memenuhi app.UnitOfWork dengan transaksi Postgres sungguhan.
type UnitOfWork struct {
	pool   *pgxpool.Pool
	events app.EventWriterFor
}

// NewUnitOfWork merangkai satuan kerja.
//
// events adalah PABRIK, bukan penulis yang sudah jadi: penulis yang dibangun di
// atas kolam koneksi akan commit sendiri, dan eventnya bertahan meski perubahan
// yang memicunya batal.
func NewUnitOfWork(pool *pgxpool.Pool, events app.EventWriterFor) *UnitOfWork {
	return &UnitOfWork{pool: pool, events: events}
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// Do menjalankan fn di dalam satu transaksi.
func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repositories) error) error {
	return pg.InTx(ctx, u.pool, func(q pg.Querier) error {
		return fn(&transactional{q: q, events: u.events})
	})
}

// transactional adalah kumpulan repository yang seluruhnya berbagi satu handle
// transaksi.
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

func (t *transactional) Events() app.EventWriter {
	if t.events == nil {
		// Tanpa penulis event, use case yang menerbitkan sesuatu akan panik.
		// Penulis yang tidak melakukan apa-apa jauh lebih berbahaya: ia membuat
		// service berjalan sambil diam-diam tidak menyiarkan apa pun.
		return refusingWriter{}
	}
	return t.events(t.q)
}

// refusingWriter menolak setiap penulisan event.
//
// Ia dipakai saat service dijalankan tanpa outbox. Menolak jauh lebih baik
// daripada diam: use case yang eventnya hilang akan meninggalkan program yang
// menunggu kurikulum selamanya, dan tidak ada yang tahu mengapa.
type refusingWriter struct{}

func (refusingWriter) Write(
	context.Context, string, string, *eventsv1.Envelope,
) error {
	return errors.New("this service was started without an outbox; nothing can be queued")
}
