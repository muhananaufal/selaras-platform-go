package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
)

func owner(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	return id
}

func day(s string) time.Time {
	parsed, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return parsed
}

// TestATitleIsDerivedFromTheFirstMessage adalah D12.
func TestATitleIsDerivedFromTheFirstMessage(t *testing.T) {
	c, err := domain.NewConversation(owner(t), "",
		"Apakah kopi berpengaruh pada tekanan darah saya?", day("2026-01-05"))
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	if c.Title != "Apakah kopi berpengaruh pada tekanan darah saya?"[:45]+"..." {
		t.Fatalf("the derived title is %q", c.Title)
	}

	// Judul yang diberikan menang.
	named, err := domain.NewConversation(owner(t), "Soal kopi", "apa pun", day("2026-01-05"))
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	if named.Title != "Soal kopi" {
		t.Fatalf("the given title was replaced by %q", named.Title)
	}
}

// TestAConversationWithoutAMessageGetsTheDefaultTitle menjaga percakapan yang
// dibuat sebelum ada pesan.
func TestAConversationWithoutAMessageGetsTheDefaultTitle(t *testing.T) {
	c, err := domain.NewConversation(owner(t), "", "", day("2026-01-05"))
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	if c.Title != domain.DefaultTitle {
		t.Fatalf("an unnamed conversation is titled %q, want %q", c.Title, domain.DefaultTitle)
	}
}

// TestATitleIsCutByRunesNotBytes menjaga karakter tidak terpotong di tengah.
func TestATitleIsCutByRunesNotBytes(t *testing.T) {
	title := domain.DeriveTitle(strings.Repeat("🥗", 60))

	if runes := len([]rune(title)); runes != 48 {
		t.Fatalf("the title is %d runes, want 45 plus the three-dot marker", runes)
	}
	for _, r := range title {
		if r == 0xFFFD {
			t.Fatal("the title was cut in the middle of a character")
		}
	}
}

// TestATitleIsOneLine menjaga tata letak daftar percakapan.
func TestATitleIsOneLine(t *testing.T) {
	title := domain.DeriveTitle("Baris pertama\nBaris kedua\t\tdengan   tab")

	if strings.ContainsAny(title, "\n\r\t") {
		t.Fatalf("the title carries control characters: %q", title)
	}
	if strings.Contains(title, "  ") {
		t.Fatalf("the title carries runs of spaces: %q", title)
	}
}

// TestOwnershipIsCheckedAgainstTheUser menjaga S9.
func TestOwnershipIsCheckedAgainstTheUser(t *testing.T) {
	mine := owner(t)
	c, err := domain.NewConversation(mine, "x", "y", day("2026-01-05"))
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}

	if !c.BelongsTo(mine) {
		t.Fatal("a conversation does not belong to its own owner")
	}
	if c.BelongsTo(owner(t)) {
		t.Fatal("a conversation belongs to a stranger")
	}

	var zero domain.UserID
	c.UserID = zero
	if c.BelongsTo(zero) {
		t.Fatal("a conversation with no owner matched an empty user id")
	}
}

// TestRenamingRefusesBlankAndOversizedTitles menjaga daftar percakapan.
func TestRenamingRefusesBlankAndOversizedTitles(t *testing.T) {
	c, _ := domain.NewConversation(owner(t), "Awal", "", day("2026-01-05"))

	if err := c.Rename("   ", day("2026-01-06")); !errors.Is(err, domain.ErrBlankTitle) {
		t.Errorf("a blank title returned %v, want ErrBlankTitle", err)
	}
	if c.Title != "Awal" {
		t.Errorf("a refused rename still changed the title to %q", c.Title)
	}

	if err := c.Rename(strings.Repeat("x", 101), day("2026-01-06")); !errors.Is(err, domain.ErrTitleTooLong) {
		t.Errorf("a 101-rune title returned %v, want ErrTitleTooLong", err)
	}

	if err := c.Rename("Judul baru", day("2026-01-06")); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if c.Title != "Judul baru" {
		t.Fatalf("the title is %q", c.Title)
	}
	if !c.UpdatedAt.Equal(day("2026-01-06")) {
		t.Fatal("renaming did not move updated_at; the list order would be stale")
	}
}

