package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

// TestCulinaryPreferencesSurviveAPartialUpdate is B16 through four layers.
//
// In the legacy system, one PATCH carrying only allergies WIPED the user's
// tastes and kitchen equipment: its repository overwrote the whole JSON column
// with whichever fields happened to pass validation. No error, and the user
// only noticed when their suggestions changed.
//
// The chain holding it back is long - the HTTP body, the proto contract, the
// use case, SQL - and breaking it at ANY ONE layer is enough to bring the bug
// back to life. That is why it is tested from the outside, not only in the
// domain.
func TestCulinaryPreferencesSurviveAPartialUpdate(t *testing.T) {
	c := newClient(t)
	c.register()

	// Never touched: the hub still opens, and its content is empty - not a
	// 404.
	code, empty := c.do(http.MethodGet, "/api/v1/culinary/hub-data", nil)
	if code != http.StatusOK {
		t.Fatalf("a hub for a user with no preferences answered %d: %v", code, empty)
	}
	if got, _ := dig(empty, "data", "preferences", "budget_level").(string); got != "" {
		t.Errorf("an untouched budget level came back as %q", got)
	}

	// Seluruh preferensi diisi.
	code, full := c.do(http.MethodPatch, "/api/v1/culinary/preferences", map[string]any{
		"allergies":         "udang dan kepiting",
		"budget_level":      "thrifty",
		"cooking_style":     "quick_every_time",
		"taste_profiles":    []string{"pedas", "gurih"},
		"kitchen_equipment": []string{"wajan", "rice cooker"},
	})
	if code != http.StatusOK {
		t.Fatalf("saving preferences answered %d: %v", code, full)
	}

	// Then ONLY the allergies are changed.
	code, partial := c.do(http.MethodPatch, "/api/v1/culinary/preferences", map[string]any{
		"allergies": "udang, kepiting, dan kacang",
	})
	if code != http.StatusOK {
		t.Fatalf("a partial update answered %d: %v", code, partial)
	}

	if got, _ := dig(partial, "data", "allergies").(string); got != "udang, kepiting, dan kacang" {
		t.Errorf("the allergy note is %q", got)
	}
	if got, _ := dig(partial, "data", "budget_level").(string); got != "thrifty" {
		t.Errorf("the budget level was wiped to %q by a patch that never mentioned it", got)
	}
	if got, _ := dig(partial, "data", "cooking_style").(string); got != "quick_every_time" {
		t.Errorf("the cooking style was wiped to %q", got)
	}
	if got, _ := dig(partial, "data", "taste_profiles").([]any); len(got) != 2 {
		t.Errorf("the taste profiles were wiped to %v", got)
	}
	if got, _ := dig(partial, "data", "kitchen_equipment").([]any); len(got) != 2 {
		t.Errorf("the kitchen equipment was wiped to %v", got)
	}

	// And what is really stored, not only what was returned.
	code, hub := c.do(http.MethodGet, "/api/v1/culinary/hub-data", nil)
	if code != http.StatusOK {
		t.Fatalf("reading the hub answered %d", code)
	}
	if got, _ := dig(hub, "data", "preferences", "budget_level").(string); got != "thrifty" {
		t.Errorf("the stored budget level is %q", got)
	}

	// DELIBERATE emptying still works: otherwise a preference once filled in
	// could never be cleared again.
	code, cleared := c.do(http.MethodPatch, "/api/v1/culinary/preferences", map[string]any{
		"taste_profiles": []string{},
	})
	if code != http.StatusOK {
		t.Fatalf("emptying the taste profiles answered %d: %v", code, cleared)
	}
	if got, _ := dig(cleared, "data", "taste_profiles").([]any); len(got) != 0 {
		t.Errorf("an explicitly emptied list still holds %v", got)
	}
	if got, _ := dig(cleared, "data", "kitchen_equipment").([]any); len(got) != 2 {
		t.Errorf("emptying one list also emptied the other: %v", got)
	}
}

