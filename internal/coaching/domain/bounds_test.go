package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// The upper bounds below are not a product rule: they mirror storage.
// coaching_weeks.week_number is SMALLINT, so no program can hold more than
// domain.MaxWeeks weeks, and every value derived from its length (days,
// the day a program is on) fits the int32 fields of the contracts. Without
// them a curriculum with week 40000 passed validation and only failed at the
// INSERT, with a range error from Postgres instead of a named one.

func TestAProgramCannotBeLongerThanItsWeeksCanBeNumbered(t *testing.T) {
	if _, err := domain.NewProgram(mustUserID(t), domain.DifficultyStandard,
		day("2026-01-05"), domain.MaxWeeks+1, day("2026-01-05")); err == nil {
		t.Fatalf("a program of %d weeks was created, want an error", domain.MaxWeeks+1)
	}
	p := newProgram(t, domain.MaxWeeks)
	if got, want := p.DurationDays(), domain.MaxWeeks*7; got != want {
		t.Fatalf("a %d-week program lasts %d days, want %d", domain.MaxWeeks, got, want)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("the longest program fails validation: %v", err)
	}

	p.EndDate = p.EndDate.AddDate(0, 0, 1)
	if err := p.Validate(); err == nil {
		t.Fatal("a program one day longer than MaxWeeks weeks passed validation")
	}
}

func TestACurriculumCannotNumberWeeksBeyondStorage(t *testing.T) {
	week := func(n int) *domain.Week { return &domain.Week{WeekNumber: n, Title: "Pekan"} }

	c := &domain.Curriculum{Title: "Program", Weeks: []*domain.Week{week(1), week(domain.MaxWeeks + 1)}}
	if err := c.Validate(); !errors.Is(err, domain.ErrInvalidWeekNumber) {
		t.Fatalf("week %d: Validate returned %v, want ErrInvalidWeekNumber", domain.MaxWeeks+1, err)
	}

	c = &domain.Curriculum{Title: "Program", Weeks: []*domain.Week{week(domain.MaxWeeks)}}
	if err := c.Validate(); err != nil {
		t.Fatalf("week %d, the largest storable number: Validate returned %v", domain.MaxWeeks, err)
	}

	// And the smallest: week 1 on its own. The case above that holds week 1
	// fails anyway (on its second week), so it never showed that week 1 is
	// accepted (mutation testing: < 1 -> <= 1 survived).
	c = &domain.Curriculum{Title: "Program", Weeks: []*domain.Week{week(1)}}
	if err := c.Validate(); err != nil {
		t.Fatalf("week 1, the smallest number: Validate returned %v", err)
	}
	c = &domain.Curriculum{Title: "Program", Weeks: []*domain.Week{week(0)}}
	if err := c.Validate(); !errors.Is(err, domain.ErrInvalidWeekNumber) {
		t.Fatalf("week 0: Validate returned %v, want ErrInvalidWeekNumber", err)
	}
}

// TestADaylightSavingWeekIsStillSevenDays pins the rounding in the day count:
// the week in which Amsterdam moves its clocks forward is 167 hours long,
// and truncating 167/24 counted it as six days.
func TestADaylightSavingWeekIsStillSevenDays(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("loading the time zone: %v", err)
	}
	start := time.Date(2026, time.March, 26, 0, 0, 0, 0, amsterdam) // clocks move on 29 March
	p, err := domain.NewProgram(mustUserID(t), domain.DifficultyStandard, start, 1, start)
	if err != nil {
		t.Fatalf("NewProgram: %v", err)
	}
	if got := p.EndDate.Sub(p.StartDate); got != 167*time.Hour {
		t.Fatalf("the test week is %v long; the premise needs 167h", got)
	}
	if got := p.DurationDays(); got != 7 {
		t.Fatalf("a one-week program across the clock change lasts %d days, want 7", got)
	}
	if got := p.DayOn(start.AddDate(0, 0, 6)); got != 7 {
		t.Fatalf("the last day of that week is day %d, want 7", got)
	}
}
