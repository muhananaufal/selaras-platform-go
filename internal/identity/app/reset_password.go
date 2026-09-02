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

// ResetLinkSender mengirim tautan reset ke alamat pemiliknya.
//
// Seluruh keamanan alur ini bertumpu pada satu andaian: tokennya hanya sampai
// ke orang yang menguasai kotak masuk itu. Tanpa pengiriman yang benar-benar
// bekerja, sisanya hanya upacara.
type ResetLinkSender interface {
	SendResetLink(ctx context.Context, to domain.Email, token domain.ResetToken) error
}

// RequestPasswordResetCommand adalah masukan mentah dari pemanggil.
type RequestPasswordResetCommand struct {
	Email string
}

// RequestPasswordReset menerbitkan token reset dan mengirimkannya.
//
// Menutup separuh S1. Di sistem lama, `PATCH /reset-password` berada di blok
// rute publik dan langsung mengganti kata sandi milik alamat mana pun yang
// disebutkan - tanpa token, tanpa verifikasi apa pun. Tabel
// `password_reset_tokens` ada di migrasinya tetapi tidak pernah dibaca.
type RequestPasswordReset struct {
	uow   UnitOfWork
	links ResetLinkSender
	now   func() time.Time
}

func NewRequestPasswordReset(uow UnitOfWork, links ResetLinkSender, now func() time.Time) (*RequestPasswordReset, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case links == nil:
		return nil, errors.New("nil reset link sender")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &RequestPasswordReset{uow: uow, links: links, now: now}, nil
}

// Execute selalu mengembalikan nil kecuali penyimpanannya sendiri bermasalah.
//
// Alamat yang tidak terdaftar, alamat yang cacat, dan surel yang gagal
// terkirim semuanya menghasilkan jawaban yang sama seperti keberhasilan.
// Sistem lama memakai aturan `exists:users,email`, yang berarti endpoint-nya
// menjawab berbeda untuk alamat yang terdaftar - dan dengan itu menjadi alat
// pencacahan akun bagi siapa pun.
//
// BATAS YANG DIAKUI: yang diseragamkan di sini baru jawabannya, belum
// waktunya. Jalur yang terdaftar mengerjakan pembuatan token, satu tulisan,
// dan satu pengiriman surel; jalur yang tidak terdaftar berhenti setelah satu
// pembacaan. Selisih itu masih bisa diukur. Yang menutupnya adalah pengiriman
// yang diantrikan sehingga permintaannya pulang seketika di kedua jalur - dan
// antrian itu belum ada.
func (r *RequestPasswordReset) Execute(ctx context.Context, cmd RequestPasswordResetCommand) error {
	email, err := domain.NewEmail(cmd.Email)
	if err != nil {
		// Alamat yang cacat tidak mungkin terdaftar, jadi jawabannya harus
		// sama dengan alamat yang tidak terdaftar. Linter benar bahwa
		// membuang galat itu mencurigakan - di sini justru itu yang
		// diinginkan, karena galat yang bocor keluar akan membedakan kedua
		// jalur dan mengembalikan lubang pencacahan yang sedang ditutup.
		//nolint:nilerr // menyeragamkan jawaban adalah maksudnya (S1)
		return nil
	}

	var (
		user  *domain.User
		token domain.ResetToken
	)

	if err := r.uow.Do(ctx, func(repos Repositories) error {
		found, err := repos.Users().FindByEmail(ctx, email)
		if err != nil {
			if errors.Is(err, domain.ErrUserNotFound) {
				return nil
			}
			return fmt.Errorf("looking up user: %w", err)
		}

		reset, issued, err := domain.NewPasswordReset(found.ID(), r.now())
		if err != nil {
			return fmt.Errorf("creating reset request: %w", err)
		}
		if err := repos.PasswordResets().Create(ctx, reset); err != nil {
			return fmt.Errorf("storing reset request: %w", err)
		}

		user, token = found, issued
		return nil
	}); err != nil {
		return err
	}

	if user == nil {
		return nil
	}

	// Pengiriman berjalan setelah transaksi ditutup. Menahan transaksi selama
	// panggilan jaringan ke penyedia surel akan membuat penyedia yang lambat
	// menahan koneksi basis data, dan itu menular ke seluruh service.
	//
	// Kegagalannya dicatat, bukan dikembalikan: pengiriman hanya pernah
	// dicoba untuk alamat yang terdaftar, jadi galat yang sampai ke pemanggil
	// akan mengumumkan justru hal yang sedang disembunyikan.
	if err := r.links.SendResetLink(ctx, email, token); err != nil {
		slog.ErrorContext(ctx, "could not send the password reset link",
			"user_id", user.ID().String(), "error", err)
	}
	return nil
}

