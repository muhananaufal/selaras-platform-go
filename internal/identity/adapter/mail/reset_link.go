// Package mail menyusun dan mengirim surel milik identity.
package mail

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	platformmail "github.com/muhananaufal/selaras-platform-go/internal/platform/mail"
)

// ResetLinkSender mengirim tautan reset kata sandi.
//
// Ia menutup F1-33, dan dengan itu S1 barulah benar-benar tertutup: seluruh
// keamanan alur reset bertumpu pada andaian bahwa tokennya hanya sampai ke
// orang yang menguasai kotak masuk itu. Tanpa pengiriman yang bekerja,
// sisanya hanya upacara.
type ResetLinkSender struct {
	sender platformmail.Sender

	// frontendURL adalah tempat tautannya menunjuk. Ia dikonfigurasi, bukan
	// diturunkan dari permintaan: alamat yang datang dari permintaan bisa
	// dipalsukan, dan tautan reset yang menunjuk ke host penyerang adalah
	// cara termudah memanen token yang baru saja kita terbitkan.
	frontendURL string
}

func NewResetLinkSender(sender platformmail.Sender, frontendURL string) (*ResetLinkSender, error) {
	switch {
	case sender == nil:
		return nil, errors.New("nil mail sender")
	case strings.TrimSpace(frontendURL) == "":
		return nil, errors.New("empty frontend url")
	}
	return &ResetLinkSender{
		sender:      sender,
		frontendURL: strings.TrimRight(frontendURL, "/"),
	}, nil
}

const resetSubject = "Reset your Selaras password"

// bodyTemplate sengaja polos dan pendek.
//
// Surel reset adalah surel yang paling sering ditiru penipu, dan yang
// membuatnya dipercaya bukan hiasannya melainkan isinya: apa yang terjadi,
// apa yang harus dilakukan, dan apa yang terjadi bila diabaikan.
const bodyTemplate = `Someone asked to reset the password for this address.

Open this link to choose a new password:

%s

The link works once and expires in one hour.

If this was not you, you can ignore this message. Your password has not
changed, and nobody can change it without this link.
`

func (s *ResetLinkSender) SendResetLink(
	ctx context.Context,
	to domain.Email,
	token domain.ResetToken,
) error {
	// Token ditempelkan sebagai query parameter, bukan fragment. Berbeda dari
	// penyerahan token OAuth, tautan ini dibuka LANGSUNG oleh pengguna dari
	// kotak masuknya, dan fragment tidak akan pernah sampai ke halaman yang
	// perlu membacanya kecuali frontend menjalankan JavaScript lebih dulu.
	//
	// Yang menjaga jendelanya tetap sempit adalah sifat tokennya sendiri:
	// sekali pakai, berumur satu jam, dan hanya hash-nya yang tersimpan.
	link := fmt.Sprintf("%s/reset-password?token=%s",
		s.frontendURL, url.QueryEscape(token.Expose()))

	return s.sender.Send(ctx, platformmail.Message{
		To:      to.String(),
		Subject: resetSubject,
		Body:    fmt.Sprintf(bodyTemplate, link),
	})
}
