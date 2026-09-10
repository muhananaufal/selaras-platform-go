package domain_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// wib is the time zone the real users are in. It is not UTC, and that is its
// purpose here: some date and clock rules are only wrong in a zone that is
// not UTC, and a test that runs entirely in UTC would never touch them.
var wib = time.FixedZone("WIB", 7*60*60)

func mustUser(t *testing.T) domain.UserID {
	t.Helper()

	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("parsing a user id: %v", err)
	}
	return id
}

// ---------------------------------------------------------------- waktu makan

// TestMealTimeFollowsTheClock tests D10 on BOTH sides of every boundary.
//
// Testing only the middle of each range proves nothing about the boundaries,
// and the boundaries are the only place this rule can go wrong.
func TestMealTimeFollowsTheClock(t *testing.T) {
	for _, tc := range []struct {
		hour int
		want domain.MealTime
	}{
		{0, domain.MealDinner},          // Past midnight is still evening.
		{2, domain.MealDinner},          // The "remainder" property, inherited knowingly.
		{4, domain.MealDinner},          // Sesaat sebelum sarapan dimulai.
		{5, domain.MealBreakfast},       // Lower bound of breakfast, inclusive.
		{9, domain.MealBreakfast},       // Last hour of breakfast.
		{10, domain.MealLunch},          // Lower bound of lunch, inclusive.
		{14, domain.MealLunch},          // Last hour of lunch.
		{15, domain.MealAfternoonSnack}, // Lower bound of the afternoon snack.
		{17, domain.MealAfternoonSnack}, // Last hour of the afternoon snack.
		{18, domain.MealDinner},         // Makan malam dimulai.
		{23, domain.MealDinner},
	} {
		at := time.Date(2026, 9, 3, tc.hour, 30, 0, 0, wib)
		if got := domain.MealTimeAt(at); got != tc.want {
			t.Errorf("at %02d:30 the meal time is %q, want %q", tc.hour, got, tc.want)
		}
	}
}

// TestTheGuideDateIsLocalMidnight guards the guide date in a non-UTC zone.
//
// This is a regression test for a wrong truncation: now.Truncate(24h) truncates
// from the UTC epoch, so 05:00 WIB - 22:00 UTC the PREVIOUS DAY - lands on
// yesterday's date. Every morning guide would be recorded on the wrong day, and
// only in the real users' time zone.
func TestTheGuideDateIsLocalMidnight(t *testing.T) {
	// 05:00 WIB on 3 September = 22:00 UTC on 2 September.
	at := time.Date(2026, 9, 3, 5, 0, 0, 0, wib)

	guide, err := domain.NewGuide(mustUser(t), validInput(), nil, at)
	if err != nil {
		t.Fatalf("creating a guide: %v", err)
	}

	year, month, day := guide.Date.Date()
	if year != 2026 || month != time.September || day != 3 {
		t.Fatalf("the guide is dated %04d-%02d-%02d, want 2026-09-03", year, month, day)
	}
	if h, m, s := guide.Date.Clock(); h != 0 || m != 0 || s != 0 {
		t.Errorf("the guide date carries a time of day: %02d:%02d:%02d", h, m, s)
	}
}

// TestTheMealTimeIsFrozenAtCreation proves the meal time is not recomputed.
func TestTheMealTimeIsFrozenAtCreation(t *testing.T) {
	morning := time.Date(2026, 9, 3, 7, 0, 0, 0, wib)

	guide, err := domain.NewGuide(mustUser(t), validInput(), nil, morning)
	if err != nil {
		t.Fatalf("creating a guide: %v", err)
	}
	if guide.MealTime != domain.MealBreakfast {
		t.Fatalf("a guide asked for at 07:00 has meal time %q", guide.MealTime)
	}

	// The guide is read in the evening. Its meal time is STILL breakfast: what
	// is stored is the context at the time it was requested.
	if got := domain.MealTimeAt(morning.Add(13 * time.Hour)); got == guide.MealTime {
		t.Fatalf("the test proves nothing: 20:00 and 07:00 give the same meal time %q", got)
	}
	if guide.MealTime != domain.MealBreakfast {
		t.Errorf("the meal time changed to %q merely by reading the clock", guide.MealTime)
	}
}

