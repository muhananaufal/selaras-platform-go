package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// goldenContext adalah SATU dokumen yang mengikat kedua sisi kontrak.
//
// Berkas ini dihasilkan di sini oleh nutrition-svc, dan DIBACA oleh test
// llm-worker sebagai masukan pembuat promptnya. Selama keduanya membaca berkas
// yang sama, nama bidang tidak bisa menyimpang diam-diam: mengubah satu sisi
// membuat sisi lain gagal.
//
// Dua literal terpisah di dua paket akan menyimpang tanpa ada yang tahu, dan
// yang terlihat kemudian hanyalah prompt yang isinya kosong-kosong.
const goldenContext = "testdata/meal_guide_context.json"

var wib = time.FixedZone("WIB", 7*60*60)

// TestTheAssembledContextMatchesTheGoldenDocument menulis dan memeriksa golden.
//
// Jalankan dengan -update untuk memperbaruinya setelah perubahan yang disengaja.
func TestTheAssembledContextMatchesTheGoldenDocument(t *testing.T) {
	now := time.Date(2026, 9, 3, 7, 30, 0, 0, wib)

	owner, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}

	prefs, err := domain.NewPreferences(owner, now)
	if err != nil {
		t.Fatalf("NewPreferences: %v", err)
	}
	if err := prefs.Apply(domain.PreferencesPatch{
		Allergies:        ptr("udang dan kepiting"),
		BudgetLevel:      ptr(domain.BudgetThrifty),
		CookingStyle:     ptr(domain.CookingQuickEveryTime),
		TasteProfiles:    ptr([]string{"pedas", "gurih"}),
		KitchenEquipment: ptr([]string{"wajan", "rice cooker"}),
	}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	chosen := []*domain.Guide{
		guideWithDishes(t, owner, now, "Sayur asem"),
		guideWithDishes(t, owner, now, "Pepes ikan"),
	}

	built := buildContext("id", domain.GuideInput{
		PlanType:          domain.PlanCookAtHome,
		TimeAvailability:  domain.TimeQuick,
		EnergyLevel:       domain.EnergyTired,
		CuisinePreference: "Masakan Sunda",
		CravingType:       domain.CravingSoupyAndWarm,
		SocialContext:     domain.SocialWithFamily,
	}, prefs, chosen, now)

	// Waktu makan diturunkan dari jamnya, bukan diisi test: 07.30 adalah
	// sarapan (D10), dan kalau ia berubah, itu perubahan aturan yang harus
	// terlihat di sini.
	if built.MealTime != string(domain.MealBreakfast) {
		t.Errorf("a guide asked for at 07:30 carries meal time %q", built.MealTime)
	}

	encoded, err := json.MarshalIndent(built, "", "  ")
	if err != nil {
		t.Fatalf("marshalling the context: %v", err)
	}
	encoded = append(encoded, '\n')

	path := filepath.FromSlash(goldenContext)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatalf("writing the golden document: %v", err)
		}
		t.Logf("golden document rewritten: %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the golden document: %v\n"+
			"Run with UPDATE_GOLDEN=1 to create it.", err)
	}
	if string(encoded) != string(want) {
		t.Errorf("the assembled context no longer matches the golden document.\n"+
			"llm-worker reads this same file; if the change is deliberate, run with\n"+
			"UPDATE_GOLDEN=1 and check that the worker's test still passes.\n\ngot:\n%s\nwant:\n%s",
			encoded, want)
	}
}

// TestTheLearningHistoryHoldsDishNamesOnly menjaga prompt tetap pendek.
func TestTheLearningHistoryHoldsDishNamesOnly(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	owner, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}

	// Hidangan yang sama muncul di dua panduan; ia hanya boleh disebut sekali.
	guides := []*domain.Guide{
		guideWithDishes(t, owner, now, "Sayur asem", "Pepes ikan"),
		guideWithDishes(t, owner, now, "Sayur asem"),
	}

	names := dishNamesOf(guides)
	if len(names) != 2 {
		t.Fatalf("the learning history holds %v, want two distinct dishes", names)
	}
	if names[0] != "Sayur asem" || names[1] != "Pepes ikan" {
		t.Errorf("the learning history holds %v", names)
	}
}

// TestAnUnreadableGuideIsSkippedNotFatal menjaga panduan hari ini tetap dibuat.
func TestAnUnreadableGuideIsSkippedNotFatal(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	owner, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}

	broken := guideWithDishes(t, owner, now, "Pepes ikan")
	broken.Data = json.RawMessage(`{"suggestions": "ini bukan daftar"}`)

	names := dishNamesOf([]*domain.Guide{
		broken,
		guideWithDishes(t, owner, now, "Sayur asem"),
	})
	if len(names) != 1 || names[0] != "Sayur asem" {
		t.Errorf("one unreadable old guide changed the history to %v", names)
	}
}

// TestTheContextNamesTheDefaultsItCannotYetFill menyatakan lubangnya terbuka.
//
// Fokus kesehatan dan misi harian belum bisa diambil nutrition-svc, dan
// keduanya memakai bawaan yang sama dengan sistem lama. Test ini ada supaya
// keadaan itu tidak berubah diam-diam menjadi tebakan yang terdengar meyakinkan
// - model yang diberi tahu sesuatu tentang seseorang akan memakainya sebagai
// fakta.
func TestTheContextNamesTheDefaultsItCannotYetFill(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	owner, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	prefs, err := domain.NewPreferences(owner, now)
	if err != nil {
		t.Fatalf("NewPreferences: %v", err)
	}

	built := buildContext("id", domain.GuideInput{}, prefs, nil, now)
	if built.HealthFocus != defaultHealthFocus {
		t.Errorf("the health focus is %q, want the stated default", built.HealthFocus)
	}
	if built.DailyMission != defaultDailyMission {
		t.Errorf("the daily mission is %q, want the stated default", built.DailyMission)
	}

	// Daftar kosong, bukan nil: nil menjadi `null` di JSON, dan pembaca di
	// worker akan menanganinya sebagai bentuk kedua tanpa alasan.
	if built.Preferences.TasteProfiles == nil || built.LearningHistory == nil {
		t.Error("an empty list was marshalled as null instead of []")
	}
}

func guideWithDishes(t *testing.T, owner domain.UserID, now time.Time, dishes ...string) *domain.Guide {
	t.Helper()

	g, err := domain.NewGuide(owner, domain.GuideInput{
		PlanType:          domain.PlanCookAtHome,
		TimeAvailability:  domain.TimeQuick,
		EnergyLevel:       domain.EnergyOrdinary,
		CuisinePreference: "apa saja",
	}, nil, now)
	if err != nil {
		t.Fatalf("NewGuide: %v", err)
	}

	suggestions := make([]map[string]string, 0, len(dishes))
	for _, dish := range dishes {
		suggestions = append(suggestions, map[string]string{"dish_name": dish})
	}
	encoded, err := json.Marshal(map[string]any{"suggestions": suggestions})
	if err != nil {
		t.Fatalf("marshalling suggestions: %v", err)
	}
	if err := g.MarkReady(encoded, now); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	return g
}

func ptr[T any](v T) *T { return &v }
