package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// TestTheProgramDayIsCountedFromOne moves the rule that used to live in
// DashboardResource, and tests it on both sides of every boundary.
//
// The legacy system computed it inside the JSON-assembling class: the only
// place this rule lived was the presentation layer, so anyone who needed the
// same number had to copy it.
func TestTheProgramDayIsCountedFromOne(t *testing.T) {
	owner, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}

	// A four-week program starting on 5 January 2026.
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	program, err := domain.NewProgram(owner, domain.DifficultyStandard, start, 4, start)
	if err != nil {
		t.Fatalf("NewProgram: %v", err)
	}

	total := program.DurationDays()
	if total != 28 {
		t.Fatalf("a four-week program lasts %d days, want 28", total)
	}

	for _, tc := range []struct {
		name string
		on   time.Time
		want int
	}{
		{"the day before it starts", start.AddDate(0, 0, -1), 0},
		{"the first day is day one", start, 1},
		{"the second day", start.AddDate(0, 0, 1), 2},
		{"the last day inside the program", start.AddDate(0, 0, 27), 28},
		{"the end date itself is over", start.AddDate(0, 0, 28), total},
		{"long after it ended it does not keep growing", start.AddDate(0, 0, 200), total},
		// The time of day must not shift the answer: the question is about days,
		// not about elapsed time.
		{"late in the day is still that day", start.Add(23 * time.Hour), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := program.DayOn(tc.on); got != tc.want {
				t.Errorf("on %s the program is at day %d, want %d",
					tc.on.Format(time.DateOnly), got, tc.want)
			}
		})
	}
}