// ConfirmPasswordResetCommand adalah masukan mentah dari pemanggil.
type ConfirmPasswordResetCommand struct {
	Token                string
	Password             string
	PasswordConfirmation string
}

// ConfirmPasswordReset menukar token yang sah dengan kata sandi baru.
type ConfirmPasswordReset struct {
	uow         UnitOfWork
	hasher      domain.PasswordHasher
	revocations domain.RevocationPublisher
	now         func() time.Time
}

func NewConfirmPasswordReset(
	uow UnitOfWork,
	hasher domain.PasswordHasher,
	revocations domain.RevocationPublisher,
	now func() time.Time,
) (*ConfirmPasswordReset, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &ConfirmPasswordReset{uow: uow, hasher: hasher, revocations: revocations, now: now}, nil
}

func (c *ConfirmPasswordReset) Execute(ctx context.Context, cmd ConfirmPasswordResetCommand) error {
	if subtle.ConstantTimeCompare([]byte(cmd.Password), []byte(cmd.PasswordConfirmation)) != 1 {
		return ErrPasswordMismatch
	}

	// Kata sandi divalidasi SEBELUM token ditebus. Kata sandi yang ditolak
	// tidak boleh menghanguskan tautan yang sah - pengguna yang salah ketik
	// masih berhak memakai tautan yang ia terima.
	password, err := domain.NewPassword(cmd.Password)
	if err != nil {
		return err
	}

	token, err := domain.ParseResetToken(cmd.Token)
	if err != nil {
		return err
	}
	hash := domain.HashResetToken(token)

	var (
		userID     domain.UserID
		generation int64
	)

	// Penebusan token, penggantian kata sandi, pencabutan sesi, dan
	// pembatalan permintaan lain berada di SATU transaksi. Bila salah satunya
	// bisa gagal sendiri, ada keadaan di mana kata sandi sudah berganti
	// sementara tokennya masih bisa dipakai lagi - persis kelemahan yang
	// sedang ditutup.
	if err := c.uow.Do(ctx, func(repos Repositories) error {
		resets := repos.PasswordResets()

		reset, err := resets.FindByTokenHash(ctx, hash)
		if err != nil {
			// Token yang tidak ditemukan disamakan dengan yang tidak sah.
			// "Token ini pernah ada tetapi sudah dipakai" memberi tahu
			// penyerang bahwa tebakannya benar.
			return domain.ErrResetTokenInvalid
		}

		if err := reset.Redeem(c.now()); err != nil {
			return fmt.Errorf("%w: %w", domain.ErrResetTokenInvalid, err)
		}

		users := repos.Users()
		user, err := users.FindByID(ctx, reset.UserID)
		if err != nil {
			return err
		}

		hashed, err := c.hasher.Hash(password)
		if err != nil {
			return fmt.Errorf("hashing password: %w", err)
		}
		if err := user.SetPasswordHash(hashed, c.now()); err != nil {
			return err
		}

		// Kalau akunnya memang sudah direbut, sesi si perebut mati bersama
		// kata sandi lamanya. Reset yang tidak mencabut sesi hanya mengganti
		// kata sandi sambil membiarkan penyerangnya tetap masuk.
		user.RevokeAllTokens(c.now())

		if err := users.Update(ctx, user); err != nil {
			return fmt.Errorf("saving the new password: %w", err)
		}
		if err := resets.MarkUsed(ctx, hash, c.now()); err != nil {
			return fmt.Errorf("marking the token used: %w", err)
		}
		// Permintaan lain yang masih beredar adalah kredensial yang masih
		// berlaku atas akun yang baru saja diamankan, dan yang paling mungkin
		// menerbitkannya adalah orang yang sedang mencoba merebutnya.
		if err := resets.InvalidateAllFor(ctx, user.ID(), c.now()); err != nil {
			return fmt.Errorf("invalidating outstanding requests: %w", err)
		}

		userID, generation = user.ID(), user.TokenGeneration()
		return nil
	}); err != nil {
		return err
	}

	if err := c.revocations.PublishGeneration(ctx, userID, generation); err != nil {
		slog.ErrorContext(ctx, "password reset but could not publish the new generation; "+
			"old tokens stay accepted until the checker reads from the source",
			"user_id", userID.String(), "generation", generation, "error", err)
	}
	return nil
}
