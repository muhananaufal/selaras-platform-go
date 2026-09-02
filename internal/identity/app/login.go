package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ErrInvalidCredentials adalah satu-satunya jawaban bagi setiap kegagalan
// masuk: email tidak terdaftar, kata sandi keliru, akun terhapus, akun tanpa
// kata sandi. Membedakannya mengubah halaman masuk menjadi alat pencacahan
// akun - penyerang cukup mencoba satu alamat untuk tahu apakah orangnya
// terdaftar.
var ErrInvalidCredentials = errors.New("invalid credentials")

// decoyHash adalah hash sungguhan atas kata sandi yang tidak akan pernah
// dipakai siapa pun.
//
// Ia diverifikasi saat email tidak dikenal atau akunnya tidak punya kata
// sandi, supaya jalur yang gagal membayar biaya waktu yang sama dengan jalur
// yang berhasil. Tanpa ini, jawaban yang seragam tidak ada gunanya: hanya
// dengan mengukur waktu jawab, penyerang tetap bisa membedakan alamat yang
// terdaftar dari yang tidak.
//
// Isinya sengaja bukan hash yang valid untuk kata sandi mana pun.
const decoyHash = domain.PasswordHash(
	"$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

// decoyPassword adalah masukan bagi verifikasi umpan di atas.
const decoyPassword = "not-a-real-password-anyone-uses"

// LoginCommand adalah masukan mentah dari pemanggil.
type LoginCommand struct {
	Email    string
	Password string
}

// Login menukar kredensial dengan token akses.
type Login struct {
	uow      UnitOfWork
	hasher   domain.PasswordHasher
	tokens   domain.TokenIssuer
	profiles ProfileFinder
	now      func() time.Time

	// Kandidat umpan dibangun sekali saat penyusunan, bukan di setiap
	// permintaan yang gagal. Konstantanya memang sah hari ini, dan kalau
	// suatu saat ia diubah menjadi tidak sah, kegagalannya muncul di
	// start-up - bukan sebagai galat yang dibuang diam-diam di jalur yang
	// justru harus tetap seragam.
	decoy domain.Password
}

func NewLogin(
	uow UnitOfWork,
	hasher domain.PasswordHasher,
	tokens domain.TokenIssuer,
	profiles ProfileFinder,
	now func() time.Time,
) (*Login, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case tokens == nil:
		return nil, errors.New("nil token issuer")
	case profiles == nil:
		return nil, errors.New("nil profile finder")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	decoy, err := domain.NewPassword(decoyPassword)
	if err != nil {
		return nil, fmt.Errorf("building the decoy candidate: %w", err)
	}

	return &Login{
		uow:      uow,
		hasher:   hasher,
		tokens:   tokens,
		profiles: profiles,
		now:      now,
		decoy:    decoy,
	}, nil
}

func (l *Login) Execute(ctx context.Context, cmd LoginCommand) (AuthResult, error) {
	var user *domain.User

	// Seluruh pemeriksaan kredensial berjalan di dalam satu satuan kerja,
	// karena login yang berhasil juga MENULIS: ia menaikkan generasi token
	// dan dengan itu mengakhiri sesi sebelumnya (D1). Membaca lalu menulis di
	// luar transaksi akan membuat dua login serempak sama-sama membaca
	// generasi lama dan salah satunya hilang.
	err := l.uow.WithUsers(ctx, func(users domain.UserRepository) error {
		found, err := l.authenticate(ctx, users, cmd)
		if err != nil {
			return err
		}

		// D1: satu sesi per pengguna. Login berhasil mencabut seluruh token
		// sebelumnya, dan token baru di bawah dibuat dengan generasi yang
		// sudah dinaikkan - kalau tidak, ia akan mencabut dirinya sendiri.
		found.RevokeAllTokens(l.now())
		if err := users.Update(ctx, found); err != nil {
			return fmt.Errorf("ending previous sessions: %w", err)
		}

		user = found
		return nil
	})
	if err != nil {
		return AuthResult{}, err
	}

	profileID := l.findProfileBestEffort(ctx, user.ID())

	token, err := l.tokens.Issue(domain.Claims{
		UserID:        user.ID(),
		UserProfileID: profileID,
		Role:          user.Role(),
		Generation:    user.TokenGeneration(),
	})
	if err != nil {
		return AuthResult{}, fmt.Errorf("issuing token: %w", err)
	}

	return AuthResult{
		UserID:        user.ID().String(),
		UserProfileID: profileID,
		AccessToken:   token,
	}, nil
}

// authenticate mengembalikan penggunanya bila kredensialnya benar, dan
// ErrInvalidCredentials untuk setiap kegagalan - tanpa kecuali.
//
// Setiap jalur keluar yang gagal melewati verifikasi hash lebih dulu, dan itu
// bukan pemborosan: itulah yang membuat jawaban yang seragam benar-benar
// seragam. Argon2 sengaja lambat, jadi jalur yang melewatinya jauh lebih
// cepat, dan selisih waktu itu sendiri sudah menjawab "apakah alamat ini
// terdaftar".
func (l *Login) authenticate(
	ctx context.Context,
	users domain.UserRepository,
	cmd LoginCommand,
) (*domain.User, error) {
	// Kata sandi yang cacat pun tetap diberi kandidat umpan, supaya
	// panjangnya sendiri tidak menjadi jalan pintas keluar.
	candidate, err := domain.NewPassword(cmd.Password)
	if err != nil {
		candidate = l.decoy
	}

	email, err := domain.NewEmail(cmd.Email)
	if err != nil {
		l.burnTime(candidate)
		return nil, ErrInvalidCredentials
	}

	user, err := users.FindByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, domain.ErrUserNotFound) {
			// Penyimpanan yang bermasalah bukan kredensial yang salah, dan
			// menyamarkannya akan membuat gangguan basis data terlihat
			// seperti gelombang salah kata sandi.
			return nil, fmt.Errorf("looking up user: %w", err)
		}
		l.burnTime(candidate)
		return nil, ErrInvalidCredentials
	}

	// Akun tanpa kata sandi - hanya Google - tidak bisa masuk lewat jalur
	// ini. Ia tetap membayar verifikasi umpan supaya waktunya tidak
	// membedakannya dari akun yang punya kata sandi.
	if !user.CanAuthenticateWithPassword() {
		l.burnTime(candidate)
		return nil, ErrInvalidCredentials
	}

	ok, _, err := l.hasher.Verify(user.PasswordHash(), candidate)
	if err != nil {
		return nil, fmt.Errorf("verifying password: %w", err)
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// burnTime menjalankan verifikasi terhadap hash umpan dan membuang hasilnya.
// Yang dibeli di sini adalah waktunya, bukan jawabannya.
func (l *Login) burnTime(candidate domain.Password) {
	if _, _, err := l.hasher.Verify(decoyHash, candidate); err != nil {
		// Hash umpan memang tidak cocok dengan apa pun; galat di sini hanya
		// berarti hasher-nya sendiri bermasalah, dan itu akan muncul lagi di
		// permintaan yang sah.
		slog.Debug("decoy verification failed", "error", err)
	}
}

// findProfileBestEffort mengambil id profil sekali per login (ADR-002 aturan
// 2), dan mengembalikan string kosong bila tidak ada atau tidak terjangkau.
//
// Profil yang belum ada memang keadaan yang sah (B7), jadi ketiadaannya bukan
// galat. profile-svc yang sedang mati diperlakukan sama: menggagalkan login
// karena layanan profil terganggu akan mengubah gangguan kecil menjadi
// pemadaman autentikasi.
func (l *Login) findProfileBestEffort(ctx context.Context, userID domain.UserID) string {
	profileID, err := l.profiles.FindProfileID(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "could not resolve the profile id; the claim stays empty",
			"user_id", userID.String(), "error", err)
		return ""
	}
	return profileID
}
