package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Logout mengakhiri sesi seorang pengguna.
//
// Ia menaikkan generasi token, bukan mencatat satu token ke daftar hitam.
// Bentuk itulah yang dituntut ADR-012 lewat D1: sistem ini hanya mengenal
// satu sesi aktif per pengguna, jadi "keluar" berarti seluruh token yang
// pernah terbit berhenti berlaku - dan itu satu kenaikan, bukan penghapusan
// sebanyak token yang beredar.
type Logout struct {
	uow         UnitOfWork
	revocations domain.RevocationPublisher
	now         func() time.Time
}

func NewLogout(
	uow UnitOfWork,
	revocations domain.RevocationPublisher,
	now func() time.Time,
) (*Logout, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Logout{uow: uow, revocations: revocations, now: now}, nil
}

func (l *Logout) Execute(ctx context.Context, userID domain.UserID) error {
	var generation int64

	if err := l.uow.Do(ctx, func(repos Repositories) error {
		users := repos.Users()
		user, err := users.FindByID(ctx, userID)
		if err != nil {
			return err
		}

		user.RevokeAllTokens(l.now())
		if err := users.Update(ctx, user); err != nil {
			return fmt.Errorf("revoking tokens: %w", err)
		}

		generation = user.TokenGeneration()
		return nil
	}); err != nil {
		return err
	}

	// Publikasi berjalan SETELAH penyimpanan berhasil, dan hanya setelahnya.
	// Mengumumkan generasi yang ternyata gagal disimpan akan mengeluarkan
	// pengguna dari sesinya berdasarkan perubahan yang tidak pernah terjadi,
	// dan pembacaan berikutnya dari sumber aslinya akan memasukkannya
	// kembali - keluar yang tidak konsisten dengan dirinya sendiri.
	//
	// Sebaliknya, publikasi yang gagal DILARANG membatalkan logout. Barisnya
	// sudah tersimpan, jadi pencabutannya nyata; yang tertinggal hanya
	// cache, dan pemeriksa yang meleset mengambilnya dari sumber aslinya.
	publishGenerationBestEffort(ctx, l.revocations, userID, generation)

	return nil
}
