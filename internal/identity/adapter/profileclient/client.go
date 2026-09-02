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
)

// callTimeout membatasi setiap panggilan.
//
// Ia ada karena kedua pemakaian di identity-svc bersifat best-effort: tanpa
// batas waktu, profile-svc yang menggantung akan menahan pendaftaran atau
// login selama apa pun, dan "best-effort" berubah menjadi "menunggu
// selamanya". Batasnya pendek dengan sengaja - jawabannya boleh hilang.
const callTimeout = 3 * time.Second

// Client memenuhi app.ProfileCreator dan app.ProfileFinder.
type Client struct {
	profiles profilev1.ProfileClient
}

func New(conn grpc.ClientConnInterface) (*Client, error) {
	if conn == nil {
		return nil, errors.New("nil grpc connection")
	}
	return &Client{profiles: profilev1.NewProfileClient(conn)}, nil
}

// CreateEmptyProfile meminta profil kosong untuk pengguna baru.
func (c *Client) CreateEmptyProfile(ctx context.Context, userID domain.UserID) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
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
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	resp, err := c.profiles.ResolveProfileId(ctx, &profilev1.ResolveProfileIdRequest{
		UserId: userID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("resolving the profile id: %w", err)
	}
	return resp.GetUserProfileId(), nil
}
