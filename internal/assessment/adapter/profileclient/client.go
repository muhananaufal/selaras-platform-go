// Package profileclient mengambil cuplikan profil dari profile-svc.
package profileclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
)

// callTimeout membatasi setiap panggilan.
//
// Berbeda dari pemakaiannya di identity-svc, panggilan ini TIDAK best-effort:
// tanpa profil tidak ada yang bisa dihitung. Batas waktunya tetap ada supaya
// profile-svc yang menggantung menghasilkan galat yang jelas alih-alih
// permintaan yang tidak pernah selesai.
const callTimeout = 5 * time.Second

// Client memenuhi app.ProfileSource.
//
// Ia berkunci user_id, bukan user_profile_id (ADR-023): id profil diturunkan
// dari profil yang dibaca, bukan diterima dari pemanggil. Itu yang membuat
// penilaian tidak bisa ditulis ke atau dibaca dari profil orang lain oleh apa
// pun yang kebetulan bisa menjangkau service ini.
type Client struct {
	profiles profilev1.ProfileClient
}

func New(conn grpc.ClientConnInterface) (*Client, error) {
	if conn == nil {
		return nil, errors.New("nil grpc connection")
	}
	return &Client{profiles: profilev1.NewProfileClient(conn)}, nil
}

var _ app.ProfileSource = (*Client)(nil)

func (c *Client) Snapshot(ctx context.Context, userID string) (app.ProfileSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	resp, err := c.profiles.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: userID})
	if err != nil {
		return app.ProfileSnapshot{}, fmt.Errorf("reading the profile: %w", err)
	}

	p := resp.GetProfile()
	return app.ProfileSnapshot{
		UserProfileID:      p.GetId(),
		Age:                ageFrom(p.GetDateOfBirth()),
		Sex:                sexFrom(p.GetSex()),
		CountryOfResidence: p.GetCountryOfResidence(),
	}, nil
}

// ageFrom menghitung umur dari tanggal ISO-8601.
//
// Tanggal yang kosong menghasilkan nol, dan nol ditolak validasi di lapisan
// use case dengan menyebut bidang mana yang kurang. Menebak umur di sini akan
// mengubah profil yang belum diisi menjadi perhitungan yang tampak sah.
func ageFrom(iso string) int {
	if iso == "" {
		return 0
	}
	born, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return 0
	}

	now := time.Now()
	age := now.Year() - born.Year()
	if now.YearDay() < born.YearDay() {
		age--
	}
	return age
}

func sexFrom(s profilev1.Sex) string {
	switch s {
	case profilev1.Sex_SEX_MALE:
		return "male"
	case profilev1.Sex_SEX_FEMALE:
		return "female"
	default:
		return ""
	}
}
