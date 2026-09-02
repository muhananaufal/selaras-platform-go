package mail_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	identitymail "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/mail"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	platformmail "github.com/muhananaufal/selaras-platform-go/internal/platform/mail"
)

// Test ini mengirim surel SUNGGUHAN ke Mailpit lalu membacanya kembali lewat
// HTTP. Palsuan tidak bisa membuktikan yang penting di sini: bahwa pesannya
// benar-benar terkirim, benar-benar sampai ke alamat yang dituju, dan
// benar-benar membawa tokennya - dan justru itu satu-satunya andaian yang
// menopang seluruh keamanan alur reset.
func mailpit(t *testing.T) (smtpHost string, smtpPort int, apiURL string) {
	t.Helper()

	host := os.Getenv("TEST_SMTP_HOST")
	rawPort := os.Getenv("TEST_SMTP_PORT")
	api := os.Getenv("TEST_MAILPIT_URL")

	if host == "" || rawPort == "" || api == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_SMTP_HOST, TEST_SMTP_PORT and TEST_MAILPIT_URL must be set; " +
				"integration tests must not be skipped in CI")
		}
		t.Skip("mailpit is not configured; start the stack with 'task up' and export the variables")
	}

	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatalf("TEST_SMTP_PORT is not a number: %v", err)
	}
	return host, port, strings.TrimRight(api, "/")
}

// clearMailbox mengosongkan Mailpit sebelum dan sesudah test, supaya pesan
// dari test lain tidak tertukar dengan yang sedang diperiksa.
func clearMailbox(t *testing.T, apiURL string) {
	t.Helper()

	clean := func() {
		req, err := http.NewRequest(http.MethodDelete, apiURL+"/api/v1/messages", nil)
		if err != nil {
			t.Fatalf("building the delete request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("clearing the mailbox: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body: %v", err)
		}
	}

	clean()
	t.Cleanup(clean)
}

type mailpitMessage struct {
	ID      string                     `json:"ID"`
	To      []struct{ Address string } `json:"To"`
	Subject string                     `json:"Subject"`
}

func waitForMessage(t *testing.T, apiURL string) mailpitMessage {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(apiURL + "/api/v1/messages")
		if err != nil {
			t.Fatalf("listing messages: %v", err)
		}

		var listing struct {
			Messages []mailpitMessage `json:"messages"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&listing)
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body: %v", err)
		}
		if decodeErr != nil {
			t.Fatalf("decoding the listing: %v", decodeErr)
		}

		if len(listing.Messages) > 0 {
			return listing.Messages[0]
		}
	}

	t.Fatal("no message arrived within the deadline")
	return mailpitMessage{}
}

func messageBody(t *testing.T, apiURL, id string) string {
	t.Helper()

	resp, err := http.Get(apiURL + "/api/v1/message/" + id)
	if err != nil {
		t.Fatalf("fetching the message: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body: %v", err)
		}
	}()

	var message struct {
		Text string `json:"Text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&message); err != nil {
		t.Fatalf("decoding the message: %v", err)
	}
	return message.Text
}

func TestTheResetLinkActuallyArrivesCarryingItsToken(t *testing.T) {
	host, port, apiURL := mailpit(t)
	clearMailbox(t, apiURL)

	sender, err := platformmail.NewSMTP(platformmail.Config{
		Host: host, Port: port, From: "selaras@example.test", Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSMTP: %v", err)
	}

	links, err := identitymail.NewResetLinkSender(sender, "https://app.example.test/")
	if err != nil {
		t.Fatalf("NewResetLinkSender: %v", err)
	}

	recipient, err := domain.NewEmail("person@example.test")
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}
	token, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}

	if err := links.SendResetLink(context.Background(), recipient, token); err != nil {
		t.Fatalf("SendResetLink: %v", err)
	}

	message := waitForMessage(t, apiURL)
	if len(message.To) != 1 || message.To[0].Address != "person@example.test" {
		t.Errorf("recipients = %+v; want exactly the address asked for", message.To)
	}
	if !strings.Contains(message.Subject, "password") {
		t.Errorf("subject = %q; want it to say what the message is about", message.Subject)
	}

	body := messageBody(t, apiURL, message.ID)

	// Yang paling penting: tokennya benar-benar ada di dalamnya. Tautan reset
	// tanpa token adalah alur yang tampak bekerja dan tidak pernah bisa
	// diselesaikan siapa pun.
	if !strings.Contains(body, token.Expose()) {
		t.Error("the message does not carry the token")
	}
	if !strings.Contains(body, "https://app.example.test/reset-password?token=") {
		t.Errorf("the message does not carry a usable link:\n%s", body)
	}

	// Dan pesannya menjelaskan apa yang terjadi bila ini bukan permintaannya.
	if !strings.Contains(strings.ToLower(body), "was not you") {
		t.Error("the message does not tell the reader what to do if it was not them")
	}
}

