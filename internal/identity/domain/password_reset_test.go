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

// A token printed to a log is a leaked token. Password-reset logs are
// precisely the ones read most often when investigating a problem.
func TestAResetTokenCannotPrintItself(t *testing.T) {
	tok, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}
	secret := tok.Expose()

	for _, rendered := range []string{
		// %s is not tested: for a Stringer it goes through exactly the same path
		// as %v below.
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

// What is stored MUST be the hash. If the token itself were stored, one
// database dump would directly mean the ability to take over every account
// with a pending reset request.
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

	// Closes half of S1: a single-use token that can be used twice is a token
	// that stays valid after its owner is done with it.
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

// Already used wins over expired, and the order does not matter to the caller
// - both are refused alike. What matters is that a used token can NEVER be
// used again, even if the clock goes backwards.
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
