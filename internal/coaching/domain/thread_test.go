package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

func programID(t *testing.T) domain.ID {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}

// TestAThreadTitleIsDerivedFromTheFirstMessage is D12.
func TestAThreadTitleIsDerivedFromTheFirstMessage(t *testing.T) {
	now := day("2026-01-10")

	thread, err := domain.NewThread(programID(t), "",
		"Saya kesulitan bangun pagi untuk jalan kaki, ada saran?", now)
	if err != nil {
		t.Fatalf("NewThread: %v", err)
	}
	if thread.Title != "Saya kesulitan bangun pagi untuk jalan kaki,..." {
		t.Fatalf("the derived title is %q", thread.Title)
	}

	// A given title wins.
	named, err := domain.NewThread(programID(t), "Soal tidur", "apa pun", now)
	if err != nil {
		t.Fatalf("NewThread: %v", err)
	}
	if named.Title != "Soal tidur" {
		t.Fatalf("the given title was replaced by %q", named.Title)
	}
}

// TestADerivedTitleIsCutByRunesNotBytes keeps characters from being split
// in the middle.
//
// Cutting by byte would split a multi-byte character and produce a title
// ending in a broken byte - rendered as an empty box, and possibly making
// its JSON invalid.
func TestADerivedTitleIsCutByRunesNotBytes(t *testing.T) {
	// Fifty emoji: 50 runes, 200 bytes.
	long := strings.Repeat("🏃", 50)

	title := domain.DeriveTitle(long)

	// 45 runes of content, plus the three-dot truncation marker.
	if runes := len([]rune(title)); runes != 48 {
		t.Fatalf("the title is %d runes, want 48", runes)
	}
	if !strings.HasSuffix(title, "...") {
		t.Fatalf("a truncated title carries no marker: %q", title)
	}
	if !isValidUTF8(title) {
		t.Fatal("the title was cut in the middle of a character")
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// TestADerivedTitleIsOneLine guards the layout of the thread list.
func TestADerivedTitleIsOneLine(t *testing.T) {
	title := domain.DeriveTitle("Baris pertama\nBaris kedua\t\tdengan   tab")

	if strings.ContainsAny(title, "\n\r\t") {
		t.Fatalf("the title carries control characters: %q", title)
	}
	if strings.Contains(title, "  ") {
		t.Fatalf("the title carries runs of spaces: %q", title)
	}
}

// TestAnEmptyFirstMessageFallsBackToTheDefaultTitle keeps a title present.
func TestAnEmptyFirstMessageFallsBackToTheDefaultTitle(t *testing.T) {
	for _, message := range []string{"", "   ", "\n\t "} {
		if got := domain.DeriveTitle(message); got != domain.DefaultThreadTitle {
			t.Errorf("DeriveTitle(%q) = %q, want the default", message, got)
		}
	}
}

// TestAThreadBelongsToExactlyOneProgram guards cross-program access.
//
// A thread of another program that happens to belong to the same user must
// still not be reachable through this program's slug.
func TestAThreadBelongsToExactlyOneProgram(t *testing.T) {
	mine := programID(t)
	theirs := programID(t)

	thread, err := domain.NewThread(mine, "x", "y", day("2026-01-10"))
	if err != nil {
		t.Fatalf("NewThread: %v", err)
	}

	if !thread.BelongsToProgram(mine) {
		t.Fatal("a thread does not belong to its own program")
	}
	if thread.BelongsToProgram(theirs) {
		t.Fatal("a thread belongs to another program")
	}

	var zero domain.ID
	thread.ProgramID = zero
	if thread.BelongsToProgram(zero) {
		t.Fatal("a thread with no program matched an empty id")
	}
}

// TestOnlyTwoRolesExist guards the roles sent to the LLM provider.
func TestOnlyTwoRolesExist(t *testing.T) {
	for _, raw := range []string{"user", "model"} {
		if _, err := domain.NewRole(raw); err != nil {
			t.Errorf("NewRole(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "assistant", "system", "User", "MODEL"} {
		if _, err := domain.NewRole(raw); !errors.Is(err, domain.ErrInvalidRole) {
			t.Errorf("NewRole(%q) was accepted", raw)
		}
	}
}

// TestAnEmptyMessageIsRefused guards against a message that says nothing.
func TestAnEmptyMessageIsRefused(t *testing.T) {
	tid := programID(t)
	now := day("2026-01-10")

	for _, text := range []string{"", "   ", "\n\t"} {
		if _, err := domain.NewUserMessage(tid, text, now); !errors.Is(err, domain.ErrEmptyMessage) {
			t.Errorf("NewUserMessage(%q) returned %v, want ErrEmptyMessage", text, err)
		}
	}

	if _, err := domain.NewMessage(tid, domain.RoleModel, nil, now); !errors.Is(err, domain.ErrEmptyMessage) {
		t.Errorf("a message with no content returned %v, want ErrEmptyMessage", err)
	}
}

// TestAnOversizedMessageIsRefused guards the prompt cost.
func TestAnOversizedMessageIsRefused(t *testing.T) {
	huge := strings.Repeat("a", 17*1024)

	_, err := domain.NewUserMessage(programID(t), huge, day("2026-01-10"))
	if !errors.Is(err, domain.ErrMessageTooLong) {
		t.Fatalf("a 17KiB message returned %v, want ErrMessageTooLong", err)
	}
}

// TestAMessageRoundTripsItsText guards the shape of its content.
func TestAMessageRoundTripsItsText(t *testing.T) {
	msg, err := domain.NewUserMessage(programID(t), "  halo pelatih  ", day("2026-01-10"))
	if err != nil {
		t.Fatalf("NewUserMessage: %v", err)
	}
	if msg.Role != domain.RoleUser {
		t.Fatalf("the role is %q, want user", msg.Role)
	}

	text, ok := msg.Text()
	if !ok {
		t.Fatal("the message carries no text")
	}
	if text != "halo pelatih" {
		t.Fatalf("the text came back as %q; it should be trimmed", text)
	}
}

// TestRenamingRefusesABlankTitle keeps a thread recognisable.
func TestRenamingRefusesABlankTitle(t *testing.T) {
	thread, err := domain.NewThread(programID(t), "Awal", "x", day("2026-01-10"))
	if err != nil {
		t.Fatalf("NewThread: %v", err)
	}

	if err := thread.Rename("   ", day("2026-01-11")); err == nil {
		t.Fatal("a blank title was accepted")
	}
	if thread.Title != "Awal" {
		t.Fatalf("a refused rename still changed the title to %q", thread.Title)
	}

	if err := thread.Rename("Judul baru", day("2026-01-11")); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if thread.Title != "Judul baru" {
		t.Fatalf("the title is %q", thread.Title)
	}
}

// TestAnOversizedTitleIsRefused guards the thread list.
func TestAnOversizedTitleIsRefused(t *testing.T) {
	long := strings.Repeat("x", 101)

	if _, err := domain.NewThread(programID(t), long, "y", day("2026-01-10")); !errors.Is(err, domain.ErrTitleTooLong) {
		t.Errorf("a 101-rune title was accepted at construction")
	}

	thread, _ := domain.NewThread(programID(t), "ok", "y", day("2026-01-10"))
	if err := thread.Rename(long, day("2026-01-11")); !errors.Is(err, domain.ErrTitleTooLong) {
		t.Errorf("a 101-rune title was accepted at rename")
	}
}
