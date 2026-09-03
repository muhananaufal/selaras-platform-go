package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

// dashboardLagBudget adalah batas yang DINYATAKAN, bukan yang ditaksir.
//
// Lima pengukuran di docs/consistency-report.md menghasilkan 444-920 ms, dan
// satu pengukuran setelah service baru dinyalakan menghasilkan 1204 ms. Batas
// di sini sepuluh detik: cukup longgar untuk mesin yang sibuk menjalankan
// tujuh container sekaligus, dan cukup ketat untuk menangkap proyeksi yang
// benar-benar berhenti bergerak.
//
// Batas yang terlalu ketat menghasilkan test yang gagal karena mesinnya sibuk,
// dan test seperti itu berhenti dipercaya sebelum ia sempat menangkap apa pun.
const dashboardLagBudget = 10 * time.Second

// TestANewUserSeesAWelcomeDashboardNotAnError adalah keadaan pertama yang
// dilihat setiap pengguna.
//
// Pengguna yang baru mendaftar belum menghasilkan satu event pun, jadi belum
// ada barisnya di read-model. Itu BUKAN 404: halaman yang menyambut pengguna
// baru tidak boleh terlihat rusak.
func TestANewUserSeesAWelcomeDashboardNotAnError(t *testing.T) {
	c := newClient(t)
	c.register()

	code, body := c.do(http.MethodGet, "/api/v1/dashboard", nil)
	if code != http.StatusOK {
		t.Fatalf("a brand new user's dashboard answered %d: %v", code, body)
	}

	if has, _ := dig(body, "data", "has_assessments").(bool); has {
		t.Error("a user who has never analysed anything is reported as having assessments")
	}
	if total, _ := dig(body, "data", "total_assessments").(float64); total != 0 {
		t.Errorf("the total is %v, want 0", total)
	}
	if dig(body, "data", "latest_assessment") != nil {
		t.Error("a user with no assessments has a latest assessment")
	}
	if dig(body, "data", "program") != nil {
		t.Error("a user with no program has a program")
	}

	// Daftar KOSONG, bukan null: klien yang mengiterasinya akan gagal.
	if history, ok := dig(body, "data", "assessment_history").([]any); !ok || len(history) != 0 {
		t.Errorf("the history is %v; it should be an empty list", dig(body, "data", "assessment_history"))
	}
	if trend, ok := dig(body, "data", "risk_trend").([]any); !ok || len(trend) != 0 {
		t.Errorf("the risk trend is %v; it should be an empty list", dig(body, "data", "risk_trend"))
	}

	// "insufficient_data", BUKAN "stable". Sistem lama menjawab stable untuk
	// analisis pertama, mencampur dua keadaan yang berbeda: klien yang
	// menggambar panah mendatar untuk stabil akan menggambarnya juga untuk
	// orang yang belum punya pembanding sama sekali.
	if got, _ := dig(body, "data", "health_trend").(string); got != "insufficient_data" {
		t.Errorf("a user with no assessments has health trend %q", got)
	}
}

// TestTheDashboardCatchesUpAfterAnAssessment adalah gerbang keluar F7.
//
// Ia mengukur SELURUH rantai lewat HTTP: penulisan penilaian, baris outbox,
// relay, Kafka, proyektor, dan pembacaan dasbor lewat gateway.
func TestTheDashboardCatchesUpAfterAnAssessment(t *testing.T) {
	c := newClient(t)
	c.register()
	c.completeProfile()

	code, first := c.do(http.MethodPost, "/api/v1/risk-assessments", assessmentInput())
	if code != http.StatusCreated {
		t.Fatalf("starting an assessment answered %d: %v", code, first)
	}

	slug, _ := dig(first, "data", "slug").(string)
	risk, _ := dig(first, "data", "risk_percentage").(float64)
	if slug == "" || risk == 0 {
		t.Fatalf("the assessment came back as %v", first)
	}

	dash := c.waitForDashboard(1, dashboardLagBudget)

	// Penilaiannya muncul utuh, bukan hanya jumlahnya.
	latest, _ := dash["latest_assessment"].(map[string]any)
	if latest == nil {
		t.Fatalf("the dashboard has no latest assessment: %v", dash)
	}
	if got, _ := latest["slug"].(string); got != slug {
		t.Errorf("the dashboard shows assessment %q, want %q", got, slug)
	}
	if got, _ := latest["risk_percentage"].(float64); got != risk {
		t.Errorf("the dashboard shows %v%%, the assessment said %v%%", got, risk)
	}

	// Kategori risiko ada, dan itu yang membedakannya dari sistem lama.
	//
	// Di sana kategorinya dibaca dari laporan LLM, sehingga pengguna yang
	// personalisasinya belum tiba - seperti pengguna ini, yang baru saja
	// menganalisis - melihat "N/A" sebagai status kesehatannya (B19). Di sini
	// ia dihitung dari SCORE2 dan ada begitu penilaiannya ada.
	category, _ := latest["risk_category"].(string)
	switch category {
	case "LOW_MODERATE", "HIGH", "VERY_HIGH":
	default:
		t.Errorf("the risk category is %q; it should be computed, not awaited", category)
	}

	// Satu penilaian: belum ada pembanding.
	if got, _ := dash["health_trend"].(string); got != "insufficient_data" {
		t.Errorf("with one assessment the trend is %q", got)
	}

	// Grafik risiko memuat titiknya.
	if trend, _ := dash["risk_trend"].([]any); len(trend) != 1 {
		t.Errorf("the risk trend holds %d points, want 1", len(trend))
	}

	// projected_at dibuka apa adanya: read-model bersifat eventually
	// consistent, dan jeda yang disembunyikan tampak seperti bug.
	if got, _ := dash["projected_at"].(string); got == "" {
		t.Error("the dashboard does not say when it was last projected")
	}
}

