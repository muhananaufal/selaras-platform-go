package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

func mustUserID(t *testing.T) domain.UserID {
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

func newProgram(t *testing.T, weeks int) *domain.Program {
	t.Helper()
	p, err := domain.NewProgram(mustUserID(t), domain.DifficultyStandard,
		day("2026-01-05"), weeks, day("2026-01-05"))
	if err != nil {
		t.Fatalf("NewProgram: %v", err)
	}
	return p
}

// TestTheEndDateIsTheOnlySourceOfTruth is F4-18, finding B5.
//
// The legacy system stored end_date and then ignored it: its completer used
// created_at + 28 days. Two sources of truth for one fact means one of them
// is wrong, and the wrong one is the one nobody looks at - a six-week program
// ended on day 28 according to its completer, and on day 42 according to its
// screen.
func TestTheEndDateIsTheOnlySourceOfTruth(t *testing.T) {
	cases := []struct {
		weeks int
		want  string
	}{
		{1, "2026-01-12"},
		{4, "2026-02-02"},
		{6, "2026-02-16"},
		{12, "2026-03-30"},
	}

	for _, c := range cases {
		p := newProgram(t, c.weeks)
		if got := p.EndDate.Format(time.DateOnly); got != c.want {
			t.Errorf("a %d-week program ends on %s, want %s", c.weeks, got, c.want)
		}
		if got := p.DurationDays(); got != c.weeks*7 {
			t.Errorf("a %d-week program lasts %d days, want %d", c.weeks, got, c.weeks*7)
		}
	}

	// And a six-week program does NOT end on day 28.
	six := newProgram(t, 6)
	if six.HasEnded(day("2026-02-02")) {
		t.Fatal("a six-week program reported itself finished on day 28; that is B5 exactly")
	}
	if !six.HasEnded(day("2026-02-16")) {
		t.Fatal("a six-week program did not report itself finished on its own end date")
	}
}

// TestAProgramCannotEndBeforeItStarts guards the invariant that makes every
// remaining-days computation make sense.
func TestAProgramCannotEndBeforeItStarts(t *testing.T) {
	if _, err := domain.NewProgram(mustUserID(t), domain.DifficultyStandard,
		day("2026-01-05"), 0, day("2026-01-05")); err == nil {
		t.Fatal("a zero-week program was accepted")
	}

	p := newProgram(t, 4)
	p.EndDate = p.StartDate.AddDate(0, 0, -1)
	if err := p.Validate(); !errors.Is(err, domain.ErrEndBeforeStart) {
		t.Fatalf("Validate returned %v, want ErrEndBeforeStart", err)
	}
}

// TestTogglingFollowsD4 guards the status transitions.
func TestTogglingFollowsD4(t *testing.T) {
	p := newProgram(t, 4)
	now := day("2026-01-10")

	if p.Status != domain.StatusActive {
		t.Fatalf("a new program starts as %q, want active", p.Status)
	}
	if err := p.Toggle(now); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if p.Status != domain.StatusPaused {
		t.Fatalf("after one toggle the status is %q, want paused", p.Status)
	}
	if err := p.Toggle(now); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if p.Status != domain.StatusActive {
		t.Fatalf("after two toggles the status is %q, want active", p.Status)
	}

	// A completed program cannot be changed. Allowing it would mean a program
	// whose graduation report has already been produced could be run again,
	// and that report would become a report about something not yet finished.
	p.Status = domain.StatusCompleted
	if err := p.Toggle(now); !errors.Is(err, domain.ErrProgramCompleted) {
		t.Fatalf("Toggle on a completed program returned %v, want ErrProgramCompleted", err)
	}
	if p.Status != domain.StatusCompleted {
		t.Fatalf("a refused toggle still changed the status to %q", p.Status)
	}
}

// TestANonActiveProgramFreezesInteraction is D5.
func TestANonActiveProgramFreezesInteraction(t *testing.T) {
	p := newProgram(t, 4)

	if err := p.EnsureInteractive(); err != nil {
		t.Fatalf("an active program refused interaction: %v", err)
	}

	for _, status := range []domain.Status{domain.StatusPaused, domain.StatusCompleted} {
		p.Status = status
		if err := p.EnsureInteractive(); !errors.Is(err, domain.ErrProgramNotActive) {
			t.Errorf("a %s program returned %v, want ErrProgramNotActive", status, err)
		}
	}
}

// TestOwnershipIsCheckedAgainstTheUser guards S9.
//
// Ownership is compared against user_id, not against the profile id as in
// the legacy system. Two identity patterns for one question means two
// places to get it wrong.
func TestOwnershipIsCheckedAgainstTheUser(t *testing.T) {
	p := newProgram(t, 4)

	if !p.BelongsTo(p.UserID) {
		t.Fatal("a program does not belong to its own owner")
	}
	if p.BelongsTo(mustUserID(t)) {
		t.Fatal("a program belongs to a stranger")
	}

	// An empty owner must not match anything, including another empty owner.
	var zero domain.UserID
	p.UserID = zero
	if p.BelongsTo(zero) {
		t.Fatal("a program with no owner matched an empty user id")
	}
}

// TestDifficultyValuesArePreservedExactly guards the client contract.
func TestDifficultyValuesArePreservedExactly(t *testing.T) {
	for _, raw := range []string{"Santai & Bertahap", "Standar & Konsisten", "Intensif & Menantang"} {
		got, err := domain.NewDifficulty(raw)
		if err != nil {
			t.Errorf("NewDifficulty(%q): %v", raw, err)
			continue
		}
		if string(got) != raw {
			t.Errorf("NewDifficulty(%q) came back as %q", raw, got)
		}
	}

	for _, raw := range []string{"", "standar", "Standar", "Relaxed & Gradual"} {
		if _, err := domain.NewDifficulty(raw); !errors.Is(err, domain.ErrInvalidDifficulty) {
			t.Errorf("NewDifficulty(%q) was accepted", raw)
		}
	}
}

// TestTheStartDateIsADateNotAMoment keeps the end-of-program comparison
// predictable.
func TestTheStartDateIsADateNotAMoment(t *testing.T) {
	at := time.Date(2026, 1, 5, 23, 47, 12, 0, time.UTC)

	p, err := domain.NewProgram(mustUserID(t), domain.DifficultyGentle, at, 4, at)
	if err != nil {
		t.Fatalf("NewProgram: %v", err)
	}

	if h := p.StartDate.Hour(); h != 0 {
		t.Fatalf("the start date kept the hour %d; whether a program has ended would depend on what time it was created", h)
	}
	if p.EndDate.Format(time.DateOnly) != "2026-02-02" {
		t.Fatalf("the end date is %s", p.EndDate.Format(time.DateOnly))
	}
}

// TestSlugsAreNotGuessable guards the public id.
func TestSlugsAreNotGuessable(t *testing.T) {
	seen := make(map[string]bool, 500)
	for range 500 {
		p := newProgram(t, 4)
		if len(p.Slug) < 16 {
			t.Fatalf("the slug %q is only %d characters", p.Slug, len(p.Slug))
		}
		if seen[p.Slug] {
			t.Fatalf("slug %q was generated twice in 500 tries", p.Slug)
		}
		seen[p.Slug] = true
	}
}
