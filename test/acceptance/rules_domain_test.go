// Package acceptance adalah daftar periksa aturan domain D1-D12 dan temuan
// keamanan S1-S11 (F9-19, kriteria selesai #11 dan #12).
//
// Satu test per aturan, dinamai menurut ID-nya, supaya "apakah D6 dijaga"
// dijawab dengan `go test ./test/acceptance -run TestD06`. Yang bisa diuji
// tanpa infrastruktur ada di berkas ini; yang menuntut stack yang menyala ada
// di rules_e2e_test.go dan melewati dirinya sendiri bila TEST_E2E_BASE_URL
// kosong - persis seperti suite e2e.
//
// Aturan yang sudah dijaga test lain di paketnya sendiri TETAP diulang di
// sini dalam bentuk paling langsung: daftar periksa yang merujuk ke test di
// tempat lain adalah daftar yang tidak bisa dibaca sendiri.
package acceptance

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
	chatdomain "github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/gemini"
	nutrition "github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// diabetesAnswers adalah kuesioner sah untuk model SCORE2-Diabetes, seluruh
// nilai klinis diketik manual.
func diabetesAnswers(diagnosedAt float64) map[string]any {
	return map[string]any{
		"has_diabetes":              true,
		"smoking_status":            "Perokok aktif",
		"q_exercise":                "Jarang",
		"sbp_input_type":            "manual",
		"sbp_value":                 150.0,
		"tchol_input_type":          "manual",
		"tchol_value":               6.2,
		"hdl_input_type":            "manual",
		"hdl_value":                 1.0,
		"age_at_diabetes_diagnosis": diagnosedAt,
		"hba1c_input_type":          "manual",
		"hba1c_value":               55.0,
		"scr_input_type":            "manual",
		"scr_value":                 80.0,
	}
}

// D6 - Batas usia diabetes bergantung profil: usia saat diagnosis tidak boleh
// melampaui usia pengguna sekarang. Sistem lama memvalidasinya di request
// (`max:` umur dari basis data); di sini aturannya milik mesin skor, karena
// mesin itulah yang memakai angkanya.
func TestD06_DiabetesDiagnosisAgeCannotExceedCurrentAge(t *testing.T) {
	engine := score.NewEngine(score.MustLoad())

	_, err := engine.Calculate(score.Request{
		Sex: "male", CountryOfResidence: "Indonesia", Age: 45,
		Answers: diabetesAnswers(50),
	})
	if !errors.Is(err, score.ErrDiabetesAgeAfterCurrentAge) {
		t.Fatalf("a diagnosis at 50 for a 45-year-old must be refused, got err=%v", err)
	}

	// Sama dengan usia sekarang masih sah: didiagnosis tahun ini.
	if _, err := engine.Calculate(score.Request{
		Sex: "male", CountryOfResidence: "Indonesia", Age: 45,
		Answers: diabetesAnswers(45),
	}); err != nil {
		t.Fatalf("a diagnosis at the current age must be accepted, got %v", err)
	}
}

// D7 - Masukan klinis punya dua mode. Setiap parameter menerima manual atau
// proksi; manual tanpa nilai ditolak, proksi ditebak dari jawaban gaya hidup.
func TestD07_ClinicalInputsAcceptManualOrProxy(t *testing.T) {
	engine := score.NewEngine(score.MustLoad())
	base := func() map[string]any {
		return map[string]any{
			"has_diabetes": false, "smoking_status": "Perokok aktif", "q_exercise": "Jarang",
			"tchol_input_type": "manual", "tchol_value": 6.2,
			"hdl_input_type": "manual", "hdl_value": 1.0,
		}
	}

	manual := base()
	manual["sbp_input_type"], manual["sbp_value"] = "manual", 150.0
	withManual, err := engine.Calculate(score.Request{Sex: "male", CountryOfResidence: "Indonesia", Age: 55, Answers: manual})
	if err != nil {
		t.Fatalf("manual: %v", err)
	}
	if withManual.ClinicalInputs.SBP != 150 {
		t.Fatalf("manual sbp was not used: %v", withManual.ClinicalInputs.SBP)
	}

	proxy := base()
	proxy["sbp_input_type"] = "proxy"
	withProxy, err := engine.Calculate(score.Request{Sex: "male", CountryOfResidence: "Indonesia", Age: 55, Answers: proxy})
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	if withProxy.ClinicalInputs.SBP <= 0 {
		t.Fatalf("proxy sbp must be estimated, got %v", withProxy.ClinicalInputs.SBP)
	}
	if withProxy.ClinicalInputs.SBP == withManual.ClinicalInputs.SBP {
		t.Fatalf("the proxy estimate coincidentally equals the manual value; the test cannot tell them apart")
	}
}

