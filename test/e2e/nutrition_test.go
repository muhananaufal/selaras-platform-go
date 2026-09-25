package e2e_test

import (
	"net/http"
	"testing"
	"time"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
)

// TestCulinaryPreferencesSurviveAPartialUpdate is B16 through four layers.
//
// In the legacy system one update carrying only allergies WIPED the user's
// tastes and kitchen equipment. The chain holding it back - the wire, the
// proto contract, the use case, SQL - breaks if ANY layer breaks, so it is
// tested from the outside.
func TestCulinaryPreferencesSurviveAPartialUpdate(t *testing.T) {
	c := newClient(t)
	c.register()

	// Never touched: the hub still opens, and its content is empty.
	empty, err := c.nutrition.GetHubData(c.ctx(), &edgev1.GetHubDataRequest{})
	if err != nil {
		t.Fatalf("a hub for a user with no preferences: %v", err)
	}
	if got := empty.GetPreferences().GetBudgetLevel(); got != nutritionv1.BudgetLevel_BUDGET_LEVEL_UNSPECIFIED {
		t.Errorf("an untouched budget level came back as %v", got)
	}

	allergies := "udang dan kepiting"
	budget := nutritionv1.BudgetLevel_BUDGET_LEVEL_THRIFTY
	style := nutritionv1.CookingStyle_COOKING_STYLE_QUICK_EVERY_TIME
	if _, err := c.nutrition.UpdatePreferences(c.ctx(), &edgev1.UpdatePreferencesRequest{
		Allergies:        &allergies,
		BudgetLevel:      &budget,
		CookingStyle:     &style,
		TasteProfiles:    &edgev1.StringList{Values: []string{"pedas", "gurih"}},
		KitchenEquipment: &edgev1.StringList{Values: []string{"wajan", "rice cooker"}},
	}); err != nil {
		t.Fatalf("saving preferences: %v", err)
	}

	// Then ONLY the allergies change.
	onlyAllergies := "udang, kepiting, dan kacang"
	partial, err := c.nutrition.UpdatePreferences(c.ctx(), &edgev1.UpdatePreferencesRequest{Allergies: &onlyAllergies})
	if err != nil {
		t.Fatalf("a partial update: %v", err)
	}
	p := partial.GetPreferences()
	if p.GetAllergies() != onlyAllergies {
		t.Errorf("the allergy note is %q", p.GetAllergies())
	}
	if p.GetBudgetLevel() != budget {
		t.Errorf("the budget level was wiped to %v by an update that never mentioned it", p.GetBudgetLevel())
	}
	if p.GetCookingStyle() != style {
		t.Errorf("the cooking style was wiped to %v", p.GetCookingStyle())
	}
	if len(p.GetTasteProfiles()) != 2 || len(p.GetKitchenEquipment()) != 2 {
		t.Errorf("a list was wiped: tastes %v, equipment %v", p.GetTasteProfiles(), p.GetKitchenEquipment())
	}

	// What is really stored, not only what was returned.
	hub, err := c.nutrition.GetHubData(c.ctx(), &edgev1.GetHubDataRequest{})
	if err != nil {
		t.Fatalf("reading the hub: %v", err)
	}
	if got := hub.GetPreferences().GetBudgetLevel(); got != budget {
		t.Errorf("the stored budget level is %v", got)
	}

	// DELIBERATE emptying still works - an empty StringList is "clear it",
	// an absent one is "leave it".
	cleared, err := c.nutrition.UpdatePreferences(c.ctx(), &edgev1.UpdatePreferencesRequest{
		TasteProfiles: &edgev1.StringList{},
	})
	if err != nil {
		t.Fatalf("emptying the taste profiles: %v", err)
	}
	if got := cleared.GetPreferences().GetTasteProfiles(); len(got) != 0 {
		t.Errorf("an explicitly emptied list still holds %v", got)
	}
	if got := cleared.GetPreferences().GetKitchenEquipment(); len(got) != 2 {
		t.Errorf("emptying one list also emptied the other: %v", got)
	}
}

