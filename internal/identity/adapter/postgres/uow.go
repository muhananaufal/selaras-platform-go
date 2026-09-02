package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// UnitOfWork memenuhi app.UnitOfWork dengan transaksi Postgres sungguhan.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork { return &UnitOfWork{pool: pool} }

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// Do menjalankan fn di dalam satu transaksi.
//
// Repository yang diserahkan ke fn dibangun DI ATAS transaksi itu, bukan di
// atas kolam koneksi. Kalau ia dibangun di atas kolam, setiap tulisan akan
// mengambil koneksinya sendiri dan commit sendiri - satuan kerjanya terlihat
// benar, transaksinya kosong, dan tidak ada satu pun test yang menyadarinya
// sampai ada kegagalan yang seharusnya membatalkan sesuatu.
func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repositories) error) error {
	return pg.InTx(ctx, u.pool, func(q pg.Querier) error {
		return fn(&transactional{q: q})
	})
}

// transactional adalah kumpulan repository yang seluruhnya berbagi satu
// handle transaksi.
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
