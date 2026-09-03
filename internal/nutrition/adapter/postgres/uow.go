package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/app"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
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

// transactional memberi seluruh repository transaksi yang SAMA.
//
// Itulah gunanya: preferensi, panduan, dan baris outbox-nya harus jadi atau
// batal bersama-sama. Repository yang masing-masing memegang koneksinya sendiri
// akan membuat panduan tersimpan tanpa eventnya, dan panduan itu menunggu isi
// yang tidak pernah diminta siapa pun.
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
