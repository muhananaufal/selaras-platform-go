package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

// TestCulinaryPreferencesSurviveAPartialUpdate adalah B16 lewat empat lapisan.
//
// Di sistem lama, satu PATCH yang hanya membawa alergi MENGHAPUS selera dan
// peralatan dapur pengguna: repositorinya menimpa seluruh kolom JSON dengan
// bidang yang kebetulan lolos validasi. Tidak ada galat, dan pengguna baru
// menyadarinya saat sarannya berubah.
//
// Rantai yang menahannya panjang - badan HTTP, kontrak proto, use case, SQL -
// dan memutusnya di SALAH SATU lapisan sudah cukup untuk menghidupkan bugnya
// kembali. Itulah sebabnya ini diuji dari luar, bukan hanya di domain.
func TestCulinaryPreferencesSurviveAPartialUpdate(t *testing.T) {
	c := newClient(t)
	c.register()

	// Belum pernah disentuh: hub tetap terbuka, dan isinya kosong - bukan 404.
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

	// Lalu HANYA alergi yang diubah.
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

	// Dan yang tersimpan sungguhan, bukan hanya yang dikembalikan.
	code, hub := c.do(http.MethodGet, "/api/v1/culinary/hub-data", nil)
	if code != http.StatusOK {
		t.Fatalf("reading the hub answered %d", code)
	}
	if got, _ := dig(hub, "data", "preferences", "budget_level").(string); got != "thrifty" {
		t.Errorf("the stored budget level is %q", got)
	}

	// Pengosongan yang DISENGAJA tetap bisa: kalau tidak, preferensi yang
	// pernah diisi tidak akan pernah bisa dihapus lagi.
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

// TestADailyGuideIsAskedForAndArrives adalah gerbang keluar F6.
//
// Permintaan dijawab 202 dan panduannya tiba belakangan - menyeberangi gateway,
// nutrition-svc, outbox, Kafka, llm-worker, dan kembali. Sistem lama menahan
// permintaan HTTP selama Gemini bekerja, dengan timeout 180 detik (B14).
func TestADailyGuideIsAskedForAndArrives(t *testing.T) {
	c := newClient(t)
	c.register()

	// Catatan alergi diisi lebih dulu: ia ikut ke dalam prompt, dan itu satu-
	// satunya bagian konteks yang keliru bisa mencederai seseorang.
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

	// Panduannya membawa isinya, bukan hanya statusnya.
	data, _ := guide["guide_data"].(map[string]any)
	suggestions, _ := data["suggestions"].([]any)
	if len(suggestions) == 0 {
		t.Fatalf("the guide arrived with no suggestions: %v", data)
	}
	first, _ := suggestions[0].(map[string]any)
	if name, _ := first["dish_name"].(string); name == "" {
		t.Errorf("the first suggestion has no dish name: %v", first)
	}

	// Waktu makannya dibekukan saat diminta, dan bukan kosong.
	if mealTime, _ := guide["meal_time"].(string); mealTime == "" {
		t.Error("the guide carries no meal time")
	}
	// Tanggalnya tanggal setempat, bukan stempel waktu.
	if date, _ := guide["guide_date"].(string); len(date) != 10 {
		t.Errorf("the guide date is %q, want a plain date", date)
	}
}

// TestTheCulinaryHistoryIsPagedAndPrivate adalah F6-08 lewat jalur sungguhan.
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

	// Halaman terakhir TIDAK membawa token: kosongnya adalah tanda berhenti.
	if last, _ := dig(second, "data", "page", "next_page_token").(string); last != "" {
		t.Errorf("the last page still carries a next token: %q", last)
	}

	// Dan orang lain tidak melihat satu pun.
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

// TestAnInvalidDailyGuideRequestIsRefused menjaga masukan hariannya.
//
// Tiga jawaban pertama WAJIB: tanpa salah satunya, prompt kehilangan bagian
// yang menjadikan saran hari ini berbeda dari saran mana pun.
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

	// Label lama sistem sebelumnya juga ditolak di preferensi, bukan diterima
	// diam-diam lalu tersimpan sebagai nilai yang tidak dikenali kode mana pun.
	code, answer := c.do(http.MethodPatch, "/api/v1/culinary/preferences", map[string]any{
		"budget_level": "Hemat",
	})
	if code == http.StatusOK {
		t.Errorf("the legacy label \"Hemat\" was accepted: %v", answer)
	}
}

// waitForGuide menunggu sebuah panduan berhenti pending, lalu mengembalikannya.
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
