package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Galat panduan.
var (
	ErrInvalidPlanType         = errors.New("invalid plan type")
	ErrInvalidTimeAvailability = errors.New("invalid time availability")
	ErrInvalidEnergyLevel      = errors.New("invalid energy level")
	ErrInvalidCravingType      = errors.New("invalid craving type")
	ErrInvalidSocialContext    = errors.New("invalid social context")
	ErrMissingCuisine          = errors.New("a cuisine preference is required")
	ErrCuisineTooLong          = errors.New("the cuisine preference is too long")
	ErrGuideNotPending         = errors.New("the guide is no longer pending")
	ErrEmptyGuideData          = errors.New("a ready guide must carry its data")
	ErrInvalidGuideData        = errors.New("the guide data is not valid json")
)

const maxCuisineRunes = 100

// PlanType is the choice between cooking at home and eating out.
type PlanType string

const (
	PlanCookAtHome PlanType = "cook_at_home"
	PlanEatOut     PlanType = "eat_out"
)

// TimeAvailability is how much time there is today.
type TimeAvailability string

const (
	TimeQuick   TimeAvailability = "quick"
	TimeRelaxed TimeAvailability = "relaxed"
)

// EnergyLevel is how energetic the user feels today.
type EnergyLevel string

const (
	EnergyEnergetic EnergyLevel = "energetic"
	EnergyOrdinary  EnergyLevel = "ordinary"
	EnergyTired     EnergyLevel = "tired"
)

// CravingType may be empty: not everyone is craving something.
type CravingType string

const (
	CravingUnspecified   CravingType = ""
	CravingSoupyAndWarm  CravingType = "soupy_and_warm"
	CravingGrilled       CravingType = "grilled"
	CravingFreshAndLight CravingType = "fresh_and_light"
	CravingQuickStirFry  CravingType = "quick_stir_fry"
)

// SocialContext boleh kosong.
type SocialContext string

const (
	SocialUnspecified SocialContext = ""
	SocialAlone       SocialContext = "alone"
	SocialWithFriends SocialContext = "with_friends"
	SocialWithPartner SocialContext = "with_partner"
	SocialWithFamily  SocialContext = "with_family"
)

// MealTime is the meal time currently under way.
type MealTime string

const (
	MealBreakfast      MealTime = "breakfast"
	MealLunch          MealTime = "lunch"
	MealAfternoonSnack MealTime = "afternoon_snack"
	MealDinner         MealTime = "dinner"
)

// MealTimeAt determines the meal time from the local clock (D10).
//
// The boundaries are exactly the legacy ones, including the "remainder"
// property: two in the morning yields dinner. That is not a mistake inherited
// without thought - someone opening the app at two in the morning is more
// likely finishing their night than starting their morning, and breakfast at
// two would feel more wrong than dinner.
//
// The time is taken as an argument, not read from time.Now() inside. A
// function that reads the clock itself can only be tested at whatever hour the
// test happens to run, and the three boundaries here would never be touched.
func MealTimeAt(t time.Time) MealTime {
	switch h := t.Hour(); {
	case h >= 5 && h < 10:
		return MealBreakfast
	case h >= 10 && h < 15:
		return MealLunch
	case h >= 15 && h < 18:
		return MealAfternoonSnack
	default:
		return MealDinner
	}
}

