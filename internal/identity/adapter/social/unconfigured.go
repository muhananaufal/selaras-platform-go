// Package social memverifikasi identitas dari penyedia masuk sosial.
package social

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
)

// Unconfigured dipakai saat masuk lewat penyedia sosial memang tidak dipasang
// di sebuah lingkungan.
//
// Ia BUKAN penopang sementara yang menunggu diganti. Menjalankan sistem ini
// tanpa masuk lewat Google adalah mode penyebaran yang sah - pendaftaran
// lewat kata sandi berjalan penuh tanpanya - dan lingkungan yang tidak punya
// kredensial penyedia lebih baik menyala dengan satu jalur masuk daripada
// tidak menyala sama sekali.
//
// Yang DILARANG adalah berpura-pura berhasil. Ia menolak dengan alasan yang
// menyebut persis apa yang kurang.
type Unconfigured struct{}

var _ interface {
	Verify(ctx context.Context, provider, idToken string) (app.SocialIdentity, error)
} = Unconfigured{}

func (Unconfigured) Verify(context.Context, string, string) (app.SocialIdentity, error) {
	return app.SocialIdentity{}, status.Error(codes.Unimplemented,
		"social sign-in is not configured in this environment; set GOOGLE_CLIENT_ID")
}
