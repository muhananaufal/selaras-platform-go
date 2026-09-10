package llmworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
)

// goldenContext is the document ACTUALLY produced by nutrition-svc.
//
// It is read from that package's testdata, not copied here as a second
// literal. Two copies in two packages would drift without anyone knowing, and
// what shows up later is only a prompt with blank fields. With one file,
// changing either side makes the other fail.
const goldenContext = "../nutrition/app/testdata/meal_guide_context.json"

func fullMealGuideContext(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.FromSlash(goldenContext))
	if err != nil {
		t.Fatalf("reading the context nutrition-svc produces: %v\n"+
			"Regenerate it with: UPDATE_GOLDEN=1 go test ./internal/nutrition/app/", err)
	}
	return string(raw)
}

// TestTheMealGuidePromptCarriesTheAllergyNote is the whole reason context_json
// is inside the event.
//
// The other LLM requests in this worker still use a "not yet in the event"
// marker for their context, and that is acceptable: an incomplete report can
// still be read as incomplete. A menu guide cannot. A prompt that leaves
// without the allergy note produces food advice that looks entirely valid, and
// the person reading it is the one allergic to it.
func TestTheMealGuidePromptCarriesTheAllergyNote(t *testing.T) {
	req, err := mealGuideRequest(&eventsv1.MealGuideRequested{
		GuideId:     "01930000-0000-7000-8000-000000000001",
		JobId:       "01930000-0000-7000-8000-000000000002",
		ContextJson: fullMealGuideContext(t),
	})
	if err != nil {
		t.Fatalf("mealGuideRequest: %v", err)
	}

	if req.Kind != KindMealGuide {
		t.Errorf("the job kind is %q", req.Kind)
	}
	if req.AggregateType != "meal_guide" {
		t.Errorf("the aggregate type is %q; the consumer filters on it", req.AggregateType)
	}

	// The prompt is really rendered, not just its data map inspected. The field
	// names in the template and the keys here are a contract that drifts
	// easily, and what drifts silently only shows up as a strange prompt.
	rendered := render(t, req)

	for _, want := range []string{
		"udang dan kepiting", // Catatan alerginya, utuh.
		"breakfast",
		"pedas, gurih",
		"wajan, rice cooker",
		"Masakan Sunda",
		"Sayur asem, Pepes ikan",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered prompt never mentions %q", want)
		}
	}
}

// TestAMealGuideRequestWithoutContextIsRefused closes the silent path.
func TestAMealGuideRequestWithoutContextIsRefused(t *testing.T) {
	for name, req := range map[string]*eventsv1.MealGuideRequested{
		"no guide":       {ContextJson: fullMealGuideContext(t)},
		"no context":     {GuideId: "01930000-0000-7000-8000-000000000001"},
		"broken context": {GuideId: "01930000-0000-7000-8000-000000000001", ContextJson: "{"},
		"no meal time": {
			GuideId:     "01930000-0000-7000-8000-000000000001",
			ContextJson: `{"language":"id"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mealGuideRequest(req); err == nil {
				t.Fatal("the request was accepted, and the guide would be built on nothing")
			}
		})
	}
}

// TestAnAbsentAllergyNoteIsStatedNotLeftBlank keeps the prompt readable.
//
// An empty field inside a prompt reads like a render mistake, and a model
// that meets one tends to make up its content.
func TestAnAbsentAllergyNoteIsStatedNotLeftBlank(t *testing.T) {
	req, err := mealGuideRequest(&eventsv1.MealGuideRequested{
		GuideId:     "01930000-0000-7000-8000-000000000001",
		ContextJson: `{"meal_time":"lunch","input":{"plan_type":"eat_out"}}`,
	})
	if err != nil {
		t.Fatalf("mealGuideRequest: %v", err)
	}

	rendered := render(t, req)
	if !strings.Contains(rendered, "tidak ada catatan alergi") {
		t.Error("an absent allergy note rendered as a blank field")
	}
	if !strings.Contains(rendered, "belum ada") {
		t.Error("an absent learning history rendered as a blank field")
	}

	// The language falls back to the default rather than becoming empty.
	if !strings.Contains(rendered, "BAHASA: "+defaultLanguage) &&
		!strings.Contains(rendered, "bahasa: "+defaultLanguage) {
		t.Errorf("the prompt names no language; it should fall back to %q", defaultLanguage)
	}
}

// TestTheMealGuideContextMatchesWhatIsStored binds the two copies.
//
// context_json in the event and generation_context in the database MUST have
// the same shape: one is used to produce the guide, the other to explain
// that guide later. Two drifting shapes would mean the explanation describes
// a different request from the one actually sent.
func TestTheMealGuideContextMatchesWhatIsStored(t *testing.T) {
	var parsed mealGuideContext
	if err := json.Unmarshal([]byte(fullMealGuideContext(t)), &parsed); err != nil {
		t.Fatalf("the context shape does not parse: %v", err)
	}

	// The fields most easily misnamed are checked one by one: a typo in a JSON
	// tag produces an empty value, not an error.
	if parsed.Preferences.Allergies == "" {
		t.Error("allergies did not survive parsing; check the json tag")
	}
	if parsed.Input.CuisinePreference == "" {
		t.Error("cuisine_preference did not survive parsing; check the json tag")
	}
	if parsed.MealTime == "" {
		t.Error("meal_time did not survive parsing; check the json tag")
	}
	if len(parsed.LearningHistory) != 2 {
		t.Errorf("learning_history parsed as %v", parsed.LearningHistory)
	}
}

// render runs the request template with its data.
func render(t *testing.T, req *Request) string {
	t.Helper()

	lib, err := prompt.Load()
	if err != nil {
		t.Fatalf("loading the prompt library: %v", err)
	}
	tmpl, err := lib.Latest(req.Template)
	if err != nil {
		t.Fatalf("the template %q named by the request does not exist: %v", req.Template, err)
	}

	// Rendering FAILS if the template mentions a field that is not in Data.
	// That is what makes this test exercise the contract, not merely the map's
	// content.
	out, err := tmpl.Render(req.Data)
	if err != nil {
		t.Fatalf("rendering %s with the worker's own data failed: %v", req.Template, err)
	}
	return out
}
