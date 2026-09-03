// Package app merangkai aturan coaching menjadi use case.
//
// Ia berdiri di antara handler dan repository, dan itu bukan lapisan yang
// ditambahkan demi kerapian: use case coaching menulis ke beberapa tabel
// sekaligus dan menerbitkan event, dan tanpa tempat yang jelas untuk itu,
// urutan serta keatomikannya akan tersebar di handler - persis yang membuat
// CoachingRepository di sistem lama tumbuh menjadi 474 baris yang memuat
// perhitungan, cache, dan event sekaligus (temuan T6).
package app

import (
	"context"
	"errors"
	"time"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// EventWriter menulis event ke outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// EventWriterFor membuat penulis event DI ATAS satu transaksi.
//
// Pabrik, bukan penulis yang sudah jadi: penulis yang dibangun di atas kolam
// koneksi akan commit sendiri, dan eventnya bertahan meski perubahan yang
// memicunya batal - menyiarkan sesuatu yang tidak pernah terjadi.
type EventWriterFor func(pg.Querier) EventWriter

// Repositories adalah kumpulan repository yang seluruhnya berbagi satu
// transaksi.
type Repositories interface {
	Programs() domain.ProgramRepository
	Curricula() domain.CurriculumRepository
	Threads() domain.ThreadRepository
	Events() EventWriter
}

// UnitOfWork menjalankan sebuah fungsi di dalam satu transaksi.
//
// Repository yang diserahkan ke fn dibangun DI ATAS transaksi itu. Kalau ia
// dibangun di atas kolam, setiap tulisan mengambil koneksinya sendiri dan
// commit sendiri - satuan kerjanya terlihat benar, transaksinya kosong, dan
// tidak ada test yang menyadarinya sampai ada kegagalan yang seharusnya
// membatalkan sesuatu.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// Service adalah seluruh use case coaching.
type Service struct {
	programs  domain.ProgramRepository
	curricula domain.CurriculumRepository
	threads   domain.ThreadRepository
	uow       UnitOfWork
	now       func() time.Time
}

func NewService(
	programs domain.ProgramRepository,
	curricula domain.CurriculumRepository,
	threads domain.ThreadRepository,
	uow UnitOfWork,
	now func() time.Time,
) (*Service, error) {
	switch {
	case programs == nil:
		return nil, errors.New("nil program repository")
	case curricula == nil:
		return nil, errors.New("nil curriculum repository")
	case threads == nil:
		return nil, errors.New("nil thread repository")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{
		programs: programs, curricula: curricula, threads: threads,
		uow: uow, now: now,
	}, nil
}

// ownedProgram memuat program dan memeriksa kepemilikannya.
//
// SATU tempat, bukan lima belas pemeriksaan tersalin seperti di sistem lama
// (temuan S9, dan F8-10 yang memperbaikinya). Ia selalu menjawab
// ErrProgramNotFound untuk milik orang lain - membedakannya dari "tidak ada"
// memberi tahu penanya bahwa slug itu ada.
func (s *Service) ownedProgram(
	ctx context.Context, programs domain.ProgramRepository, slug, userID string,
) (*domain.Program, error) {
	owner, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	program, err := programs.FindBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if !program.BelongsTo(owner) {
		return nil, domain.ErrProgramNotFound
	}
	return program, nil
}

// ownedThread memuat thread beserta programnya, dan memeriksa keduanya.
//
// Kepemilikan diperiksa di tingkat PROGRAM, bukan thread: thread tidak punya
// pemilik sendiri, dan memeriksanya sendiri berarti menyalin aturan yang sudah
// ada di tempat lain.
func (s *Service) ownedThread(
	ctx context.Context, r Repositories, threadSlug, userID string,
) (*domain.Thread, *domain.Program, error) {
	owner, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, nil, err
	}

	thread, err := r.Threads().FindThreadBySlug(ctx, threadSlug)
	if err != nil {
		return nil, nil, err
	}

	program, err := r.Programs().FindByID(ctx, thread.ProgramID)
	if err != nil {
		return nil, nil, err
	}
	if !program.BelongsTo(owner) {
		// Thread milik orang lain menjawab "thread tidak ada", bukan "program
		// tidak ada": yang ditanyakan penanya adalah threadnya.
		return nil, nil, domain.ErrThreadNotFound
	}
	return thread, program, nil
}
