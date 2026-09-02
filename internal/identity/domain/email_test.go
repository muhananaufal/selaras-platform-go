package domain_test

import (
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func TestNewEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "accepts a plain address", input: "user@example.com", want: "user@example.com"},
		{name: "lowercases so the same person cannot register twice", input: "User@Example.COM", want: "user@example.com"},
		{name: "trims surrounding whitespace", input: "  user@example.com  ", want: "user@example.com"},
		{name: "rejects empty", input: "", wantErr: true},
		{name: "rejects a missing at sign", input: "userexample.com", wantErr: true},
		{name: "rejects a missing domain", input: "user@", wantErr: true},
		{name: "rejects a missing local part", input: "@example.com", wantErr: true},
		{name: "rejects an embedded space", input: "us er@example.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.NewEmail(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewEmail(%q) succeeded, want an error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewEmail(%q): %v", tt.input, err)
			}
			if got.String() != tt.want {
				t.Errorf("NewEmail(%q) = %q, want %q", tt.input, got.String(), tt.want)
			}
		})
	}
}

// Alamat yang hanya berbeda huruf besar-kecil adalah orang yang sama.
// Tanpa ini, dua akun bisa lahir untuk satu alamat.
func TestEmailEqualityIgnoresCase(t *testing.T) {
	t.Parallel()

	a, err := domain.NewEmail("Person@Example.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := domain.NewEmail("person@example.COM")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("%q and %q should be the same address", a.String(), b.String())
	}
}
