package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Yang di bawah ini mewakili tetangga yang belum ada. Semuanya menolak dengan
// alasan yang menyebut task-nya, dan tidak satu pun berpura-pura berhasil.
//
// Bedanya dengan penopang sementara: alur yang memakainya sudah dirancang
// untuk menghadapi kegagalannya. profile-svc yang tidak ada diperlakukan
// persis seperti profile-svc yang sedang mati, dan itu keadaan yang memang
// sah (ADR-002 aturan 1, B7).

// unavailableProfiles berdiri untuk profile-svc, yang datang di F1-31.
type unavailableProfiles struct{}

var errProfileServiceAbsent = errors.New("profile-svc is not wired yet; see F1-31")

func (unavailableProfiles) CreateEmptyProfile(context.Context, domain.UserID) (string, error) {
	return "", errProfileServiceAbsent
}

func (unavailableProfiles) FindProfileID(context.Context, domain.UserID) (string, error) {
	return "", errProfileServiceAbsent
}

// unavailableLinks berdiri untuk pengiriman surel, yang datang di F1-33.
//
// Ia mencatat pada tingkat ERROR, dan itu disengaja. Permintaan reset yang
// tokennya tidak pernah terkirim adalah alur yang diam-diam tidak bekerja;
// satu-satunya yang membuatnya terlihat adalah log yang keras.
type unavailableLinks struct{}

func (unavailableLinks) SendResetLink(ctx context.Context, to domain.Email, _ domain.ResetToken) error {
	slog.ErrorContext(ctx, "no mail transport is wired; the reset token cannot reach anyone",
		"recipient", to.String(), "task", "F1-33")
	return errors.New("no mail transport is configured; see F1-33")
}

// localGenerationSource membaca generasi token dari basis data identity.
//
// identity-svc adalah pemilik data itu, jadi ia bertanya ke penyimpanannya
// sendiri - bukan memanggil dirinya lewat gRPC. Gateway-lah yang memakai
// klien gRPC sebagai sumbernya.
type localGenerationSource struct {
	users domain.UserRepository
}

func (s localGenerationSource) CurrentGeneration(ctx context.Context, userID domain.UserID) (int64, error) {
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return 0, err
	}
	return user.TokenGeneration(), nil
}

// profileClient adalah yang dibutuhkan identity-svc dari profile-svc:
// membuat profil kosong, dan mencari id profil. Keduanya dipakai
// best-effort, dan antarmuka sesempit ini membuat penopangnya sepele.
type profileClient interface {
	CreateEmptyProfile(ctx context.Context, userID domain.UserID) (string, error)
	FindProfileID(ctx context.Context, userID domain.UserID) (string, error)
}