// ----------------------------------------------------------------- preferensi

// TestAPartialUpdateLeavesUntouchedFieldsAlone is the B16 regression test.
//
// In the legacy system, one PATCH carrying only allergies WIPED the user's
// tastes and kitchen equipment, because its repository overwrote the whole
// JSON column with whichever fields happened to be sent. That was silent data
// loss: no error, and the user only noticed when their suggestions changed.
func TestAPartialUpdateLeavesUntouchedFieldsAlone(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	prefs, err := domain.NewPreferences(mustUser(t), now)
	if err != nil {
		t.Fatalf("creating preferences: %v", err)
	}

	// The user fills in all of their preferences.
	full := domain.PreferencesPatch{
		Allergies:        ptr("udang"),
		BudgetLevel:      ptr(domain.BudgetStandard),
		CookingStyle:     ptr(domain.CookingBatchMealPrep),
		TasteProfiles:    ptr([]string{"pedas", "gurih"}),
		KitchenEquipment: ptr([]string{"wajan", "rice cooker"}),
	}
	if err := prefs.Apply(full, now); err != nil {
		t.Fatalf("applying the full patch: %v", err)
	}

	// Then they change only their allergy note.
	later := now.Add(time.Hour)
	if err := prefs.Apply(domain.PreferencesPatch{Allergies: ptr("udang, kepiting")}, later); err != nil {
		t.Fatalf("applying the partial patch: %v", err)
	}

	if prefs.Allergies != "udang, kepiting" {
		t.Errorf("the allergy note is %q", prefs.Allergies)
	}
	if prefs.BudgetLevel != domain.BudgetStandard {
		t.Errorf("the budget level was wiped to %q by a patch that never mentioned it", prefs.BudgetLevel)
	}
	if prefs.CookingStyle != domain.CookingBatchMealPrep {
		t.Errorf("the cooking style was wiped to %q", prefs.CookingStyle)
	}
	if len(prefs.TasteProfiles) != 2 {
		t.Errorf("the taste profiles were wiped to %v", prefs.TasteProfiles)
	}
	if len(prefs.KitchenEquipment) != 2 {
		t.Errorf("the kitchen equipment was wiped to %v", prefs.KitchenEquipment)
	}
}

// TestAnExplicitlyEmptyListIsNotTheSameAsAnAbsentOne is the other side of
// B16.
//
// A partial update must still ALLOW deliberate emptying. Otherwise a
// preference once filled in could never be cleared again.
func TestAnExplicitlyEmptyListIsNotTheSameAsAnAbsentOne(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	prefs, err := domain.NewPreferences(mustUser(t), now)
	if err != nil {
		t.Fatalf("creating preferences: %v", err)
	}
	if err := prefs.Apply(domain.PreferencesPatch{
		TasteProfiles: ptr([]string{"pedas"}),
	}, now); err != nil {
		t.Fatalf("applying: %v", err)
	}

	if err := prefs.Apply(domain.PreferencesPatch{
		TasteProfiles: ptr([]string{}),
	}, now); err != nil {
		t.Fatalf("emptying the taste profiles: %v", err)
	}
	if len(prefs.TasteProfiles) != 0 {
		t.Errorf("an explicitly emptied list still holds %v", prefs.TasteProfiles)
	}
	// An empty slice, not nil: nil becomes NULL in a column that is NOT NULL.
	if prefs.TasteProfiles == nil {
		t.Error("the emptied list is nil, which cannot be written to a NOT NULL column")
	}
}

