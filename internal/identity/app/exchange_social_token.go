package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

var (
	// ErrUnsupportedProvider menolak penyedia yang belum dipasang.
	//
	// Daftar putih, bukan daftar hitam: penyedia yang tidak dikenal ditolak,
	// bukan diteruskan. Rute publiknya menerima {provider} sebagai parameter,
	// jadi tanpa daftar ini nilai apa pun dari luar akan masuk ke pencarian.
	ErrUnsupportedProvider = errors.New("unsupported social provider")

	// ErrEmailNotVerifiedByProvider menolak identitas yang alamatnya belum
	// dibuktikan penyedianya.
	ErrEmailNotVerifiedByProvider = errors.New("the provider has not verified this email address")
)

// providerGoogle adalah satu-satunya yang dipasang hari ini.
const providerGoogle = "google"

// SocialIdentity adalah yang sudah didapat edge dari penyedia.
//
// Use case ini TIDAK berbicara dengan Google. Pertukaran kode OAuth,
// pemeriksaan parameter state, dan pembacaan id_token adalah urusan adapter
// di edge; yang sampai ke sini hanyalah hasilnya, sehingga alur akun bisa
// diuji tanpa jaringan.
type SocialIdentity struct {
	Provider   string
	ProviderID string
	Email      string

	// EmailVerified adalah pernyataan penyedia bahwa alamat itu benar-benar
	// milik orang yang baru saja masuk.
	//
	// Ia dibawa terpisah dan bukan diandaikan benar. Siapa pun bisa membuat
	// akun di sebuah penyedia memakai alamat orang lain; yang membedakan
	// "alamat yang diketik" dari "alamat yang terbukti" hanya klaim ini.
	EmailVerified bool
}

// ExchangeSocialToken menukar identitas dari penyedia dengan token akses.
type ExchangeSocialToken struct {
	uow         UnitOfWork
	tokens      domain.TokenIssuer
	profiles    ProfileCreator
	revocations domain.RevocationPublisher
	now         func() time.Time
}

func NewExchangeSocialToken(
	uow UnitOfWork,
	tokens domain.TokenIssuer,
	profiles ProfileCreator,
	revocations domain.RevocationPublisher,
	now func() time.Time,
) (*ExchangeSocialToken, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case tokens == nil:
		return nil, errors.New("nil token issuer")
	case profiles == nil:
		return nil, errors.New("nil profile creator")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &ExchangeSocialToken{
		uow: uow, tokens: tokens, profiles: profiles, revocations: revocations, now: now,
	}, nil
}

func (e *ExchangeSocialToken) Execute(ctx context.Context, identity SocialIdentity) (AuthResult, error) {
	if identity.Provider != providerGoogle {
		return AuthResult{}, fmt.Errorf("%w: %q", ErrUnsupportedProvider, identity.Provider)
	}
	if strings.TrimSpace(identity.ProviderID) == "" {
		return AuthResult{}, errors.New("the provider returned no subject id")
	}

	email, err := domain.NewEmail(identity.Email)
	if err != nil {
		return AuthResult{}, err
	}

	// Alamat yang belum diverifikasi ditolak SEBELUM apa pun dicari.
	//
	// Alur ini memutuskan siapa Anda berdasarkan alamat surel saat sebuah
	// identitas Google belum pernah terlihat. Kalau penyedianya tidak
	// menyatakan alamat itu terbukti milik si penandatangan, seseorang bisa
	// membuat akun Google dengan alamat orang lain lalu masuk sebagai orang
	// itu. Membuat akun baru pun ditolak: kelak pemilik alamat yang sebenarnya
	// akan mendaftar dan menemukan akunnya sudah ditempati.
	if !identity.EmailVerified {
		return AuthResult{}, fmt.Errorf("%w: %s", ErrEmailNotVerifiedByProvider, email)
	}

	var (
		user    *domain.User
		isNewly bool
	)

	if err := e.uow.Do(ctx, func(repos Repositories) error {
		users := repos.Users()

		found, created, err := e.findOrCreate(ctx, users, identity, email)
		if err != nil {
			return err
		}

		// D1, seperti di login biasa: sistem lama memanggil
		// tokens()->delete() setiap kali login sosial berhasil.
		found.RevokeAllTokens(e.now())

		if created {
			if err := users.Create(ctx, found); err != nil {
				return err
			}
		} else if err := users.Update(ctx, found); err != nil {
			return fmt.Errorf("saving the linked account: %w", err)
		}

		user, isNewly = found, created
		return nil
	}); err != nil {
		return AuthResult{}, err
	}

	// Profil hanya diminta untuk akun yang baru dibuat. Menutup B7: sistem
	// lama tidak pernah membuat profil di jalur ini sama sekali, sehingga dua
	// cara mendaftar menghasilkan keadaan yang berbeda tanpa alasan.
	profileID := ""
	if isNewly {
		profileID = createProfileBestEffort(ctx, e.profiles, user.ID())
	}

	token, err := e.tokens.Issue(domain.Claims{
		UserID:        user.ID(),
		UserProfileID: profileID,
		Role:          user.Role(),
		Generation:    user.TokenGeneration(),
	})
	if err != nil {
		return AuthResult{}, fmt.Errorf("issuing token: %w", err)
	}

	publishGenerationBestEffort(ctx, e.revocations, user.ID(), user.TokenGeneration())

	return AuthResult{
		UserID:        user.ID().String(),
		UserProfileID: profileID,
		AccessToken:   token,
	}, nil
}

// findOrCreate menemukan akun yang cocok atau menyiapkan yang baru, dan
// menyatakan mana dari keduanya lewat nilai balik kedua.
func (e *ExchangeSocialToken) findOrCreate(
	ctx context.Context,
	users domain.UserRepository,
	identity SocialIdentity,
	email domain.Email,
) (*domain.User, bool, error) {
	// Pencarian dimulai dari id penyedia, bukan dari alamat.
	//
	// Sub milik Google tidak pernah berubah; alamat surel bisa. Sistem lama
	// memakai updateOrCreate berkunci alamat, jadi seseorang yang mengganti
	// alamat Google-nya akan mendapat akun kedua di sini.
	switch found, err := users.FindByGoogleID(ctx, identity.ProviderID); {
	case err == nil:
		return found, false, nil
	case !errors.Is(err, domain.ErrUserNotFound):
		return nil, false, fmt.Errorf("looking up by provider id: %w", err)
	}

	// Belum pernah terlihat. Alamatnya sudah terbukti di atas, jadi ia boleh
	// dipakai untuk menemukan akun yang sudah ada.
	switch found, err := users.FindByEmail(ctx, email); {
	case err == nil:
		// S5. LinkGoogle satu-satunya jalan menautkan, dan ia tidak bisa
		// menyentuh kata sandi. Sistem lama memakai updateOrCreate dengan
		// password: Hash::make(Str::random(32)) di dalamnya, sehingga setiap
		// login sosial menghancurkan kredensial yang berfungsi.
		//
		// Ia juga menolak menimpa identitas Google lain yang sudah tertaut:
		// satu identitas menunjuk satu akun, dan menimpanya akan memindahkan
		// kepemilikan tanpa siapa pun tahu.
		if err := found.LinkGoogle(identity.ProviderID, e.now()); err != nil {
			return nil, false, err
		}
		return found, false, nil
	case !errors.Is(err, domain.ErrUserNotFound):
		return nil, false, fmt.Errorf("looking up by email: %w", err)
	}

	created, err := domain.RegisterWithGoogle(email, identity.ProviderID, e.now())
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}