// D8 - Jendela konteks percakapan 20 pesan.
func TestD08_ConversationContextWindowIsTwentyMessages(t *testing.T) {
	if chatdomain.ContextWindow != 20 {
		t.Fatalf("ContextWindow = %d, want 20", chatdomain.ContextWindow)
	}
}

// D10 - Waktu makan ditentukan jam server di zona Asia/Jakarta: 05-10 sarapan,
// 10-15 makan siang, 15-18 camilan, sisanya makan malam.
func TestD10_MealTimeFollowsTheJakartaClock(t *testing.T) {
	wib := time.FixedZone("WIB", 7*60*60)
	cases := map[time.Time]nutrition.MealTime{
		time.Date(2026, 9, 7, 5, 0, 0, 0, wib):   nutrition.MealBreakfast,
		time.Date(2026, 9, 7, 9, 59, 0, 0, wib):  nutrition.MealBreakfast,
		time.Date(2026, 9, 7, 10, 0, 0, 0, wib):  nutrition.MealLunch,
		time.Date(2026, 9, 7, 13, 37, 0, 0, wib): nutrition.MealLunch,
		time.Date(2026, 9, 7, 15, 0, 0, 0, wib):  nutrition.MealAfternoonSnack,
		time.Date(2026, 9, 7, 18, 0, 0, 0, wib):  nutrition.MealDinner,
		time.Date(2026, 9, 7, 2, 0, 0, 0, wib):   nutrition.MealDinner,
	}
	for at, want := range cases {
		if got := nutrition.MealTimeAt(at); got != want {
			t.Errorf("%s -> %s, want %s", at.Format("15:04"), got, want)
		}
	}
}

// D12 - Judul thread dan percakapan dibuat otomatis dari 45 karakter pertama
// pesan pertama bila tidak diberikan.
func TestD12_TitleIsDerivedFromTheFirstFortyFiveRunes(t *testing.T) {
	long := strings.Repeat("abcdefghij", 6) // 60 rune
	got := chatdomain.DeriveTitle(long)
	// 45 rune pertama, lalu penanda pemotongan - bentuk Str::limit(45) di
	// sistem lama, yang dipertahankan supaya judul lama dan baru serupa.
	if !strings.HasPrefix(got, long[:45]) || got == long || len([]rune(got)) > 45+3 {
		t.Fatalf("derived title = %q (%d runes), want the first 45 plus a truncation mark", got, len([]rune(got)))
	}
	if got := chatdomain.DeriveTitle("Halo"); got != "Halo" {
		t.Fatalf("a short message is its own title, got %q", got)
	}
}

// S3/S4 - Verifikasi TLS ke Gemini TIDAK dimatikan. Sistem lama menulis
// verify=false; klien Go memakai transport bawaan, dan bukti bahwa ia
// memverifikasi adalah: sertifikat yang tidak dipercaya DITOLAK.
func TestS03_S04_TLSToTheProviderIsVerified(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := gemini.New(gemini.Config{
		APIKey: "test-key", Model: "test-model", Endpoint: server.URL,
		Timeout: 5 * time.Second, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), llm.Request{Prompt: "halo", PromptVersion: "t.v1"})
	if err == nil {
		t.Fatal("a self-signed certificate was accepted; TLS verification is off")
	}
	var certErr *tls.CertificateVerificationError
	if !errors.As(err, &certErr) && !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("the failure is not a certificate error: %v", err)
	}
}

// S7 - Kunci API tidak pernah ada di URL; ia dikirim sebagai header.
func TestS07_APIKeyTravelsInAHeaderNotTheURL(t *testing.T) {
	var seenURL, seenHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenURL, seenHeader = r.URL.String(), r.Header.Get("x-goog-api-key")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := gemini.New(gemini.Config{
		APIKey: "sekret-123", Model: "test-model", Endpoint: server.URL,
		Timeout: 5 * time.Second, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = client.Generate(context.Background(), llm.Request{Prompt: "halo", PromptVersion: "t.v1"})

	if strings.Contains(seenURL, "sekret-123") || strings.Contains(seenURL, "key=") {
		t.Fatalf("the API key leaked into the URL: %s", seenURL)
	}
	if seenHeader != "sekret-123" {
		t.Fatalf("x-goog-api-key = %q, want the configured key", seenHeader)
	}
}
