// Package mail sends outgoing email.
package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Message is one plain-text email.
//
// Plain text only, and that is not a shortcoming waiting to be fixed: the
// emails this system sends carry one link, and HTML only adds surface
// without adding anything that gets across.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender sends one message.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Config holds what is needed to reach the SMTP server.
type Config struct {
	Host string
	Port int

	// Username and Password may be empty. A local development server demands
	// no authentication, and refusing a configuration without credentials
	// would make this flow impossible to try on one's own machine.
	Username string
	Password string

	// From is the sender address. It is required: any SMTP server refuses a
	// message without one, and the failure surfaces far from here.
	From string

	Timeout time.Duration
}

// SMTP sends through an SMTP server.
//
// It uses net/smtp from the standard library, not a third-party library.
// The reason: what this system sends is plain text with one link, and for
// that the stdlib is enough - including STARTTLS, which it negotiates on
// its own when the server announces it.
//
// The trigger for abandoning it is clear: as soon as attachments, HTML, or
// implicit TLS on port 465 are needed, the stdlib is no longer adequate and
// the right library has to be chosen consciously.
type SMTP struct {
	cfg  Config
	addr string
	auth smtp.Auth
}

func NewSMTP(cfg Config) (*SMTP, error) {
	switch {
	case strings.TrimSpace(cfg.Host) == "":
		return nil, errors.New("empty smtp host")
	case cfg.Port <= 0:
		return nil, fmt.Errorf("smtp port %d is not valid", cfg.Port)
	case strings.TrimSpace(cfg.From) == "":
		return nil, errors.New("empty sender address")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}

	s := &SMTP{
		cfg:  cfg,
		addr: net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)),
	}
	if cfg.Username != "" {
		s.auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	return s, nil
}

var _ Sender = (*SMTP)(nil)

func (s *SMTP) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(msg.To) == "" {
		return errors.New("no recipient")
	}

	payload := s.compose(msg)

	// smtp.SendMail takes no context, so the timeout is enforced here. Without
	// it, a hanging mail server would hold the request for however long - and
	// the request calling it is a user's request.
	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(s.addr, s.auth, s.cfg.From, []string{msg.To}, payload)
	}()

	timeout := time.NewTimer(s.cfg.Timeout)
	defer timeout.Stop()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("sending mail: %w", err)
		}
		return nil
	case <-timeout.C:
		return fmt.Errorf("sending mail: no answer within %s", s.cfg.Timeout)
	case <-ctx.Done():
		return fmt.Errorf("sending mail: %w", ctx.Err())
	}
}

// compose assembles an RFC 5322 message.
//
// Header values are stripped of CR and LF.
//
// Without that, a value containing a newline could inject additional headers
// into a message we send under our own name - a Reply-To that steers replies
// to an attacker, say.
//
// It CANNOT add recipients through this transport: smtp.SendMail sets the
// recipients from the SMTP envelope, and headers in the message body do not
// touch them. Stripping is still mandatory, but that is not the reason.
func (s *SMTP) compose(msg Message) []byte {
	var b strings.Builder

	b.WriteString("From: " + sanitiseHeader(s.cfg.From) + "\r\n")
	b.WriteString("To: " + sanitiseHeader(msg.To) + "\r\n")
	b.WriteString("Subject: " + sanitiseHeader(msg.Subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(msg.Body, "\n", "\r\n"))

	return []byte(b.String())
}

func sanitiseHeader(value string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(value)
}
