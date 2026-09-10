package score

// Category is the cardiovascular risk category.
//
// Its values equal the codes the legacy system used, so clients that already
// handle them need not change (ADR-005).
type Category string

const (
	CategoryUnspecified Category = ""
	CategoryLowModerate Category = "LOW_MODERATE"
	CategoryHigh        Category = "HIGH"
	CategoryVeryHigh    Category = "VERY_HIGH"
)

// CategoryFor determines the risk category from age and percentage.
//
// This is a LOOKUP TABLE, not a judgement. It is deterministic, and the result
// is the same every time for the same input.
//
// The legacy system did not compute it at all: it ASKED THE LANGUAGE MODEL to
// apply this table, through three lines of instruction inside the
// personalisation prompt, then stored whatever the model answered as the
// user's risk category (B19). No check afterwards. A clinical classification
// that can be computed with six comparisons must not depend on whether the
// model happened to read its instructions carefully.
//
// Another consequence of the old way: a user whose personalisation had not
// arrived - or had failed - had no category at all, and their dashboard showed
// "N/A" as their health status.
//
// The thresholds are taken from the prompt template in this repository
// (internal/llm/prompt/templates/personalization.v1.tmpl section 4.1), which
// is a copy of the legacy rule. That is the source, and it is named as such:
// not a clinical publication I checked myself. Changing these numbers demands
// a better source than that document, not merely agreement among developers.
func CategoryFor(age int, riskPercent float64) Category {
	// Age bounds are INCLUSIVE below and exclusive above, following the
	// readings "Age < 50", "Age 50-69", "Age >= 70".
	switch {
	case age < 50:
		return categorise(riskPercent, 2.5, 7.5)
	case age < 70:
		return categorise(riskPercent, 5, 10)
	default:
		return categorise(riskPercent, 7.5, 15)
	}
}

// categorise compares the percentage against two thresholds.
//
// The lower threshold is EXCLUSIVE for LOW_MODERATE ("< 2.5%") and inclusive
// for HIGH ("2.5-7.49%"). Exactly on the threshold means moving up a category,
// not down - rounding towards the milder side on a boundary value is the kind
// of mistake a health application must not make.
func categorise(percent, high, veryHigh float64) Category {
	switch {
	case percent < high:
		return CategoryLowModerate
	case percent < veryHigh:
		return CategoryHigh
	default:
		return CategoryVeryHigh
	}
}