// TestADailyGuideIsAskedForAndArrives is the F6 exit gate: the request returns
// at once, and the guide arrives later on the WatchDailyGuide stream - through
// the gateway, nutrition-svc, the outbox, Kafka, llm-worker, and back. The
// legacy system held the request while Gemini worked (B14).
func TestADailyGuideIsAskedForAndArrives(t *testing.T) {
	c := newClient(t)
	c.register()

	// The allergy note goes into the prompt, and it is the only part of the
	// context that can hurt someone when wrong.
	allergies := "udang"
	if _, err := c.nutrition.UpdatePreferences(c.ctx(), &edgev1.UpdatePreferencesRequest{Allergies: &allergies}); err != nil {
		t.Fatalf("saving the allergy note: %v", err)
	}

	in := dailyGuideInput()
	in.CravingType = nutritionv1.CravingType_CRAVING_TYPE_SOUPY_AND_WARM
	in.SocialContext = nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FAMILY
	asked, err := c.nutrition.GenerateDailyGuide(c.ctx(), &edgev1.GenerateDailyGuideRequest{Input: in})
	if err != nil {
		t.Fatalf("asking for a guide: %v", err)
	}
	if asked.GetGuideId() == "" {
		t.Fatalf("the answer names no guide: %v", asked)
	}
	if asked.GetStatus() != nutritionv1.GuideStatus_GUIDE_STATUS_PENDING {
		t.Errorf("a guide not generated yet has status %v", asked.GetStatus())
	}

	guide := c.watchGuide(asked.GetGuideId(), 90*time.Second)

	// The guide carries its content, not only its status.
	suggestions := guide.GetGuideData().GetStructValue().GetFields()["suggestions"].GetListValue().GetValues()
	if len(suggestions) == 0 {
		t.Fatalf("the guide arrived with no suggestions: %v", guide.GetGuideData())
	}
	if name := suggestions[0].GetStructValue().GetFields()["dish_name"].GetStringValue(); name == "" {
		t.Errorf("the first suggestion has no dish name: %v", suggestions[0])
	}
	if guide.GetMealTime() == nutritionv1.MealTime_MEAL_TIME_UNSPECIFIED {
		t.Error("the guide carries no meal time")
	}
	// Its date is the local date, not a timestamp.
	if len(guide.GetGuideDate()) != len(time.DateOnly) {
		t.Errorf("the guide date is %q, want a plain date", guide.GetGuideDate())
	}
}

// TestTheCulinaryHistoryIsPagedAndPrivate is F6-08 through the real path.
func TestTheCulinaryHistoryIsPagedAndPrivate(t *testing.T) {
	c := newClient(t)
	c.register()

	for range 3 {
		if _, err := c.nutrition.GenerateDailyGuide(c.ctx(), &edgev1.GenerateDailyGuideRequest{
			Input: &nutritionv1.DailyGuideInput{
				PlanType:          nutritionv1.PlanType_PLAN_TYPE_EAT_OUT,
				TimeAvailability:  nutritionv1.TimeAvailability_TIME_AVAILABILITY_RELAXED,
				EnergyLevel:       nutritionv1.EnergyLevel_ENERGY_LEVEL_ORDINARY,
				CuisinePreference: "Masakan Padang",
			},
		}); err != nil {
			t.Fatalf("asking for a guide: %v", err)
		}
	}

	first, err := c.nutrition.GetHubData(c.ctx(), &edgev1.GetHubDataRequest{Page: &edgev1.PageRequest{PageSize: 2}})
	if err != nil {
		t.Fatalf("the hub: %v", err)
	}
	if n := len(first.GetHistory()); n != 2 {
		t.Fatalf("the first page holds %d guides, want 2", n)
	}
	token := first.GetPage().GetNextPageToken()
	if token == "" {
		t.Fatalf("the first page carries no next token: %v", first)
	}

	second, err := c.nutrition.GetHubData(c.ctx(), &edgev1.GetHubDataRequest{
		Page: &edgev1.PageRequest{PageSize: 2, PageToken: token},
	})
	if err != nil {
		t.Fatalf("the second page: %v", err)
	}
	if n := len(second.GetHistory()); n != 1 {
		t.Fatalf("the second page holds %d guides, want 1", n)
	}
	if last := second.GetPage().GetNextPageToken(); last != "" {
		t.Errorf("the last page still carries a next token: %q", last)
	}

	stranger := newClient(t)
	stranger.register()
	theirs, err := stranger.nutrition.GetHubData(stranger.ctx(), &edgev1.GetHubDataRequest{})
	if err != nil {
		t.Fatalf("the hub for a stranger: %v", err)
	}
	if len(theirs.GetHistory()) != 0 || theirs.GetPreferences().GetAllergies() != "" {
		t.Errorf("a stranger sees someone else's culinary data: %v", theirs)
	}
}