// Nilai yang mengandung CR atau LF bisa menyisipkan header tambahan ke dalam
// pesan yang kita kirim atas nama sendiri.
//
// Yang TIDAK bisa dilakukannya lewat transport ini adalah menambah penerima:
// smtp.SendMail menetapkan penerima dari amplop SMTP, dan header di badan
// pesan tidak menyentuhnya. Yang nyata adalah header palsu yang terbaca
// klien surel - Reply-To yang mengarahkan balasan ke penyerang, atau
// Content-Type yang mengubah cara pesannya ditampilkan.
func TestHeaderInjectionIsRefused(t *testing.T) {
	host, port, apiURL := mailpit(t)
	clearMailbox(t, apiURL)

	sender, err := platformmail.NewSMTP(platformmail.Config{
		Host: host, Port: port,
		From:    "selaras@example.test",
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSMTP: %v", err)
	}

	err = sender.Send(context.Background(), platformmail.Message{
		To:      "person@example.test",
		Subject: "Reset\r\nReply-To: attacker@example.test",
		Body:    "hello",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	message := waitForMessage(t, apiURL)
	raw := rawMessage(t, apiURL, message.ID)

	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "reply-to:") {
			t.Errorf("the injected header became a real header: %q", line)
		}
	}

	// Nilainya tetap terkirim, hanya digabung menjadi satu baris - menolak
	// pesannya sama sekali akan mengubah karakter aneh di judul menjadi
	// kegagalan pengiriman.
	if !strings.Contains(message.Subject, "attacker@example.test") {
		t.Errorf("subject = %q; want the value kept, flattened onto one line", message.Subject)
	}
}

// rawMessage mengambil sumber mentah pesan, satu-satunya cara memeriksa
// header apa yang benar-benar terkirim.
func rawMessage(t *testing.T, apiURL, id string) string {
	t.Helper()

	resp, err := http.Get(apiURL + "/api/v1/message/" + id + "/raw")
	if err != nil {
		t.Fatalf("fetching the raw message: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body: %v", err)
		}
	}()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the raw message: %v", err)
	}
	return string(raw)
}

func TestNewSMTPRefusesIncompleteConfiguration(t *testing.T) {
	cases := map[string]platformmail.Config{
		"no host": {Port: 1025, From: "a@b.co"},
		"no port": {Host: "localhost", From: "a@b.co"},
		"no from": {Host: "localhost", Port: 1025},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := platformmail.NewSMTP(cfg); err == nil {
				t.Error("NewSMTP accepted an incomplete configuration")
			}
		})
	}
}

func TestNewResetLinkSenderRefusesMissingDependencies(t *testing.T) {
	sender, err := platformmail.NewSMTP(platformmail.Config{
		Host: "localhost", Port: 1025, From: "a@b.co",
	})
	if err != nil {
		t.Fatalf("NewSMTP: %v", err)
	}

	if _, err := identitymail.NewResetLinkSender(nil, "https://app.test"); err == nil {
		t.Error("accepted a nil sender")
	}
	if _, err := identitymail.NewResetLinkSender(sender, ""); err == nil {
		t.Error("accepted an empty frontend url")
	}
}