// TestAMessageIsTrimmedAndBounded menjaga isi pesan.
func TestAMessageIsTrimmedAndBounded(t *testing.T) {
	cid, _ := domain.NewID()
	now := day("2026-01-05")

	m, err := domain.NewMessage(cid, domain.RoleUser, "  halo  ", now)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if m.Content != "halo" {
		t.Fatalf("the content came back as %q; it should be trimmed", m.Content)
	}

	for _, blank := range []string{"", "   ", "\n\t"} {
		if _, err := domain.NewMessage(cid, domain.RoleUser, blank, now); !errors.Is(err, domain.ErrEmptyMessage) {
			t.Errorf("NewMessage(%q) returned %v, want ErrEmptyMessage", blank, err)
		}
	}

	huge := strings.Repeat("a", 17*1024)
	if _, err := domain.NewMessage(cid, domain.RoleUser, huge, now); !errors.Is(err, domain.ErrMessageTooLong) {
		t.Errorf("a 17KiB message returned %v, want ErrMessageTooLong", err)
	}
}

// TestOnlyTwoRolesExist menjaga peran yang dikirim ke penyedia LLM.
func TestOnlyTwoRolesExist(t *testing.T) {
	for _, raw := range []string{"user", "model"} {
		if _, err := domain.NewRole(raw); err != nil {
			t.Errorf("NewRole(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "assistant", "system", "User"} {
		if _, err := domain.NewRole(raw); !errors.Is(err, domain.ErrInvalidRole) {
			t.Errorf("NewRole(%q) was accepted", raw)
		}
	}

	cid, _ := domain.NewID()
	if _, err := domain.NewMessage(cid, domain.Role("assistant"), "x", day("2026-01-05")); !errors.Is(err, domain.ErrInvalidRole) {
		t.Error("a message with an unknown role was accepted")
	}
}

// TestPagingStaysWithinSaneBounds menjaga satu permintaan tidak meminta seluruh
// riwayat.
func TestPagingStaysWithinSaneBounds(t *testing.T) {
	cases := []struct {
		in         domain.Page
		wantNumber int
		wantSize   int
		wantOffset int
	}{
		{domain.Page{}, 1, 20, 0},
		{domain.Page{Number: -3, Size: -1}, 1, 20, 0},
		{domain.Page{Number: 3, Size: 10}, 3, 10, 20},
		{domain.Page{Number: 1, Size: 1000}, 1, 100, 0},
	}

	for _, c := range cases {
		got := c.in.Normalise()
		if got.Number != c.wantNumber || got.Size != c.wantSize {
			t.Errorf("%+v normalised to %+v, want number=%d size=%d",
				c.in, got, c.wantNumber, c.wantSize)
		}
		if got.Offset() != c.wantOffset {
			t.Errorf("%+v has offset %d, want %d", c.in, got.Offset(), c.wantOffset)
		}
	}
}

// TestSlugsAreNotGuessable menjaga id publik.
func TestSlugsAreNotGuessable(t *testing.T) {
	seen := make(map[string]bool, 500)
	for range 500 {
		c, err := domain.NewConversation(owner(t), "x", "", day("2026-01-05"))
		if err != nil {
			t.Fatalf("NewConversation: %v", err)
		}
		if len(c.Slug) < 16 {
			t.Fatalf("the slug %q is only %d characters", c.Slug, len(c.Slug))
		}
		if seen[c.Slug] {
			t.Fatalf("slug %q was generated twice in 500 tries", c.Slug)
		}
		seen[c.Slug] = true
	}
}