// TestAnInvalidDailyGuideRequestIsRefused guards the daily input on the wire.
//
// The required answers are refused when missing, and - the case a typed client
// cannot even express - a legacy label or a mistyped enum name is refused
// instead of being read as "none". Every refusal is a client error, never 5xx.
func TestAnInvalidDailyGuideRequestIsRefused(t *testing.T) {
	c := newClient(t)
	c.register()

	const proc = "/edge.v1.Nutrition/GenerateDailyGuide"
	for name, body := range map[string]string{
		"no plan type":      `{"input":{"timeAvailability":"TIME_AVAILABILITY_QUICK","energyLevel":"ENERGY_LEVEL_TIRED","cuisinePreference":"Sunda"}}`,
		"no energy level":   `{"input":{"planType":"PLAN_TYPE_COOK_AT_HOME","timeAvailability":"TIME_AVAILABILITY_QUICK","cuisinePreference":"Sunda"}}`,
		"no cuisine":        `{"input":{"planType":"PLAN_TYPE_COOK_AT_HOME","timeAvailability":"TIME_AVAILABILITY_QUICK","energyLevel":"ENERGY_LEVEL_TIRED"}}`,
		"legacy plan label": `{"input":{"planType":"Masak di Rumah","timeAvailability":"TIME_AVAILABILITY_QUICK","energyLevel":"ENERGY_LEVEL_TIRED","cuisinePreference":"Sunda"}}`,
		"unknown craving":   `{"input":{"planType":"PLAN_TYPE_COOK_AT_HOME","timeAvailability":"TIME_AVAILABILITY_QUICK","energyLevel":"ENERGY_LEVEL_TIRED","cuisinePreference":"Sunda","cravingType":"Berkuah & Hangat"}}`,
		"undefined number":  `{"input":{"planType":99,"timeAvailability":"TIME_AVAILABILITY_QUICK","energyLevel":"ENERGY_LEVEL_TIRED","cuisinePreference":"Sunda"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			status, code := c.raw(proc, body)
			if status != http.StatusBadRequest || code != "invalid_argument" {
				t.Fatalf("got %d %q; want 400 invalid_argument", status, code)
			}
		})
	}

	// The legacy labels are refused in the preferences too, not stored as a
	// value no code recognises.
	if status, code := c.raw("/edge.v1.Nutrition/UpdatePreferences", `{"budgetLevel":"Hemat"}`); status != http.StatusBadRequest {
		t.Errorf("the legacy label \"Hemat\" answered %d %q; want 400", status, code)
	}
}

// watchGuide waits on the WatchDailyGuide stream until the guide is final.
func (c *client) watchGuide(guideID string, timeout time.Duration) *edgev1.DailyMealGuide {
	c.t.Helper()

	deadline := time.Now().Add(timeout)
	var last *edgev1.DailyMealGuide
	for time.Now().Before(deadline) {
		ctx, cancel := contextUntil(c.t, deadline)
		stream, err := c.nutrition.WatchDailyGuide(ctx, &edgev1.WatchDailyGuideRequest{GuideId: guideID})
		if err != nil {
			cancel()
			c.t.Fatalf("opening WatchDailyGuide: %v", err)
		}
		for stream.Receive() {
			last = stream.Msg().GetGuide()
			switch last.GetStatus() {
			case nutritionv1.GuideStatus_GUIDE_STATUS_READY:
				cancel()
				return last
			case nutritionv1.GuideStatus_GUIDE_STATUS_FAILED:
				cancel()
				c.t.Fatalf("the guide failed instead of arriving: %v", last)
			default:
			}
		}
		cancel()
	}
	c.t.Fatalf("the guide never arrived within %v; last state: %v", timeout, last)
	return nil
}
