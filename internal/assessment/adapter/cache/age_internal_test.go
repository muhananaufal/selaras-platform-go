package cache

import (
	"testing"
	"time"
)

// TestAgeIsCountedFromTheBirthdayThatHasActuallyHappened guards a direct
// input to the risk model.
//
// Age is not just a difference in years. Someone born in December is still
// a year younger for the first eleven months, and counting it as a
// difference in years alone would raise their age for that whole period -
// which raises the risk reported to them.
//
// The "today" date is supplied explicitly, not taken from the clock: a test
// whose result depends on when it runs changes without anyone changing the
// code.
func TestAgeIsCountedFromTheBirthdayThatHasActuallyHappened(t *testing.T) {
	date := func(s string) time.Time {
		parsed, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad date in the test itself: %v", err)
		}
		return parsed
	}

	cases := []struct {
		name  string
		birth string
		on    string
		want  int
	}{
		{"ulang tahun sudah lewat", "1970-01-05", "2026-09-03", 56},
		{"ulang tahun belum lewat", "1970-12-25", "2026-09-03", 55},
		{"tepat pada hari ulang tahun", "1970-09-03", "2026-09-03", 56},
		{"sehari sebelum ulang tahun", "1970-09-04", "2026-09-03", 55},
		{"sehari setelah ulang tahun", "1970-09-02", "2026-09-03", 56},
		{"bayi yang belum berulang tahun", "2026-01-01", "2026-09-03", 0},

		// A date of birth in the future is impossible - the domain refuses it -
		// but the cache can receive anything that arrives through an event. A
		// negative age would be an impossible input to the risk model.
		{"tanggal lahir di masa depan", "2030-01-01", "2026-09-03", 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ageOn(date(c.birth), date(c.on)); got != c.want {
				t.Fatalf("ageOn(%s, %s) = %d, want %d", c.birth, c.on, got, c.want)
			}
		})
	}
}

// TestALeapDayBirthdayDoesNotJumpAYear guards 29 February.
//
// YearDay shifts by one in a leap year, and a naive comparison makes someone
// born on 1 March count as having their birthday a day early in leap years.
// The difference is one day, but it hits everyone born after February, every
// four years.
func TestALeapDayBirthdayDoesNotJumpAYear(t *testing.T) {
	date := func(s string) time.Time {
		parsed, _ := time.Parse("2006-01-02", s)
		return parsed
	}

	// 2028 is a leap year. Someone born on 1 March 1990 is 37 on 1 March 2028,
	// and 36 on 29 February 2028.
	if got := ageOn(date("1990-03-01"), date("2028-03-01")); got != 38 {
		t.Errorf("on their birthday in a leap year the age is %d, want 38", got)
	}
	if got := ageOn(date("1990-03-01"), date("2028-02-29")); got != 37 {
		t.Errorf("the day before their birthday in a leap year the age is %d, want 37", got)
	}
}