// TestARejectedPatchChangesNothing guards the all-or-nothing property.
func TestARejectedPatchChangesNothing(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	prefs, err := domain.NewPreferences(mustUser(t), now)
	if err != nil {
		t.Fatalf("creating preferences: %v", err)
	}
	if err := prefs.Apply(domain.PreferencesPatch{
		Allergies:     ptr("udang"),
		TasteProfiles: ptr([]string{"pedas"}),
	}, now); err != nil {
		t.Fatalf("applying: %v", err)
	}

	// The first field is valid, the last is not. The first must NOT already
	// have been written.
	bad := domain.PreferencesPatch{
		Allergies:        ptr("tidak ada"),
		KitchenEquipment: ptr([]string{"   "}),
	}
	if err := prefs.Apply(bad, now.Add(time.Hour)); !errors.Is(err, domain.ErrBlankTag) {
		t.Fatalf("a blank tag was accepted, or reported as %v", err)
	}

	if prefs.Allergies != "udang" {
		t.Errorf("a rejected patch still changed the allergy note to %q", prefs.Allergies)
	}
	if !prefs.UpdatedAt.Equal(now) {
		t.Errorf("a rejected patch still moved updated_at to %v", prefs.UpdatedAt)
	}
}

func TestPreferenceTagsAreCleanedAndBounded(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	t.Run("duplicates are dropped and whitespace trimmed", func(t *testing.T) {
		prefs, err := domain.NewPreferences(mustUser(t), now)
		if err != nil {
			t.Fatalf("creating preferences: %v", err)
		}
		if err := prefs.Apply(domain.PreferencesPatch{
			TasteProfiles: ptr([]string{" pedas ", "pedas", "gurih"}),
		}, now); err != nil {
			t.Fatalf("applying: %v", err)
		}

		want := []string{"pedas", "gurih"}
		if len(prefs.TasteProfiles) != len(want) {
			t.Fatalf("the taste profiles are %v, want %v", prefs.TasteProfiles, want)
		}
		for i := range want {
			if prefs.TasteProfiles[i] != want[i] {
				t.Errorf("tag %d is %q, want %q", i, prefs.TasteProfiles[i], want[i])
			}
		}
	})

	t.Run("an over-long tag is refused", func(t *testing.T) {
		prefs, _ := domain.NewPreferences(mustUser(t), now)
		long := make([]rune, 51)
		for i := range long {
			long[i] = 'a'
		}
		if err := prefs.Apply(domain.PreferencesPatch{
			TasteProfiles: ptr([]string{string(long)}),
		}, now); !errors.Is(err, domain.ErrTagTooLong) {
			t.Fatalf("a 51-rune tag was reported as %v", err)
		}
	})

	t.Run("an unbounded list is refused", func(t *testing.T) {
		prefs, _ := domain.NewPreferences(mustUser(t), now)
		many := make([]string, 31)
		for i := range many {
			many[i] = string(rune('a'+i%26)) + string(rune('0'+i/26))
		}
		if err := prefs.Apply(domain.PreferencesPatch{
			KitchenEquipment: ptr(many),
		}, now); !errors.Is(err, domain.ErrTooManyTags) {
			t.Fatalf("31 tags were reported as %v", err)
		}
	})

	t.Run("an over-long allergy note is refused", func(t *testing.T) {
		prefs, _ := domain.NewPreferences(mustUser(t), now)
		long := make([]rune, 1001)
		for i := range long {
			long[i] = 'a'
		}
		if err := prefs.Apply(domain.PreferencesPatch{
			Allergies: ptr(string(long)),
		}, now); !errors.Is(err, domain.ErrAllergiesTooLong) {
			t.Fatalf("a 1001-rune note was reported as %v", err)
		}
	})
}