// TestADailyGuideIsAskedForAndArrives is the F6 exit gate.
//
// The request is answered 202 and the guide arrives later - crossing the
// gateway, nutrition-svc, the outbox, Kafka, llm-worker, and back. The legacy
// system held the HTTP request while Gemini worked, with a 180-second timeout
// (B14).
func TestADailyGuideIsAskedForAndArrives(t *testing.T) {
	c := newClient(t)
	c.register()

	// The allergy note is filled in first: it goes into the prompt, and it is
	// the only part of the context that can hurt someone when wrong.
	if code, body := c.do(http.MethodPatch, "/api/v1/culinary/preferences", map[string]any{
		"allergies": "udang",
	}); code != http.StatusOK {
		t.Fatalf("saving the allergy note answered %d: %v", code, body)
	}

	code, asked := c.do(http.MethodPost, "/api/v1/culinary/daily-guides", map[string]any{
		"plan_type":          "cook_at_home",
		"time_availability":  "quick",
		"energy_level":       "tired",
		"cuisine_preference": "Masakan Sunda",
		"craving_type":       "soupy_and_warm",
		"social_context":     "with_family",
	})
	if code != http.StatusAccepted {
		t.Fatalf("asking for a guide answered %d, want 202: %v", code, asked)
	}

	guideID, _ := dig(asked, "data", "guide_id").(string)
	if guideID == "" {
		t.Fatalf("the answer names no guide: %v", asked)
	}
	if got, _ := dig(asked, "data", "status").(string); got != "pending" {
		t.Errorf("a guide that has not been generated yet has status %q", got)
	}

	guide := c.waitForGuide(guideID, 90*time.Second)

	// The guide carries its content, not only its status.
	data, _ := guide["guide_data"].(map[string]any)
	suggestions, _ := data["suggestions"].([]any)
	if len(suggestions) == 0 {
		t.Fatalf("the guide arrived with no suggestions: %v", data)
	}
	first, _ := suggestions[0].(map[string]any)
	if name, _ := first["dish_name"].(string); name == "" {
		t.Errorf("the first suggestion has no dish name: %v", first)
	}

	// Its meal time was frozen when requested, and is not empty.
	if mealTime, _ := guide["meal_time"].(string); mealTime == "" {
		t.Error("the guide carries no meal time")
	}
	// Its date is the local date, not a timestamp.
	if date, _ := guide["guide_date"].(string); len(date) != 10 {
		t.Errorf("the guide date is %q, want a plain date", date)
	}
}

// TestTheCulinaryHistoryIsPagedAndPrivate is F6-08 through the real path.
func TestTheCulinaryHistoryIsPagedAndPrivate(t *testing.T) {
	c := newClient(t)
	c.register()

	for range 3 {
		code, body := c.do(http.MethodPost, "/api/v1/culinary/daily-guides", map[string]any{
			"plan_type":          "eat_out",
			"time_availability":  "relaxed",
			"energy_level":       "ordinary",
			"cuisine_preference": "Masakan Padang",
		})
		if code != http.StatusAccepted {
			t.Fatalf("asking for a guide answered %d: %v", code, body)
		}
	}

	code, first := c.do(http.MethodGet, "/api/v1/culinary/hub-data?page_size=2", nil)
	if code != http.StatusOK {
		t.Fatalf("the hub answered %d: %v", code, first)
	}
	if items, _ := dig(first, "data", "history").([]any); len(items) != 2 {
		t.Fatalf("the first page holds %d guides, want 2", len(items))
	}

	token, _ := dig(first, "data", "page", "next_page_token").(string)
	if token == "" {
		t.Fatalf("the first page carries no next token: %v", first)
	}

	code, second := c.do(http.MethodGet,
		"/api/v1/culinary/hub-data?page_size=2&page_token="+token, nil)
	if code != http.StatusOK {
		t.Fatalf("the second page answered %d", code)
	}
	if rest, _ := dig(second, "data", "history").([]any); len(rest) != 1 {
		t.Fatalf("the second page holds %d guides, want 1", len(rest))
	}

	// The last page carries NO token: its emptiness is the stop signal.
	if last, _ := dig(second, "data", "page", "next_page_token").(string); last != "" {
		t.Errorf("the last page still carries a next token: %q", last)
	}

	// And someone else sees none at all.
	stranger := newClient(t)
	stranger.register()

	code, theirs := stranger.do(http.MethodGet, "/api/v1/culinary/hub-data", nil)
	if code != http.StatusOK {
		t.Fatalf("the hub answered %d for a stranger", code)
	}
	if items, _ := dig(theirs, "data", "history").([]any); len(items) != 0 {
		t.Errorf("a stranger sees %d of someone else's guides", len(items))
	}
	if got, _ := dig(theirs, "data", "preferences", "allergies").(string); got != "" {
		t.Errorf("a stranger sees someone else's allergy note: %q", got)
	}
}

