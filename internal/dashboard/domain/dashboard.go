// Package domain holds the dashboard rules.
//
// It imports nothing from the adapters, and a boundary test guards that.
package domain

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// Errors that callers recognise.
var (
	ErrInvalidID       = errors.New("invalid id")
	ErrEventFromFuture = errors.New("the event is dated in the future")
)

// UserID points at identity.users (ADR-024).
type UserID struct{ v uuid.UUID }

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: user %q", ErrInvalidID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// Trend is the direction of the risk change between the last two
// assessments.
type Trend string

const (
	// TrendInsufficientData is used when there is only one assessment so far.
	//
	// It is NOT "stable". The legacy system answered stable with the text "Ini
	// adalah analisis pertama Anda", mixing two different states into one
	// value - a client drawing an arrow for "stable" would draw it for someone
	// who has nothing to compare against at all.
	TrendInsufficientData Trend = "insufficient_data"

	TrendImproving Trend = "improving"
	TrendStable    Trend = "stable"
	TrendWorsening Trend = "worsening"
)

// trendDeadband is the change regarded as NOT meaningful.
//
// Zero point one percentage point, the same as the legacy system. It exists so
// rounding and trivial answer changes are not reported as "improving" or
// "worsening" - news about a health risk that changes direction every time
// someone refills the questionnaire stops being believed.
const trendDeadband = 0.1

// TrendBetween states the direction of change from previous to latest.
//
// A nil previous means there is nothing to compare against yet.
func TrendBetween(latest float64, previous *float64) Trend {
	if previous == nil {
		return TrendInsufficientData
	}

	switch diff := latest - *previous; {
	case diff < -trendDeadband:
		return TrendImproving
	case diff > trendDeadband:
		return TrendWorsening
	default:
		return TrendStable
	}
}

// ChangeBetween is the size of the change, rounded to two decimal places - the
// same as the legacy system.
//
// Zero when there is nothing to compare against, AND zero when the change is
// inside the deadband: reporting a number too small to change the direction
// only makes the client show "stable, +0.04%".
func ChangeBetween(latest float64, previous *float64) float64 {
	if previous == nil {
		return 0
	}

	diff := latest - *previous
	if math.Abs(diff) <= trendDeadband {
		return 0
	}
	return math.Round(diff*100) / 100
}

// Assessment is one assessment in the dashboard history.
type Assessment struct {
	Slug           string
	AssessedAt     time.Time
	RiskPercentage float64
	RiskCategory   string
	ModelUsed      string
}

// Program is the summary of the running coaching program.
type Program struct {
	Slug       string
	Title      string
	Status     string
	CurrentDay int
	TotalDays  int

	// A nil Completion means not computed yet, NOT zero percent. Program
	// events are published from two places and only one of them counts tasks.
	Completion *float64
}

// Dashboard is one read-model row.
type Dashboard struct {
	UserID UserID

	Latest   *Assessment
	Previous *float64

	Total   int
	History []*Assessment
	Program *Program

	// ProjectedAt is the occurred_at of the last event that came in, not its
	// processing time. The difference between the two is the lag F7-06
	// measures.
	ProjectedAt time.Time
}

// Trend is the health direction of this user.
func (d *Dashboard) Trend() Trend {
	if d.Latest == nil {
		return TrendInsufficientData
	}
	return TrendBetween(d.Latest.RiskPercentage, d.Previous)
}

// Change is the size of the change.
func (d *Dashboard) Change() float64 {
	if d.Latest == nil {
		return 0
	}
	return ChangeBetween(d.Latest.RiskPercentage, d.Previous)
}

// IsEmpty says this user has never run an analysis.
//
// The gateway translates it into a welcome message, as the legacy system did.
// It is checked through the history, not through Latest: the two have to
// agree, and an empty history is the more fundamental state.
func (d *Dashboard) IsEmpty() bool { return d.Total == 0 }

// TrendWindow is the length of the risk chart window.
//
// Thirty days, the same as the legacy system.
const TrendWindow = 30 * 24 * time.Hour

// RiskTrend returns the chart points inside the window, OLDEST first.
//
// The order is deliberately the reverse of the history: a chart is read left
// to right as time moving forward, while a history list is read from the
// newest.
func (d *Dashboard) RiskTrend(now time.Time) []*Assessment {
	cutoff := now.Add(-TrendWindow)

	// An empty slice, not nil: nil becomes `null` in JSON, and a client
	// drawing the chart fails instead of drawing an empty chart.
	out := make([]*Assessment, 0, len(d.History))
	for i := len(d.History) - 1; i >= 0; i-- {
		if d.History[i].AssessedAt.Before(cutoff) {
			continue
		}
		out = append(out, d.History[i])
	}
	return out
}
