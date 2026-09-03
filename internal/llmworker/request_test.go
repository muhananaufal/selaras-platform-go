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

// goldenContext adalah dokumen yang BENAR-BENAR dihasilkan nutrition-svc.
//
// Ia dibaca dari testdata milik paket itu, bukan disalin ke sini sebagai
// literal kedua. Dua salinan di dua paket akan menyimpang tanpa ada yang tahu,
// dan yang terlihat kemudian hanyalah prompt dengan bidang kosong-kosong.
// Dengan satu berkas, mengubah salah satu sisi membuat sisi lain gagal.
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

// TestTheMealGuidePromptCarriesTheAllergyNote adalah alasan keseluruhan
// context_json ada di dalam eventnya.
//
// Permintaan LLM lain di worker ini masih memakai penanda "belum dibawa event"
// untuk konteksnya, dan itu bisa diterima: laporan yang kurang lengkap tetap
// bisa dibaca sebagai kurang lengkap. Panduan menu tidak begitu. Prompt yang
// berangkat tanpa catatan alergi menghasilkan saran makanan yang terlihat sah
// sepenuhnya, dan yang membacanya adalah orang yang alergi terhadapnya.
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

	// Prompt-nya dirender sungguhan, bukan hanya diperiksa peta datanya. Nama
	// bidang di templat dan kunci di sini adalah kontrak yang mudah menyimpang,
	// dan yang menyimpang diam-diam hanya terlihat sebagai prompt yang aneh.
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

// TestAMealGuideRequestWithoutContextIsRefused menutup jalur diamnya.
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

// TestAnAbsentAllergyNoteIsStatedNotLeftBlank menjaga prompt tetap terbaca.
//
// Bidang kosong di dalam prompt terbaca sebagai kekeliruan render, dan model
// yang menemuinya cenderung mengarang isinya sendiri.
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

	// Bahasa jatuh ke bawaan, bukan menjadi kosong.
	if !strings.Contains(rendered, "BAHASA: "+defaultLanguage) &&
		!strings.Contains(rendered, "bahasa: "+defaultLanguage) {
		t.Errorf("the prompt names no language; it should fall back to %q", defaultLanguage)
	}
}

// TestTheMealGuideContextMatchesWhatIsStored mengikat kedua salinannya.
//
// context_json di event dan generation_context di basis data HARUS berbentuk
// sama: yang satu dipakai membuat panduannya, yang lain dipakai menjelaskan
// panduan itu kemudian. Dua bentuk yang menyimpang berarti penjelasannya
// menggambarkan permintaan yang berbeda dari yang benar-benar dikirim.
func TestTheMealGuideContextMatchesWhatIsStored(t *testing.T) {
	var parsed mealGuideContext
	if err := json.Unmarshal([]byte(fullMealGuideContext(t)), &parsed); err != nil {
		t.Fatalf("the context shape does not parse: %v", err)
	}

	// Bidang yang paling mudah salah nama diperiksa satu per satu: kesalahan
	// ketik pada tag JSON menghasilkan nilai kosong, bukan galat.
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

// render menjalankan templat permintaan dengan datanya.
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

	// Render GAGAL bila templatnya menyebut bidang yang tidak ada di Data.
	// Itulah yang membuat test ini menguji kontrak, bukan sekadar isi peta.
	out, err := tmpl.Render(req.Data)
	if err != nil {
		t.Fatalf("rendering %s with the worker's own data failed: %v", req.Template, err)
	}
	return out
}
