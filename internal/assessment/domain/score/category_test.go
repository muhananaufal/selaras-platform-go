package score_test

import (
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

// TestTheRiskCategoryFollowsTheTable tests BOTH sides of every threshold.
//
// There are six thresholds, and each one is a place where this rule can go
// wrong. Testing only the middle of each range proves nothing about the
// boundaries - and the boundaries are what decide whether someone is told
// their risk is high or moderate.
//
// Source of the numbers:
// internal/llm/prompt/templates/personalization.v1.tmpl section 4.1, a copy
// of the legacy rule.
func TestTheRiskCategoryFollowsTheTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     int
		percent float64
		want    score.Category
	}{
		// Age < 50: < 2.5 low-moderate; 2.5-7.49 high; >= 7.5 very high.
		{"under 50, well below", 30, 0.4, score.CategoryLowModerate},
		{"under 50, just below the first threshold", 49, 2.49, score.CategoryLowModerate},
		{"under 50, exactly on the first threshold", 49, 2.5, score.CategoryHigh},
		{"under 50, just below the second", 49, 7.49, score.CategoryHigh},
		{"under 50, exactly on the second", 49, 7.5, score.CategoryVeryHigh},
		{"under 50, far above", 49, 40, score.CategoryVeryHigh},

		// Age 50-69: < 5 low-moderate; 5-9.99 high; >= 10 very high. Age 50 uses
		// THIS table, not the one below it.
		{"exactly 50 uses the middle table", 50, 4.9, score.CategoryLowModerate},
		{"50-69, exactly on the first threshold", 55, 5, score.CategoryHigh},
		{"50-69, just below the second", 69, 9.99, score.CategoryHigh},
		{"50-69, exactly on the second", 69, 10, score.CategoryVeryHigh},

		// Age >= 70: < 7.5 low-moderate; 7.5-14.99 high; >= 15 very high. Age 70
		// uses THIS table, not the one above it - and that is the difference
		// easiest to get wrong: at 8% someone aged 69 is "very high", while
		// someone aged 70 is "high".
		{"exactly 70 uses the oldest table", 70, 7.4, score.CategoryLowModerate},
		{"70+, exactly on the first threshold", 70, 7.5, score.CategoryHigh},
		{"70+, just below the second", 80, 14.99, score.CategoryHigh},
		{"70+, exactly on the second", 80, 15, score.CategoryVeryHigh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := score.CategoryFor(tc.age, tc.percent); got != tc.want {
				t.Errorf("at age %d with %.2f%% the category is %q, want %q",
					tc.age, tc.percent, got, tc.want)
			}
		})
	}
}

// TestTheAgeBandsDoNotOverlap proves the three tables really differ.
//
// If all three happened to be the same, every test above would still pass while
// testing nothing about the choice of table.
func TestTheAgeBandsDoNotOverlap(t *testing.T) {
	// Eight percent: very high below 50, high at 50-69, and still high at 70+
	// - but 7.4 percent separates the last one.
	const percent = 8

	if got := score.CategoryFor(49, percent); got != score.CategoryVeryHigh {
		t.Errorf("at 49 with 8%% the category is %q", got)
	}
	if got := score.CategoryFor(50, percent); got != score.CategoryHigh {
		t.Errorf("at 50 with 8%% the category is %q", got)
	}

	// And at 7.4 percent, all three answer differently.
	if a, b, c := score.CategoryFor(49, 7.4), score.CategoryFor(55, 7.4), score.CategoryFor(70, 7.4); //
	a != score.CategoryHigh || b != score.CategoryHigh || c != score.CategoryLowModerate {
		t.Errorf("at 7.4%% the three bands answer %q / %q / %q; the oldest band should be low-moderate", a, b, c)
	}
}
