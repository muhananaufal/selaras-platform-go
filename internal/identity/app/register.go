package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// RegisterCommand adalah masukan mentah dari pemanggil. Ia memakai string
// biasa karena inilah batas tempat masukan yang belum dipercaya masuk;
// tipe domain baru terbentuk setelah divalidasi di bawah.
type RegisterCommand struct {
	Email                string
	Password             string
	PasswordConfirmation string
}

// Register mendaftarkan akun berbasis kata sandi.
type Register struct {
	uow      UnitOfWork
	hasher   domain.PasswordHasher
	tokens   domain.TokenIssuer
	profiles ProfileCreator
	now      func() time.Time
}

// NewRegister menolak ketergantungan yang kosong.
//
// Sebuah service yang tersusun setengah akan berjalan sampai permintaan
// pertama yang kebetulan menyentuh bagian yang hilang, lalu panik di
// produksi. Lebih baik gagal saat start-up, saat belum ada yang dirugikan.
func NewRegister(
	uow UnitOfWork,
	hasher domain.PasswordHasher,
	tokens domain.TokenIssuer,
	profiles ProfileCreator,
	now func() time.Time,
) (*Register, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case tokens == nil:
		return nil, errors.New("nil token issuer")
	case profiles == nil:
		return nil, errors.New("nil profile creator")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Register{uow: uow, hasher: hasher, tokens: tokens, profiles: profiles, now: now}, nil
}

func (r *Register) Execute(ctx context.Context, cmd RegisterCommand) (AuthResult, error) {
	// Perbandingan konfirmasi dibuat waktu-tetap. Ia membandingkan dua
	// masukan dari orang yang sama, jadi tidak ada rahasia yang bocor -
	// tetapi membandingkan kata sandi dengan == di mana pun adalah kebiasaan
	// yang cepat menular ke tempat yang benar-benar penting.
	if subtle.ConstantTimeCompare([]byte(cmd.Password), []byte(cmd.PasswordConfirmation)) != 1 {
		return AuthResult{}, ErrPasswordMismatch
	}

	email, err := domain.NewEmail(cmd.Email)
	if err != nil {
		return AuthResult{}, err
	}
	password, err := domain.NewPassword(cmd.Password)
	if err != nil {
		return AuthResult{}, err
	}

	hash, err := r.hasher.Hash(password)
	if err != nil {
		return AuthResult{}, fmt.Errorf("hashing password: %w", err)
	}

	user, err := domain.Register(email, hash, r.now())
	if err != nil {
		return AuthResult{}, err
	}

	// Penyimpanan pengguna berjalan di dalam satu satuan kerja. Hari ini ia
	// hanya membungkus satu tulisan; nanti baris outbox `user.registered`
	// menyusul ke dalam satuan yang sama (F3-03), dan use case ini tidak
	// perlu berubah bentuk untuk menerimanya.
	if err := r.uow.Do(ctx, func(repos Repositories) error {
		users := repos.Users()
		return users.Create(ctx, user)
	}); err != nil {
		// ErrEmailTaken diteruskan apa adanya: pada registrasi, pemanggil
		// memang perlu tahu alamatnya sudah dipakai, kalau tidak ia tidak
		// bisa menjelaskan apa pun kepada penggunanya.
		if errors.Is(err, domain.ErrEmailTaken) || errors.Is(err, domain.ErrGoogleIDTaken) {
			return AuthResult{}, err
		}
		return AuthResult{}, fmt.Errorf("storing user: %w", err)
	}

	profileID := r.createProfileBestEffort(ctx, user.ID())

	token, err := r.tokens.Issue(domain.Claims{
		UserID:        user.ID(),
		UserProfileID: profileID,
		Role:          user.Role(),
		Generation:    user.TokenGeneration(),
	})
	if err != nil {
		// Penggunanya sudah tersimpan, jadi ini bukan kegagalan registrasi -
		// tetapi ia tidak punya token, dan satu-satunya jawaban jujur adalah
		// galat. Mencoba masuk akan berhasil.
		return AuthResult{}, fmt.Errorf("issuing token: %w", err)
	}

	return AuthResult{
		UserID:        user.ID().String(),
		UserProfileID: profileID,
		AccessToken:   token,
	}, nil
}

// createProfileBestEffort meminta profile-svc membuat profil kosong dan
// mengembalikan string kosong bila gagal.
//
// ADR-002 aturan 1: kegagalannya DILARANG menggagalkan registrasi. "Pengguna
// tanpa profil" sudah menjadi keadaan yang sah hari ini (B7) - jalur
// pendaftaran lewat Google di sistem lama tidak pernah membuat profil sama
// sekali, dan sistemnya tetap berjalan.
//
// Kegagalannya dicatat, bukan ditelan diam-diam: tanpa catatan, profile-svc
// bisa mati berhari-hari dan yang terlihat hanya pengguna yang profilnya
// kosong tanpa sebab.
func (r *Register) createProfileBestEffort(ctx context.Context, userID domain.UserID) string {
	profileID, err := r.profiles.CreateEmptyProfile(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "could not create an empty profile; registration continues",
			"user_id", userID.String(), "error", err)
		return ""
	}
	return profileID
}
