// Package app memuat use case profile.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

// Service melayani seluruh alur profil.
//
// Ketiganya cukup kecil dan cukup terkait untuk hidup dalam satu tipe;
// memecahnya menjadi tiga struct berisi satu metode hanya menambah nama tanpa
// menambah kejelasan.
type Service struct {
	profiles domain.ProfileRepository
	now      func() time.Time
}

func NewService(profiles domain.ProfileRepository, now func() time.Time) (*Service, error) {
	switch {
	case profiles == nil:
		return nil, errors.New("nil profile repository")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{profiles: profiles, now: now}, nil
}

// Get mengembalikan profil seorang pengguna.
//
// Profil yang belum ada menghasilkan ErrProfileNotFound, dan pemanggilnya
// yang memutuskan apa artinya - bagi gateway itu `data: null`, bukan galat,
// karena "pengguna tanpa profil" memang keadaan yang sah (B7).
func (s *Service) Get(ctx context.Context, userID domain.UserID) (*domain.Profile, error) {
	return s.profiles.FindByUserID(ctx, userID)
}

// CreateEmpty membuat profil kosong untuk pengguna baru.
//
// Dipanggil identity-svc setelah pendaftaran, dan bersifat best-effort di
// sisi pemanggil (ADR-002 aturan 1). Di sisi ini ia tetap harus benar:
// profil kedua untuk pengguna yang sama ditolak indeks unik.
func (s *Service) CreateEmpty(ctx context.Context, userID domain.UserID) (*domain.Profile, error) {
	profile, err := domain.NewEmptyProfile(userID, s.now())
	if err != nil {
		return nil, err
	}

	if err := s.profiles.Create(ctx, profile); err != nil {
		// Sudah punya profil bukan kegagalan bagi pemanggil: identity-svc
		// bisa mencoba ulang setelah jawaban yang hilang di jaringan, dan
		// percobaan kedua harus menghasilkan hal yang sama seperti yang
		// pertama.
		if errors.Is(err, domain.ErrProfileExists) {
			return s.profiles.FindByUserID(ctx, userID)
		}
		return nil, err
	}
	return profile, nil
}

// Update menerapkan perubahan, dan membuat profilnya bila belum ada.
//
// Perilaku membuat-bila-belum-ada itu dipertahankan dari `updateOrCreate` di
// sistem lama, dan ADR-022 menjelaskan mengapa ia wajib: tanpa itu, pengguna
// yang pembuatan profilnya gagal saat mendaftar tidak akan pernah bisa punya
// profil - padahal kegagalan itu justru yang diizinkan ADR-002 aturan 1.
func (s *Service) Update(
	ctx context.Context,
	userID domain.UserID,
	changes domain.ProfileChanges,
) (*domain.Profile, error) {
	now := s.now()

	profile, err := s.profiles.FindByUserID(ctx, userID)
	switch {
	case err == nil:
		if err := profile.Apply(changes, now); err != nil {
			return nil, err
		}
		if err := s.profiles.Update(ctx, profile); err != nil {
			return nil, fmt.Errorf("saving profile: %w", err)
		}
		return profile, nil

	case errors.Is(err, domain.ErrProfileNotFound):
		created, err := domain.NewEmptyProfile(userID, now)
		if err != nil {
			return nil, err
		}
		// Perubahannya diterapkan SEBELUM disimpan, sehingga profil yang
		// nilainya ditolak tidak pernah sempat ada. Menyimpan dulu lalu
		// memperbarui akan meninggalkan profil kosong setiap kali
		// permintaannya cacat.
		if err := created.Apply(changes, now); err != nil {
			return nil, err
		}
		if err := s.profiles.Create(ctx, created); err != nil {
			// Dua permintaan serempak bisa sama-sama menemukan profilnya
			// belum ada. Yang kalah membaca ulang dan menerapkan
			// perubahannya di atas yang menang, alih-alih gagal.
			if errors.Is(err, domain.ErrProfileExists) {
				return s.Update(ctx, userID, changes)
			}
			return nil, fmt.Errorf("creating profile: %w", err)
		}
		return created, nil

	default:
		return nil, fmt.Errorf("looking up profile: %w", err)
	}
}
