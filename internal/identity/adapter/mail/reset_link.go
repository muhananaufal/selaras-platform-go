// Package mail composes and sends identity's emails.
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

// ResetLinkSender sends the password-reset link.
//
// It closes F1-33, and only with it is S1 truly closed: the whole security
// of the reset flow rests on the assumption that the token reaches only the
// person who controls that inbox. Without delivery that works, the rest is
// ceremony.
type ResetLinkSender struct {
	sender platformmail.Sender

	// frontendURL is where the link points. It is configured, not derived from
	// the request: an address that comes from the request can be forged, and a
	// reset link pointing at an attacker's host is the easiest way to harvest
	// the tokens we just issued.
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

// bodyTemplate is deliberately plain and short.
//
// Reset emails are the emails most often imitated by scammers, and what
// makes one trustworthy is not decoration but content: what happened, what
// to do, and what happens if it is ignored.
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
	// The token is appended as a query parameter, not a fragment. Unlike an
	// OAuth token hand-off, this link is opened DIRECTLY by the user from
	// their inbox, and a fragment would never reach the page that needs to
	// read it unless the frontend ran JavaScript first.
	//
	// What keeps the window narrow is the nature of the token itself:
	// single-use, one hour to live, and only its hash is stored.
	link := fmt.Sprintf("%s/reset-password?token=%s",
		s.frontendURL, url.QueryEscape(token.Expose()))

	return s.sender.Send(ctx, platformmail.Message{
		To:      to.String(),
		Subject: resetSubject,
		Body:    fmt.Sprintf(bodyTemplate, link),
	})
}