func TestUnknownPreferenceValuesAreRefused(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)
	prefs, _ := domain.NewPreferences(mustUser(t), now)

	// "Hemat" is the legacy Indonesian label. It is NOT a valid value here,
	// and accepting it silently would smuggle a display label into the
	// database.
	if err := prefs.Apply(domain.PreferencesPatch{
		BudgetLevel: ptr(domain.BudgetLevel("Hemat")),
	}, now); !errors.Is(err, domain.ErrInvalidBudgetLevel) {
		t.Errorf("the legacy label \"Hemat\" was reported as %v", err)
	}

	if err := prefs.Apply(domain.PreferencesPatch{
		CookingStyle: ptr(domain.CookingStyle("Masak Cepat Setiap Saat")),
	}, now); !errors.Is(err, domain.ErrInvalidCookingStyle) {
		t.Errorf("a legacy cooking style was reported as %v", err)
	}

	// Empty is VALID: it means "not chosen yet".
	if err := prefs.Apply(domain.PreferencesPatch{
		BudgetLevel: ptr(domain.BudgetUnspecified),
	}, now); err != nil {
		t.Errorf("clearing the budget level was refused: %v", err)
	}
}

func TestAnEmptyPatchIsRecognised(t *testing.T) {
	if !(domain.PreferencesPatch{}).IsEmpty() {
		t.Error("a patch with no fields is not reported as empty")
	}
	if (domain.PreferencesPatch{Allergies: ptr("")}).IsEmpty() {
		t.Error("a patch that explicitly clears the allergy note is reported as empty")
	}
}

// ------------------------------------------------------------------- panduan

func TestGuideInputRequiresTheThreeDailyAnswers(t *testing.T) {
	for name, mutate := range map[string]func(*domain.GuideInput){
		"no plan type":         func(in *domain.GuideInput) { in.PlanType = "" },
		"no time availability": func(in *domain.GuideInput) { in.TimeAvailability = "" },
		"no energy level":      func(in *domain.GuideInput) { in.EnergyLevel = "" },
		"no cuisine":           func(in *domain.GuideInput) { in.CuisinePreference = "   " },
		"unknown plan type":    func(in *domain.GuideInput) { in.PlanType = "Masak di Rumah" },
		"unknown craving":      func(in *domain.GuideInput) { in.CravingType = "Berkuah & Hangat" },
		"unknown social":       func(in *domain.GuideInput) { in.SocialContext = "Sendiri" },
	} {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			if err := in.Validate(); err == nil {
				t.Fatal("the input was accepted")
			}
		})
	}

	// The craving and the social context MAY be empty: not everyone is craving
	// something, and not every meal has company.
	in := validInput()
	in.CravingType = domain.CravingUnspecified
	in.SocialContext = domain.SocialUnspecified
	if err := in.Validate(); err != nil {
		t.Errorf("an input without a craving or a social context was refused: %v", err)
	}
}

func TestAGuideStartsPendingAndWithoutData(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	guide, err := domain.NewGuide(mustUser(t), validInput(), nil, now)
	if err != nil {
		t.Fatalf("creating a guide: %v", err)
	}

	if guide.Status != domain.GuidePending {
		t.Errorf("a new guide has status %q", guide.Status)
	}
	if len(guide.Data) != 0 {
		t.Errorf("a new guide already carries data: %s", guide.Data)
	}
	if guide.Chosen {
		t.Error("a new guide is already marked chosen")
	}
	// An empty context becomes an empty JSON object, not NULL: the column is
	// NOT NULL.
	if string(guide.Context) != "{}" {
		t.Errorf("an absent context was stored as %q, want {}", guide.Context)
	}
}