// dateOf takes the LOCAL date of a time.
//
// Not now.Truncate(24 * time.Hour): Truncate cuts from the UTC epoch, so in any
// zone that is not UTC it lands on a shifted hour - in WIB, 07:00 on the 3rd
// stays 07:00, while 05:00 becomes the 2nd. Someone's guide would be recorded
// on the wrong day, every morning.
func dateOf(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

// GuideInput is the daily input the user provides.
type GuideInput struct {
	PlanType          PlanType
	TimeAvailability  TimeAvailability
	EnergyLevel       EnergyLevel
	CuisinePreference string
	CravingType       CravingType
	SocialContext     SocialContext
}

// Validate checks the daily input.
//
// The first three fields are REQUIRED, the same as the legacy system:
// without any one of them, the prompt loses the part that makes today's
// advice different from any other advice, and what remains is only a list of
// generic dishes.
func (in GuideInput) Validate() error {
	switch in.PlanType {
	case PlanCookAtHome, PlanEatOut:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidPlanType, in.PlanType)
	}

	switch in.TimeAvailability {
	case TimeQuick, TimeRelaxed:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTimeAvailability, in.TimeAvailability)
	}

	switch in.EnergyLevel {
	case EnergyEnergetic, EnergyOrdinary, EnergyTired:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidEnergyLevel, in.EnergyLevel)
	}

	switch in.CravingType {
	case CravingUnspecified, CravingSoupyAndWarm, CravingGrilled,
		CravingFreshAndLight, CravingQuickStirFry:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidCravingType, in.CravingType)
	}

	switch in.SocialContext {
	case SocialUnspecified, SocialAlone, SocialWithFriends,
		SocialWithPartner, SocialWithFamily:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSocialContext, in.SocialContext)
	}

	cuisine := strings.TrimSpace(in.CuisinePreference)
	if cuisine == "" {
		return ErrMissingCuisine
	}
	if utf8.RuneCountInString(cuisine) > maxCuisineRunes {
		return fmt.Errorf("%w: %d runes, max %d",
			ErrCuisineTooLong, utf8.RuneCountInString(cuisine), maxCuisineRunes)
	}
	return nil
}

// GuideStatus is the state of guide generation.
type GuideStatus string

const (
	GuidePending GuideStatus = "pending"
	GuideReady   GuideStatus = "ready"
	GuideFailed  GuideStatus = "failed"
)

// Guide is one daily menu guide.
//
// It is born in the pending state: its generation is asynchronous, unlike
// the legacy system which waited for Gemini inside the HTTP request (B14).
type Guide struct {
	ID     ID
	UserID UserID

	Date     time.Time
	MealTime MealTime
	Input    GuideInput

	Status GuideStatus

	// Context is the context assembled when the request was made, stored so a
	// suggestion can be explained again later.
	Context json.RawMessage

	// Data is empty until the guide arrives.
	Data json.RawMessage

	Chosen bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewGuide creates a new guide request.
//
// mealTime is derived from now and FROZEN here. Recomputing it when the guide
// is read would make a breakfast suggestion show up as a dinner suggestion
// just because the user opened the app again in the evening.
func NewGuide(userID UserID, in GuideInput, context json.RawMessage, now time.Time) (*Guide, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: a guide needs an owner", ErrInvalidID)
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if len(context) > 0 && !json.Valid(context) {
		return nil, fmt.Errorf("%w: generation context", ErrInvalidGuideData)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}

	in.CuisinePreference = strings.TrimSpace(in.CuisinePreference)

	// An empty context is stored as an empty JSON object, not as NULL: the
	// column is NOT NULL, and `{}` can still be read by any tool that reads
	// JSON. NULL would force every reader to handle two shapes.
	if len(context) == 0 {
		context = json.RawMessage(`{}`)
	}

	return &Guide{
		ID:        id,
		UserID:    userID,
		Date:      dateOf(now),
		MealTime:  MealTimeAt(now),
		Input:     in,
		Status:    GuidePending,
		Context:   context,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// MarkReady installs a guide that has arrived.
//
// The invariant is exactly the CHECK in the database: what is ready HAS
// content. It is enforced in both places deliberately - here so the caller
// gets an explainable error, there so even a forgotten write path cannot get
// past it.
func (g *Guide) MarkReady(data json.RawMessage, now time.Time) error {
	if g.Status != GuidePending {
		return fmt.Errorf("%w: %s", ErrGuideNotPending, g.Status)
	}
	if len(data) == 0 {
		return ErrEmptyGuideData
	}
	if !json.Valid(data) {
		return ErrInvalidGuideData
	}

	g.Status = GuideReady
	g.Data = data
	g.UpdatedAt = now
	return nil
}

// MarkFailed marks a guide that never arrived.
//
// The status changes and the content STAYS empty. A failed guide is
// deliberately not given placeholder content: an apology text stored as
// guide_data would be shown to the user as menu advice, and there would be no
// way to tell it from a real suggestion afterwards.
func (g *Guide) MarkFailed(now time.Time) error {
	if g.Status != GuidePending {
		return fmt.Errorf("%w: %s", ErrGuideNotPending, g.Status)
	}
	g.Status = GuideFailed
	g.UpdatedAt = now
	return nil
}
