// Package app merangkai read-model dasbor menjadi use case.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ProjectionName adalah nama proyeksi ini di projection_state.
//
// Ia tetap. Mengubahnya berarti posisi yang sudah tercatat menjadi milik
// proyeksi lain, dan perintah rebuild akan mengira tidak ada yang pernah
// dibangun.
const ProjectionName = "dashboard"

// Repositories adalah repository yang berbagi satu transaksi.
type Repositories interface {
	Dashboards() domain.Repository
	State() domain.StateRepository
}

// UnitOfWork menjalankan sebuah fungsi di dalam satu transaksi.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// Service adalah seluruh use case dasbor.
type Service struct {
	dashboards domain.Repository
	state      domain.StateRepository
	uow        UnitOfWork
	now        func() time.Time
}

func NewService(
	dashboards domain.Repository,
	state domain.StateRepository,
	uow UnitOfWork,
	now func() time.Time,
) (*Service, error) {
	switch {
	case dashboards == nil:
		return nil, errors.New("nil dashboard repository")
	case state == nil:
		return nil, errors.New("nil projection state repository")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{dashboards: dashboards, state: state, uow: uow, now: now}, nil
}

// View adalah dasbor beserta hal-hal yang diturunkan darinya.
type View struct {
	Dashboard *domain.Dashboard

	// Lag adalah jeda antara peristiwa terakhir yang diproyeksikan dan
	// sekarang. Ia DIBUKA lewat API, bukan disembunyikan: read-model bersifat
	// eventually consistent, dan jeda yang disembunyikan tampak seperti bug.
	Lag time.Duration
}

// Get membaca dasbor seorang pengguna (F7-04).
//
// SATU query untuk ringkasannya dan satu untuk riwayatnya - bukan empat
// repository yang saling memanggil seperti sistem lama, dan tanpa cache yang
// harus diingat seseorang untuk dihapus.
//
// Pengguna yang belum punya proyeksi mendapat dasbor KOSONG, bukan galat.
// Halaman yang menyambut pengguna baru tidak boleh terlihat rusak, dan gateway
// menerjemahkan kosongnya menjadi pesan sambutan seperti sistem lama.
func (s *Service) Get(ctx context.Context, userID string) (*View, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	dash, err := s.dashboards.Find(ctx, user)
	switch {
	case errors.Is(err, domain.ErrNoDashboard):
		dash = &domain.Dashboard{UserID: user, History: []*domain.Assessment{}}
	case err != nil:
		return nil, err
	}

	view := &View{Dashboard: dash}

	// Lag dibaca dari posisi PROYEKSI, bukan dari baris pengguna ini.
	//
	// Baris pengguna yang jarang berubah akan melaporkan lag berjam-jam
	// meskipun proyeksinya baru saja memproses ratusan event orang lain -
	// angka yang benar tentang barisnya, tetapi jawaban yang salah untuk
	// pertanyaan "seberapa tertinggal dasbor ini".
	state, err := s.state.Get(ctx, ProjectionName)
	if err != nil {
		return nil, err
	}
	if !state.LastEventAt.IsZero() {
		view.Lag = s.now().Sub(state.LastEventAt)
	}
	return view, nil
}

// ProjectAssessment menerapkan satu penilaian ke dalam proyeksi (F7-02).
//
// Proyeksi dan posisinya bergerak dalam SATU transaksi. Kalau keduanya bisa
// terpisah, posisi bisa maju melewati event yang belum diterapkan - dan
// pembangunan ulang akan mengira event itu sudah masuk.
func (s *Service) ProjectAssessment(
	ctx context.Context, userID string, a *domain.Assessment, occurredAt time.Time,
) error {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return err
	}

	return s.uow.Do(ctx, func(r Repositories) error {
		if err := r.Dashboards().ApplyAssessment(ctx, user, a, occurredAt); err != nil {
			return err
		}
		return r.State().Advance(ctx, ProjectionName, occurredAt)
	})
}

// ProjectProgram menyalin keadaan program coaching ke dalam proyeksi.
func (s *Service) ProjectProgram(
	ctx context.Context, userID string, p *domain.Program, occurredAt time.Time,
) error {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return err
	}

	return s.uow.Do(ctx, func(r Repositories) error {
		if err := r.Dashboards().ApplyProgram(ctx, user, p, occurredAt); err != nil {
			return err
		}
		return r.State().Advance(ctx, ProjectionName, occurredAt)
	})
}

// Forget menghapus proyeksi seorang pengguna.
func (s *Service) Forget(ctx context.Context, userID string) error {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return err
	}

	return s.uow.Do(ctx, func(r Repositories) error {
		return r.Dashboards().Forget(ctx, user)
	})
}

// State mengembalikan posisi proyeksi, untuk perintah rebuild dan pengukuran.
func (s *Service) State(ctx context.Context) (domain.ProjectionState, error) {
	return s.state.Get(ctx, ProjectionName)
}

// Querier diekspor ulang supaya pemasangan di cmd tidak perlu mengimpor paket
// platform hanya untuk satu tipe.
type Querier = pg.Querier