// TestAnInvalidDailyGuideRequestIsRefused guards the daily input.
//
// The first three answers are REQUIRED: without any one of them, the prompt
// loses the part that makes today's advice different from any other advice.
func TestAnInvalidDailyGuideRequestIsRefused(t *testing.T) {
	c := newClient(t)
	c.register()

	valid := map[string]any{
		"plan_type":          "cook_at_home",
		"time_availability":  "quick",
		"energy_level":       "tired",
		"cuisine_preference": "Masakan Sunda",
	}

	for name, mutate := range map[string]func(map[string]any){
		"no plan type":      func(b map[string]any) { delete(b, "plan_type") },
		"no energy level":   func(b map[string]any) { delete(b, "energy_level") },
		"no cuisine":        func(b map[string]any) { delete(b, "cuisine_preference") },
		"legacy plan label": func(b map[string]any) { b["plan_type"] = "Masak di Rumah" },
		"unknown craving":   func(b map[string]any) { b["craving_type"] = "Berkuah & Hangat" },
	} {
		t.Run(name, func(t *testing.T) {
			body := make(map[string]any, len(valid))
			for k, v := range valid {
				body[k] = v
			}
			mutate(body)

			code, answer := c.do(http.MethodPost, "/api/v1/culinary/daily-guides", body)
			if code == http.StatusAccepted {
				t.Fatalf("an invalid request was accepted: %v", answer)
			}
			if code >= 500 {
				t.Fatalf("an invalid request answered %d; bad input is not a server fault", code)
			}
		})
	}

	// The old labels of the previous system are refused in the preferences as
	// well, not silently accepted and stored as a value no code recognises.
	code, answer := c.do(http.MethodPatch, "/api/v1/culinary/preferences", map[string]any{
		"budget_level": "Hemat",
	})
	if code == http.StatusOK {
		t.Errorf("the legacy label \"Hemat\" was accepted: %v", answer)
	}
}

// waitForGuide waits for a guide to stop being pending, then returns it.
func (c *client) waitForGuide(guideID string, timeout time.Duration) map[string]any {
	c.t.Helper()

	deadline := time.Now().Add(timeout)
	var last string

	for time.Now().Before(deadline) {
		code, hub := c.do(http.MethodGet, "/api/v1/culinary/hub-data", nil)
		if code != http.StatusOK {
			c.t.Fatalf("reading the hub answered %d: %v", code, hub)
		}

		history, _ := dig(hub, "data", "history").([]any)
		for _, raw := range history {
			guide, _ := raw.(map[string]any)
			if id, _ := guide["id"].(string); id != guideID {
				continue
			}

			status, _ := guide["status"].(string)
			last = status
			switch status {
			case "ready":
				return guide
			case "failed":
				c.t.Fatalf("the guide failed instead of arriving: %v", guide)
			}
		}
		time.Sleep(2 * time.Second)
	}

	c.t.Fatalf("the guide never arrived within %v; its last status was %q", timeout, last)
	return nil
}
