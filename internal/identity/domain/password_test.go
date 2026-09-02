package domain_test

import (
	"strings"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func TestNewPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "accepts eight characters", input: "12345678"},
		{name: "accepts a long passphrase", input: "correct horse battery staple"},
		{name: "rejects seven characters", input: "1234567", wantErr: true},
		{name: "rejects empty", input: "", wantErr: true},
		{name: "rejects whitespace only", input: "        ", wantErr: true},
		{name: "rejects beyond the hashing input limit", input: strings.Repeat("a", 1025), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewPassword(tt.input)
			if tt.wantErr && err == nil {
				t.Fatalf("NewPassword(len=%d) succeeded, want an error", len(tt.input))
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("NewPassword(len=%d): %v", len(tt.input), err)
			}
		})
	}
}

// Kata sandi tidak boleh bocor lewat log atau pesan galat. Satu-satunya
// pertahanan yang bekerja tanpa disiplin manusia adalah membuat tipenya
// menolak mencetak dirinya sendiri.
func TestPasswordNeverPrintsItself(t *testing.T) {
	t.Parallel()

	secret := "hunter2-and-then-some"
	p, err := domain.NewPassword(secret)
	if err != nil {
		t.Fatal(err)
	}

	if got := p.String(); strings.Contains(got, secret) {
		t.Errorf("String() leaked the password: %q", got)
	}
	if got := p.GoString(); strings.Contains(got, secret) {
		t.Errorf("GoString() leaked the password: %q", got)
	}
	if p.Expose() != secret {
		t.Error("Expose() must return the real value; it is the only way out")
	}
}

// Panjang minimum sengaja tidak dinaikkan dari yang lama, tetapi batas
// atas ditambahkan: argon2 menerima masukan sepanjang apa pun, dan
// membiarkannya berarti seseorang bisa mengirim megabita untuk membebani
// CPU pada endpoint yang tidak butuh autentikasi.
func TestPasswordUpperBoundGuardsHashingCost(t *testing.T) {
	t.Parallel()

	if _, err := domain.NewPassword(strings.Repeat("x", 1024)); err != nil {
		t.Errorf("1024 characters should be accepted: %v", err)
	}
	if _, err := domain.NewPassword(strings.Repeat("x", 1025)); err == nil {
		t.Error("1025 characters should be rejected")
	}
}
