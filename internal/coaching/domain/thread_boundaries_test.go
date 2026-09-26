package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// The thread limits were only tested well past the line (mutation testing:
// each <= / > at these checks survived). Each case sits exactly ON the limit
// and one past it; "é" is two bytes per rune, so a check that counted bytes
// instead of runes fails as well.

func TestAThreadTitleOfExactlyTheLimitIsAccepted(t *testing.T) {
	atLimit := strings.Repeat("é", 100)

	if _, err := domain.NewThread(programID(t), atLimit, "halo", day("2026-01-10")); err != nil {
		t.Fatalf("a 100-rune title was refused: %v", err)
	}
	if _, err := domain.NewThread(programID(t), atLimit+"é", "halo", day("2026-01-10")); !errors.Is(err, domain.ErrTitleTooLong) {
		t.Fatalf("a 101-rune title returned %v, want ErrTitleTooLong", err)
	}

	thread, err := domain.NewThread(programID(t), "Awal", "halo", day("2026-01-10"))
	if err != nil {
		t.Fatalf("NewThread: %v", err)
	}
	if err := thread.Rename(atLimit, day("2026-01-11")); err != nil {
		t.Fatalf("renaming to a 100-rune title was refused: %v", err)
	}
	if err := thread.Rename(atLimit+"é", day("2026-01-11")); !errors.Is(err, domain.ErrTitleTooLong) {
		t.Fatalf("renaming to a 101-rune title returned %v, want ErrTitleTooLong", err)
	}
}

func TestADerivedThreadTitleIsCutOnlyPastItsLength(t *testing.T) {
	exactly := strings.Repeat("é", 45)
	if got := domain.DeriveTitle(exactly); got != exactly {
		t.Fatalf("a 45-rune message became the title %q; it fits and must stay whole", got)
	}
	longer := exactly + "é"
	if got := domain.DeriveTitle(longer); got == longer || !strings.HasPrefix(got, exactly) {
		t.Fatalf("a 46-rune message became the title %q; it must be cut after 45 runes", got)
	}
}

func TestAUserMessageOfExactlyTheLimitIsAccepted(t *testing.T) {
	atLimit := strings.Repeat("a", 16*1024)
	if _, err := domain.NewUserMessage(programID(t), atLimit, day("2026-01-10")); err != nil {
		t.Fatalf("a message of exactly 16 KiB was refused: %v", err)
	}
	if _, err := domain.NewUserMessage(programID(t), atLimit+"a", day("2026-01-10")); !errors.Is(err, domain.ErrMessageTooLong) {
		t.Fatalf("a message one byte over 16 KiB returned %v, want ErrMessageTooLong", err)
	}
}
