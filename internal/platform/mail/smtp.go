// Package mail mengirim surel keluar.
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

// Message adalah satu surel teks polos.
//
// Hanya teks polos, dan itu bukan kekurangan yang menunggu diperbaiki: surel
// yang dikirim sistem ini membawa satu tautan, dan HTML hanya menambah
// permukaan tanpa menambah yang tersampaikan.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender mengirim satu pesan.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Config menampung yang dibutuhkan untuk menghubungi server SMTP.
type Config struct {
	Host string
	Port int

	// Username dan Password boleh kosong. Server pengembangan lokal tidak
	// menuntut autentikasi, dan menolak konfigurasi tanpa kredensial akan
	// membuat alur ini tidak bisa dicoba sama sekali di mesin sendiri.
	Username string
	Password string

	// From adalah alamat pengirim. Ia wajib: server SMTP mana pun menolak
	// pesan tanpanya, dan gagalnya jauh dari sini.
	From string

	Timeout time.Duration
}

// SMTP mengirim lewat server SMTP.
//
// Memakai net/smtp dari pustaka standar, bukan pustaka pihak ketiga.
// Alasannya: yang dikirim sistem ini adalah teks polos satu tautan, dan
// untuk itu stdlib sudah cukup - termasuk STARTTLS, yang dinegosiasikannya
// sendiri bila server mengumumkannya.
//
// Pembatalnya jelas: begitu ada lampiran, HTML, atau kebutuhan TLS implisit
// di port 465, stdlib tidak lagi memadai dan pustaka yang tepat harus
// dipilih sadar.
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

	// smtp.SendMail tidak menerima context, jadi batas waktunya dijaga di
	// sini. Tanpa itu, server surel yang menggantung akan menahan permintaan
	// selama apa pun - dan permintaan yang memanggilnya adalah permintaan
	// pengguna.
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

// compose menyusun pesan RFC 5322.
//
// Nilai header dibersihkan dari CR dan LF.
//
// Tanpa itu, sebuah nilai yang mengandung baris baru bisa menyisipkan header
// tambahan ke dalam pesan yang kita kirim atas nama sendiri - Reply-To yang
// mengarahkan balasan ke penyerang, misalnya.
//
// Ia TIDAK bisa menambah penerima lewat transport ini: smtp.SendMail
// menetapkan penerima dari amplop SMTP, dan header di badan pesan tidak
// menyentuhnya. Membersihkannya tetap wajib, tetapi alasannya bukan itu.
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