func TestAGuideBecomesReadyOnlyWithUsableData(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)
	later := now.Add(20 * time.Second)

	newGuide := func(t *testing.T) *domain.Guide {
		t.Helper()

		g, err := domain.NewGuide(mustUser(t), validInput(), nil, now)
		if err != nil {
			t.Fatalf("creating a guide: %v", err)
		}
		return g
	}

	t.Run("empty data is refused", func(t *testing.T) {
		g := newGuide(t)
		if err := g.MarkReady(nil, later); !errors.Is(err, domain.ErrEmptyGuideData) {
			t.Fatalf("empty data was reported as %v", err)
		}
		if g.Status != domain.GuidePending {
			t.Errorf("the guide moved to %q despite the refusal", g.Status)
		}
	})

	t.Run("data that is not json is refused", func(t *testing.T) {
		g := newGuide(t)
		if err := g.MarkReady(json.RawMessage("maaf, AI sedang sibuk"), later); !errors.Is(err, domain.ErrInvalidGuideData) {
			t.Fatalf("a plain-text reply was reported as %v", err)
		}
		if g.Status != domain.GuidePending {
			t.Errorf("the guide moved to %q despite the refusal", g.Status)
		}
	})

	t.Run("usable data is accepted once", func(t *testing.T) {
		g := newGuide(t)
		data := json.RawMessage(`{"suggestions":[{"dish_name":"Pepes ikan"}]}`)

		if err := g.MarkReady(data, later); err != nil {
			t.Fatalf("usable data was refused: %v", err)
		}
		if g.Status != domain.GuideReady {
			t.Fatalf("the guide has status %q", g.Status)
		}
		if !g.UpdatedAt.Equal(later) {
			t.Errorf("updated_at is %v, want %v", g.UpdatedAt, later)
		}

		// The second time is refused: a REDELIVERY from Kafka must not overwrite
		// a guide that has arrived with content from another attempt.
		if err := g.MarkReady(data, later); !errors.Is(err, domain.ErrGuideNotPending) {
			t.Errorf("a second delivery was reported as %v", err)
		}
	})

	t.Run("a failed guide keeps no data", func(t *testing.T) {
		g := newGuide(t)
		if err := g.MarkFailed(later); err != nil {
			t.Fatalf("marking failed: %v", err)
		}
		if g.Status != domain.GuideFailed {
			t.Fatalf("the guide has status %q", g.Status)
		}
		if len(g.Data) != 0 {
			t.Errorf("a failed guide carries data: %s", g.Data)
		}
		// A guide that has already failed cannot turn into a success with content
		// arriving later.
		if err := g.MarkReady(json.RawMessage(`{"a":1}`), later); !errors.Is(err, domain.ErrGuideNotPending) {
			t.Errorf("a failed guide accepted data, reported as %v", err)
		}
	})
}

func TestAGuideNeedsAnOwner(t *testing.T) {
	if _, err := domain.NewGuide(domain.UserID{}, validInput(), nil, time.Now()); !errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("an ownerless guide was reported as %v", err)
	}
	if _, err := domain.NewPreferences(domain.UserID{}, time.Now()); !errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("ownerless preferences were reported as %v", err)
	}
}

// ------------------------------------------------------------------- halaman

func TestPageNormalisationBoundsTheRequest(t *testing.T) {
	for _, tc := range []struct {
		in, want domain.Page
	}{
		{domain.Page{}, domain.Page{Number: 1, Size: 20}},
		{domain.Page{Number: -3, Size: 0}, domain.Page{Number: 1, Size: 20}},
		{domain.Page{Number: 2, Size: 5}, domain.Page{Number: 2, Size: 5}},
		// The legacy system returned the WHOLE history; here there is a ceiling.
		{domain.Page{Number: 1, Size: 5000}, domain.Page{Number: 1, Size: 100}},
	} {
		if got := tc.in.Normalise(); got != tc.want {
			t.Errorf("%+v normalises to %+v, want %+v", tc.in, got, tc.want)
		}
	}

	if got := (domain.Page{Number: 3, Size: 20}).Offset(); got != 40 {
		t.Errorf("page 3 of 20 starts at offset %d, want 40", got)
	}
	if got := (domain.Page{Number: 1, Size: 20}).Offset(); got != 0 {
		t.Errorf("page 1 starts at offset %d, want 0", got)
	}
}

// validInput is a valid daily input, used as the starting point.
func validInput() domain.GuideInput {
	return domain.GuideInput{
		PlanType:          domain.PlanCookAtHome,
		TimeAvailability:  domain.TimeQuick,
		EnergyLevel:       domain.EnergyTired,
		CuisinePreference: "Masakan Sunda",
		CravingType:       domain.CravingSoupyAndWarm,
		SocialContext:     domain.SocialWithFamily,
	}
}

func ptr[T any](v T) *T { return &v }