// TestASecondAssessmentGivesTheDashboardATrend adalah alasan riwayat disimpan.
func TestASecondAssessmentGivesTheDashboardATrend(t *testing.T) {
	c := newClient(t)
	c.register()
	c.completeProfile()

	for range 2 {
		if code, body := c.do(http.MethodPost, "/api/v1/risk-assessments", assessmentInput()); code != http.StatusCreated {
			t.Fatalf("starting an assessment answered %d: %v", code, body)
		}
	}

	dash := c.waitForDashboard(2, dashboardLagBudget)

	// Dua penilaian dengan jawaban yang sama menghasilkan angka yang sama, jadi
	// trennya "stable" - dan itu berbeda dari "insufficient_data" yang dijawab
	// saat baru ada satu.
	if got, _ := dash["health_trend"].(string); got != "stable" {
		t.Errorf("two identical assessments give trend %q, want stable", got)
	}
	if history, _ := dash["assessment_history"].([]any); len(history) != 2 {
		t.Errorf("the history holds %d assessments, want 2", len(history))
	}
	if trend, _ := dash["risk_trend"].([]any); len(trend) != 2 {
		t.Errorf("the risk trend holds %d points, want 2", len(trend))
	}
}

// TestOneUsersAssessmentsNeverReachAnothersDashboard adalah S9 di read-model.
//
// Proyeksi menulis ke satu baris per pengguna, dan barisnya dipilih dari
// user_id di dalam eventnya. Kekeliruan di sana tidak menghasilkan galat -
// ia menghasilkan riwayat kesehatan seseorang di dasbor orang lain.
func TestOneUsersAssessmentsNeverReachAnothersDashboard(t *testing.T) {
	owner := newClient(t)
	owner.register()
	owner.completeProfile()

	if code, body := owner.do(http.MethodPost, "/api/v1/risk-assessments", assessmentInput()); code != http.StatusCreated {
		t.Fatalf("starting an assessment answered %d: %v", code, body)
	}
	owner.waitForDashboard(1, dashboardLagBudget)

	stranger := newClient(t)
	stranger.register()

	code, theirs := stranger.do(http.MethodGet, "/api/v1/dashboard", nil)
	if code != http.StatusOK {
		t.Fatalf("a stranger's dashboard answered %d", code)
	}
	if total, _ := dig(theirs, "data", "total_assessments").(float64); total != 0 {
		t.Errorf("a stranger sees %v of someone else's assessments", total)
	}
	if history, _ := dig(theirs, "data", "assessment_history").([]any); len(history) != 0 {
		t.Errorf("a stranger sees %d of someone else's assessments in the history", len(history))
	}
}

// waitForDashboard menunggu proyeksi menyusul sampai jumlah yang diharapkan.
func (c *client) waitForDashboard(want int, timeout time.Duration) map[string]any {
	c.t.Helper()

	started := time.Now()
	deadline := started.Add(timeout)
	var last float64

	for time.Now().Before(deadline) {
		code, body := c.do(http.MethodGet, "/api/v1/dashboard", nil)
		if code != http.StatusOK {
			c.t.Fatalf("reading the dashboard answered %d: %v", code, body)
		}

		data, _ := dig(body, "data").(map[string]any)
		last, _ = data["total_assessments"].(float64)

		if int(last) >= want {
			c.t.Logf("the dashboard caught up in %v", time.Since(started).Round(time.Millisecond))
			return data
		}
		time.Sleep(100 * time.Millisecond)
	}

	c.t.Fatalf("the dashboard never reached %d assessments within %v; it stopped at %v.\n"+
		"See docs/consistency-report.md for the measured lag and what it depends on.",
		want, timeout, last)
	return nil
}

// completeProfile mengisi profil secukupnya supaya penilaian bisa dihitung.
func (c *client) completeProfile() {
	c.t.Helper()

	code, body := c.do(http.MethodPatch, "/api/v1/profile", map[string]any{
		"first_name":           "Uji",
		"last_name":            "Dasbor",
		"date_of_birth":        "1970-05-10",
		"sex":                  "male",
		"country_of_residence": "Indonesia",
	})
	if code != http.StatusOK {
		c.t.Fatalf("completing the profile answered %d: %v", code, body)
	}
}

// assessmentInput adalah kuesioner yang sah, seluruhnya diisi manual.
//
// Nilai jawabannya string berbahasa Indonesia karena itulah yang dikirim
// antarmuka hari ini, dan bentuknya dipertahankan (ADR-005).
func assessmentInput() map[string]any {
	return map[string]any{
		"has_diabetes":     false,
		"smoking_status":   "Perokok aktif",
		"q_exercise":       "Jarang",
		"sbp_input_type":   "manual",
		"sbp_value":        150,
		"tchol_input_type": "manual",
		"tchol_value":      6.2,
		"hdl_input_type":   "manual",
		"hdl_value":        1.0,
	}
}
