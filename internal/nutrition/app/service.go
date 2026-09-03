// Package app merangkai aturan nutrition menjadi use case.
package app

import (
	"context"
	"errors"
	"time"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// EventWriter menulis event ke outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// Repositories adalah repository yang berbagi satu transaksi.
type Repositories interface {
	Preferences() domain.PreferencesRepository
	Guides() domain.GuideRepository
	Events() EventWriter
}

// UnitOfWork menjalankan sebuah fungsi di dalam satu transaksi.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// LanguageSource menyebutkan bahasa seorang pengguna.
//
// Ia antarmuka, bukan tipe cache-nya langsung: yang dibutuhkan use case hanya
// jawabannya, dan dari mana jawaban itu datang - cache, panggilan, atau nilai
// tetap - bukan urusannya.
type LanguageSource interface {
	Of(ctx context.Context, userID string) (string, error)
}

// Service adalah seluruh use case nutrition.
type Service struct {
	preferences domain.PreferencesRepository
	guides      domain.GuideRepository
	languages   LanguageSource
	uow         UnitOfWork
	now         func() time.Time
}

func NewService(
	preferences domain.PreferencesRepository,
	guides domain.GuideRepository,
	languages LanguageSource,
	uow UnitOfWork,
	now func() time.Time,
) (*Service, error) {
	switch {
	case preferences == nil:
		return nil, errors.New("nil preferences repository")
	case guides == nil:
		return nil, errors.New("nil guide repository")
	case languages == nil:
		return nil, errors.New("nil language source")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	}

	return &Service{
		preferences: preferences,
		guides:      guides,
		languages:   languages,
		uow:         uow,
		now:         now,
	}, nil
}

// learningHistoryLimit adalah berapa banyak menu yang dipilih ikut ke prompt.
//
// Lima, sama dengan sistem lama. Lebih banyak hanya memperpanjang prompt yang
// dibayar per token tanpa menambah apa yang bisa disimpulkan model tentang
// selera seseorang.
const learningHistoryLimit = 5

// preferencesOrEmpty membaca preferensi, dan menganggap ketiadaannya kosong.
//
// Pengguna yang belum pernah membuka halaman preferensi bukan kesalahan, dan
// hub-nya tetap harus bisa dibuka.
func (s *Service) preferencesOrEmpty(
	ctx context.Context, repo domain.PreferencesRepository, user domain.UserID,
) (*domain.Preferences, error) {
	prefs, err := repo.FindByUser(ctx, user)
	switch {
	case errors.Is(err, domain.ErrPreferencesNotFound):
		return domain.NewPreferences(user, s.now())
	case err != nil:
		return nil, err
	}
	return prefs, nil
}
