// Package profileclient menghubungi profile-svc dari identity-svc.
package profileclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
)

// callTimeout membatasi setiap panggilan.
//
// Ia ada karena kedua pemakaian di identity-svc bersifat best-effort: tanpa
// batas waktu, profile-svc yang menggantung akan menahan pendaftaran atau
// login selama apa pun, dan "best-effort" berubah menjadi "menunggu
// selamanya". Batasnya pendek dengan sengaja - jawabannya boleh hilang.
const callTimeout = 3 * time.Second

// Minter menerbitkan token berumur pendek atas nama seorang pengguna.
//
// Kedua panggilan di sini terjadi SEBELUM pengguna memegang token - saat
// mendaftar dan saat masuk - sementara profile-svc, sejak ADR-026, menolak
// RPC berpengguna tanpa token yang sub-nya cocok. identity-svc adalah
// satu-satunya pemegang kunci privat, jadi ia yang mencetak token sekali
// pakai itu; profile-svc memverifikasinya dengan cara yang sama persis
// seperti token pengguna, tanpa jalur khusus yang bisa disalahgunakan.
type Minter func(userID domain.UserID) (string, error)

// Client memenuhi app.ProfileCreator dan app.ProfileFinder.
type Client struct {
	profiles profilev1.ProfileClient
	mint     Minter
}

func New(conn grpc.ClientConnInterface, mint Minter) (*Client, error) {
	switch {
	case conn == nil:
		return nil, errors.New("nil grpc connection")
	case mint == nil:
		return nil, errors.New("nil token minter; profile-svc would refuse every call")
	}
	return &Client{profiles: profilev1.NewProfileClient(conn), mint: mint}, nil
}

// asUser membatasi waktu panggilan dan menempelkan token atas nama pengguna.
func (c *Client) asUser(ctx context.Context, userID domain.UserID) (context.Context, context.CancelFunc, error) {
	raw, err := c.mint(userID)
	if err != nil {
		return nil, nil, fmt.Errorf("minting a token for the profile-svc call: %w", err)
	}
	ctx, cancel := context.WithTimeout(authn.WithToken(ctx, raw), callTimeout)
	return ctx, cancel, nil
}

// CreateEmptyProfile meminta profil kosong untuk pengguna baru.
func (c *Client) CreateEmptyProfile(ctx context.Context, userID domain.UserID) (string, error) {
	ctx, cancel, err := c.asUser(ctx, userID)
	if err != nil {
		return "", err
	}
	defer cancel()

	resp, err := c.profiles.CreateEmptyProfile(ctx, &profilev1.CreateEmptyProfileRequest{
		UserId: userID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("asking profile-svc for an empty profile: %w", err)
	}
	return resp.GetProfile().GetId(), nil
}

// FindProfileID mengambil id profil seorang pengguna.
//
// Profil yang belum ada mengembalikan string kosong TANPA galat, karena itulah
// yang dijanjikan kontraknya dan itu keadaan yang sah (ADR-002 aturan 2, B7).
// Memperlakukannya sebagai galat akan membuat setiap pengguna yang profilnya
// belum dibuat gagal masuk.
func (c *Client) FindProfileID(ctx context.Context, userID domain.UserID) (string, error) {
	ctx, cancel, err := c.asUser(ctx, userID)
	if err != nil {
		return "", err
	}
	defer cancel()

	resp, err := c.profiles.ResolveProfileId(ctx, &profilev1.ResolveProfileIdRequest{
		UserId: userID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("resolving the profile id: %w", err)
	}
	return resp.GetUserProfileId(), nil
}
