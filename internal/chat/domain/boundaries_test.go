package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
)

// The limits below were only ever tested well past the line, so a limit that
// moved by one in either direction went unnoticed (mutation testing: every
// <= / > at these checks survived). Each case sits exactly ON the limit, and
// one past it. Titles use "é", two bytes per rune, so a check that counted
// bytes instead of runes would fail here as well.

func TestATitleOfExactlyTheLimitIsAccepted(t *testing.T) {
	atLimit := strings.Repeat("é", 100)

	if _, err := domain.NewConversation(owner(t), atLimit, "Halo", day("2026-01-05")); err != nil {
		t.Fatalf("a 100-rune title was refused: %v", err)
	}
	if _, err := domain.NewConversation(owner(t), atLimit+"é", "Halo", day("2026-01-05")); !errors.Is(err, domain.ErrTitleTooLong) {
		t.Fatalf("a 101-rune title returned %v, want ErrTitleTooLong", err)
	}

	c, _ := domain.NewConversation(owner(t), "Halo", "", day("2026-01-05"))
	if err := c.Rename(atLimit, day("2026-01-06")); err != nil {
		t.Fatalf("renaming to a 100-rune title was refused: %v", err)
	}
}

func TestADerivedTitleIsCutOnlyPastItsLength(t *testing.T) {
	exactly := strings.Repeat("é", 45)
	if got := domain.DeriveTitle(exactly); got != exactly {
		t.Fatalf("a 45-rune message became the title %q; it fits and must stay whole", got)
	}

	longer := exactly + "é"
	got := domain.DeriveTitle(longer)
	if got == longer || !strings.HasPrefix(got, exactly) {
		t.Fatalf("a 46-rune message became the title %q; it must be cut after 45 runes", got)
	}
}

func TestAMessageOfExactlyTheLimitIsAccepted(t *testing.T) {
	cid, _ := domain.NewID()
	atLimit := strings.Repeat("a", 16*1024)

	if _, err := domain.NewMessage(cid, domain.RoleUser, atLimit, day("2026-01-05")); err != nil {
		t.Fatalf("a message of exactly 16 KiB was refused: %v", err)
	}
	if _, err := domain.NewMessage(cid, domain.RoleUser, atLimit+"a", day("2026-01-05")); !errors.Is(err, domain.ErrMessageTooLong) {
		t.Fatalf("a message one byte over 16 KiB returned %v, want ErrMessageTooLong", err)
	}
}

func TestAPageOfOneStaysOne(t *testing.T) {
	if got := (domain.Page{Number: 1, Size: 1}).Normalise(); got.Size != 1 {
		t.Fatalf("a page of size 1 was normalised to size %d", got.Size)
	}
}
