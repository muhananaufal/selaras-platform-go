package domain_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func TestEveryResetTokenIsDifferent(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok, err := domain.NewResetToken()
		if err != nil {
			t.Fatalf("NewResetToken: %v", err)
		}
		if seen[tok.Expose()] {
			t.Fatal("two generated reset tokens were identical")
		}
		seen[tok.Expose()] = true
	}
}

// Sebuah token yang tercetak ke log adalah token yang bocor. Log reset kata
// sandi justru yang paling sering dibaca saat menyelidiki masalah.
func TestAResetTokenCannotPrintItself(t *testing.T) {
	tok, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}
	secret := tok.Expose()

	for _, rendered := range []string{
		// %s tidak ikut diuji: bagi sebuah Stringer, ia melewati jalur yang
		// sama persis dengan %v di bawah.
		fmt.Sprintf("%v", tok),
		fmt.Sprintf("%+v", tok),
		fmt.Sprintf("%#v", tok),
		fmt.Sprint(tok),
		fmt.Sprintf("%v", struct{ T domain.ResetToken }{tok}),
	} {
		if strings.Contains(rendered, secret) {
			t.Errorf("a reset token printed itself: %s", rendered)
		}
	}
}

func TestParseResetTokenRejectsAnythingOfTheWrongShape(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":            "",
		"not base64":       "!!!not-base64!!!",
		"too short":        "c2hvcnQ",
		"too long":         strings.Repeat("A", 100),
		"padded but short": "YWJj",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := domain.ParseResetToken(raw); !errors.Is(err, domain.ErrResetTokenInvalid) {
				t.Errorf("ParseResetToken(%.20q) = %v; want ErrResetTokenInvalid", raw, err)
			}
		})
	}
}

func TestParseResetTokenAcceptsWhatWasGenerated(t *testing.T) {
	tok, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}
	parsed, err := domain.ParseResetToken(tok.Expose())
	if err != nil {
		t.Fatalf("ParseResetToken rejected a token it generated: %v", err)
	}
	if !domain.HashResetToken(parsed).Equal(domain.HashResetToken(tok)) {
		t.Error("a round-tripped token hashes differently")
	}
}

// Yang disimpan WAJIB hash-nya. Kalau tokennya sendiri yang disimpan, satu
// dump basis data langsung berarti kemampuan mengambil alih setiap akun yang
// sedang punya permintaan reset.
func TestTheStoredHashIsNotTheToken(t *testing.T) {
	id := mustUserID(t)

	reset, token, err := domain.NewPasswordReset(id, time.Now())
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}

	if strings.Contains(fmt.Sprintf("%x", reset.TokenHash), token.Expose()) {
		t.Error("the stored hash contains the token itself")
	}
	if reset.UserID != id {
		t.Errorf("user id = %s; want %s", reset.UserID, id)
	}
	if reset.UsedAt != nil {
		t.Error("a fresh reset request is already marked used")
	}
	if !reset.ExpiresAt.After(reset.CreatedAt) {
		t.Error("the request expires before it was created")
	}
}

func TestDifferentTokensHashDifferently(t *testing.T) {
	first, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}
	second, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}
	if domain.HashResetToken(first).Equal(domain.HashResetToken(second)) {
		t.Error("two different tokens produced the same hash")
	}
}

func TestRedeemAcceptsAFreshTokenExactlyOnce(t *testing.T) {
	now := time.Now()
	reset, _, err := domain.NewPasswordReset(mustUserID(t), now)
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}

	if err := reset.Redeem(now.Add(time.Minute)); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if reset.UsedAt == nil {
		t.Fatal("Redeem succeeded without marking the request used")
	}

	// Menutup separuh S1: token sekali pakai yang bisa dipakai dua kali
	// adalah token yang tetap berlaku setelah pemiliknya selesai memakainya.
	if err := reset.Redeem(now.Add(2 * time.Minute)); !errors.Is(err, domain.ErrResetTokenUsed) {
		t.Errorf("the second Redeem = %v; want ErrResetTokenUsed", err)
	}
}

func TestRedeemRefusesAnExpiredToken(t *testing.T) {
	now := time.Now()
	reset, _, err := domain.NewPasswordReset(mustUserID(t), now)
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}

	if err := reset.Redeem(reset.ExpiresAt); !errors.Is(err, domain.ErrResetTokenExpired) {
		t.Errorf("Redeem at the expiry instant = %v; want ErrResetTokenExpired", err)
	}
	if err := reset.Redeem(reset.ExpiresAt.Add(time.Second)); !errors.Is(err, domain.ErrResetTokenExpired) {
		t.Errorf("Redeem after expiry = %v; want ErrResetTokenExpired", err)
	}
	if reset.UsedAt != nil {
		t.Error("a refused Redeem still marked the request used")
	}
}

// Sudah dipakai menang atas sudah kedaluwarsa, dan urutannya tidak penting
// bagi pemanggil - keduanya sama-sama ditolak. Yang penting adalah token yang
// sudah dipakai TIDAK pernah bisa dipakai lagi, walau jamnya mundur.
func TestAUsedTokenStaysUsedEvenIfTheClockMovesBack(t *testing.T) {
	now := time.Now()
	reset, _, err := domain.NewPasswordReset(mustUserID(t), now)
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}
	if err := reset.Redeem(now); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	if err := reset.Redeem(now.Add(-time.Hour)); !errors.Is(err, domain.ErrResetTokenUsed) {
		t.Errorf("Redeem with a rewound clock = %v; want ErrResetTokenUsed", err)
	}
}

func mustUserID(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID()
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	return id
}
