package acceptance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Aturan yang hanya bisa dibuktikan lewat kontrak publik, terhadap stack
// yang menyala (compose atau k3d). Tanpa TEST_E2E_BASE_URL test ini melewati
// dirinya sendiri; di CI ia WAJIB berjalan.

const password = "correct-horse-battery"

type api struct {
	t     *testing.T
	base  string
	token string
	email string
}

func stack(t *testing.T) *api {
	t.Helper()
	base := os.Getenv("TEST_E2E_BASE_URL")
	if base == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_E2E_BASE_URL is not set; acceptance tests must not be skipped in CI")
		}
		t.Skip("TEST_E2E_BASE_URL is not set; start the stack to run this rule")
	}
	return &api{t: t, base: base}
}

func (a *api) call(method, path string, body any, bearer string) (int, map[string]any, http.Header) {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+"/api/v1"+path, reader)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	var decoded map[string]any
	raw, _ := io.ReadAll(res.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			decoded = map[string]any{"_raw": string(raw)}
		}
	}
	return res.StatusCode, decoded, res.Header
}

func (a *api) do(method, path string, body any) (int, map[string]any) {
	code, decoded, _ := a.call(method, path, body, a.token)
	return code, decoded
}

func (a *api) register() *api {
	a.t.Helper()
	a.email = fmt.Sprintf("acc-%d-%s@user.co", time.Now().UnixNano(), strings.ToLower(strings.ReplaceAll(a.t.Name(), "/", "-")))
	code, body := a.do(http.MethodPost, "/register", map[string]any{
		"name": "Acceptance", "email": a.email, "password": password, "password_confirmation": password,
	})
	if code != http.StatusCreated && code != http.StatusOK {
		a.t.Fatalf("register answered %d: %v", code, body)
	}
	a.token, _ = body["access_token"].(string)
	if a.token == "" {
		a.t.Fatalf("register returned no access token: %v", body)
	}
	return a
}

func (a *api) login() string {
	a.t.Helper()
	code, body, _ := a.call(http.MethodPost, "/login", map[string]any{"email": a.email, "password": password}, "")
	if code != http.StatusOK {
		a.t.Fatalf("login answered %d: %v", code, body)
	}
	token, _ := body["access_token"].(string)
	return token
}

func (a *api) completeProfile() {
	a.t.Helper()
	if code, body := a.do(http.MethodPatch, "/profile", map[string]any{
		"first_name": "Uji", "last_name": "Aturan", "date_of_birth": "1970-05-10",
		"sex": "male", "country_of_residence": "Indonesia",
	}); code != http.StatusOK {
		a.t.Fatalf("profile answered %d: %v", code, body)
	}
}

func assessmentInput() map[string]any {
	return map[string]any{
		"has_diabetes": false, "smoking_status": "Perokok aktif", "q_exercise": "Jarang",
		"sbp_input_type": "manual", "sbp_value": 150, "tchol_input_type": "manual", "tchol_value": 6.2,
		"hdl_input_type": "manual", "hdl_value": 1.0,
	}
}

func (a *api) startAssessment() string {
	a.t.Helper()
	code, body := a.do(http.MethodPost, "/risk-assessments", assessmentInput())
	if code != http.StatusCreated {
		a.t.Fatalf("assessment answered %d: %v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	slug, _ := data["slug"].(string)
	if slug == "" {
		a.t.Fatalf("assessment returned no slug: %v", body)
	}
	return slug
}

func (a *api) startProgram() (int, map[string]any) {
	a.t.Helper()
	return a.do(http.MethodPost, "/coaching/programs", map[string]any{"difficulty": "Standar & Konsisten"})
}

// startProgramFrom memulai program yang bersumber dari satu hasil analisis.
//
// Coaching mengenal analisis lewat event assessment.completed (F4-06), jadi
// ada jeda antara 201 dari /risk-assessments dan saat slug-nya bisa dipakai:
// 404 selama jeda itu dicoba lagi selama beberapa detik, bukan diasumsikan
// tidak ada.
func (a *api) startProgramFrom(assessmentSlug string) (int, map[string]any) {
	a.t.Helper()
	body := map[string]any{"difficulty": "Standar & Konsisten", "risk_assessment_slug": assessmentSlug}
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, resp := a.do(http.MethodPost, "/coaching/programs", body)
		if code != http.StatusNotFound || time.Now().After(deadline) {
			return code, resp
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func statusOf(t *testing.T, body map[string]any) string {
	t.Helper()
	data, _ := body["data"].(map[string]any)
	status, _ := data["status"].(string)
	if status == "" {
		t.Fatalf("no status in %v", body)
	}
	return status
}

func slugOf(t *testing.T, body map[string]any) string {
	t.Helper()
	data, _ := body["data"].(map[string]any)
	slug, _ := data["slug"].(string)
	if slug == "" {
		t.Fatalf("no slug in %v", body)
	}
	return slug
}

// D1 - Satu sesi per pengguna: login yang berhasil mencabut token sebelumnya.
func TestD01_ANewLoginRevokesTheOlderSession(t *testing.T) {
	a := stack(t).register()
	old := a.token

	if code, _, _ := a.call(http.MethodGet, "/me", nil, old); code != http.StatusOK {
		t.Fatalf("the first session is not usable: %d", code)
	}
	fresh := a.login()

	// Pencabutan disebarkan lewat Redis; ia hampir seketika, tetapi bukan
	// nol - diberi beberapa detik, bukan diasumsikan.
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, _, _ := a.call(http.MethodGet, "/me", nil, old)
		if code == http.StatusUnauthorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the older token still works after a new login: %d", code)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if code, _, _ := a.call(http.MethodGet, "/me", nil, fresh); code != http.StatusOK {
		t.Fatalf("the new session must work: %d", code)
	}
}

// D2 - Satu program aktif per pengguna. Memulai yang kedua TIDAK ditolak:
// seperti sistem lama (`initiateProgram`), program aktif sebelumnya dijeda dan
// yang baru menjadi satu-satunya yang aktif. Yang dijaga adalah "satu aktif",
// bukan "tidak boleh memulai lagi" - itu perbedaan yang sempat saya salah
// tulis sebagai 409, dan test inilah yang menangkapnya.
// D3 - Satu program per hasil analisis: penilaian yang sudah dipakai satu
// program ditolak 409, sekalipun program itu sudah dijeda.
func TestD02_D03_OneActiveProgramPerUserAndPerAssessment(t *testing.T) {
	a := stack(t).register()
	a.completeProfile()
	assessment := a.startAssessment()

	code, body := a.startProgramFrom(assessment)
	if code != http.StatusAccepted {
		t.Fatalf("the first program answered %d: %v", code, body)
	}
	first := slugOf(t, body)

	code, body = a.startProgram()
	if code != http.StatusAccepted {
		t.Fatalf("D2: a second program answered %d, want 202 with the first one paused: %v", code, body)
	}
	if got := statusOf(t, body); got != "active" {
		t.Fatalf("D2: the new program must be the active one, got %q", got)
	}
	code, body = a.do(http.MethodGet, "/coaching/programs/"+first, nil)
	if code != http.StatusOK {
		t.Fatalf("reading the first program answered %d: %v", code, body)
	}
	if got := statusOf(t, body); got != "paused" {
		t.Fatalf("D2: the previous program must be paused, got %q", got)
	}

	if code, body := a.startProgramFrom(assessment); code != http.StatusConflict {
		t.Fatalf("D3: reusing an assessment answered %d, want 409: %v", code, body)
	}
}

// D4 - Program hanya bisa dijeda dari active dan dilanjutkan dari paused.
// Menjeda dua kali berturut-turut berarti melanjutkan; yang ditolak adalah
// status lain (selesai/dibatalkan), diuji lewat program yang dihapus.
func TestD04_ToggleOnlyMovesBetweenActiveAndPaused(t *testing.T) {
	a := stack(t).register()
	a.completeProfile()
	a.startAssessment()
	code, body := a.startProgram()
	if code != http.StatusAccepted {
		t.Fatalf("program: %d %v", code, body)
	}
	slug := slugOf(t, body)
	toggle := "/coaching/programs/" + slug + "/toggle-program-status"

	if code, body := a.do(http.MethodPatch, toggle, nil); code != http.StatusOK {
		t.Fatalf("pausing an active program answered %d: %v", code, body)
	}
	if code, body := a.do(http.MethodPatch, toggle, nil); code != http.StatusOK {
		t.Fatalf("resuming a paused program answered %d: %v", code, body)
	}
	if code, body := a.do(http.MethodDelete, "/coaching/programs/"+slug, nil); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("ending the program answered %d: %v", code, body)
	}
	if code, _ := a.do(http.MethodPatch, toggle, nil); code != http.StatusConflict && code != http.StatusNotFound {
		t.Fatalf("toggling a program that is no longer active answered %d, want 409 or 404", code)
	}
}

// D5 - Program non-aktif membekukan interaksi: thread baru pada program yang
// dijeda ditolak 409.
func TestD05_APausedProgramFreezesInteraction(t *testing.T) {
	a := stack(t).register()
	a.completeProfile()
	a.startAssessment()
	code, body := a.startProgram()
	if code != http.StatusAccepted {
		t.Fatalf("program: %d %v", code, body)
	}
	slug := slugOf(t, body)
	if code, _ := a.do(http.MethodPatch, "/coaching/programs/"+slug+"/toggle-program-status", nil); code != http.StatusOK {
		t.Fatalf("pause: %d", code)
	}
	if code, body := a.do(http.MethodPost, "/coaching/programs/"+slug+"/threads", map[string]any{"message": "halo"}); code != http.StatusConflict {
		t.Fatalf("a thread on a paused program answered %d, want 409: %v", code, body)
	}
}

// D11 - Penghapusan akun bersifat permanen: setelah saga selesai, masuk
// kembali dengan kredensial yang sama gagal.
func TestD11_DeletionIsPermanent(t *testing.T) {
	a := stack(t).register()
	if code, body := a.do(http.MethodDelete, "/delete-account", map[string]any{"password": password}); code != http.StatusAccepted {
		t.Fatalf("delete answered %d: %v", code, body)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		code, _, _ := a.call(http.MethodPost, "/login", map[string]any{"email": a.email, "password": password}, "")
		if code != http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the deleted account can still sign in a minute later")
		}
		time.Sleep(2 * time.Second)
	}
}

// S1 - Reset kata sandi menuntut token yang sah; tanpa token yang benar,
// kata sandi TIDAK diganti.
func TestS01_PasswordResetRequiresAValidToken(t *testing.T) {
	a := stack(t).register()
	code, _, _ := a.call(http.MethodPost, "/password-reset/confirm", map[string]any{
		"token": "bukan-token-yang-pernah-diterbitkan", "password": "kata-sandi-baru-123", "password_confirmation": "kata-sandi-baru-123",
	}, "")
	if code < 400 || code >= 500 {
		t.Fatalf("a bogus token answered %d, want a 4xx", code)
	}
	// Kata sandi lama masih berlaku: tidak ada yang diganti.
	if code, _, _ := a.call(http.MethodPost, "/login", map[string]any{"email": a.email, "password": password}, ""); code != http.StatusOK {
		t.Fatalf("the original password stopped working after a bogus reset: %d", code)
	}
}

// S2 - Penghapusan akun memverifikasi kata sandi.
func TestS02_DeleteAccountVerifiesThePassword(t *testing.T) {
	a := stack(t).register()
	if code, _ := a.do(http.MethodDelete, "/delete-account", map[string]any{"password": "salah"}); code != http.StatusForbidden {
		t.Fatalf("a wrong password answered %d, want 403", code)
	}
	if code, _ := a.do(http.MethodDelete, "/delete-account", map[string]any{}); code != http.StatusUnprocessableEntity {
		t.Fatalf("no password answered %d, want 422", code)
	}
	if code, _, _ := a.call(http.MethodGet, "/me", nil, a.token); code != http.StatusOK {
		t.Fatalf("the account was touched by a refused deletion: %d", code)
	}
}

// S8 - Token kedaluwarsa: setiap token membawa exp yang berada di masa depan
// yang terbatas, dan API melaporkan expires_at.
func TestS08_TokensExpire(t *testing.T) {
	a := stack(t).register()
	parts := strings.Split(a.token, ".")
	if len(parts) != 3 {
		t.Fatalf("the token is not a JWT: %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
		Iat int64 `json:"iat"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Exp == 0 {
		t.Fatal("the token has no exp claim; it would never expire (S8)")
	}
	if life := claims.Exp - claims.Iat; life <= 0 || life > 24*3600 {
		t.Fatalf("token lifetime = %ds, want positive and at most a day", life)
	}
}

// S9 - Otorisasi tidak membocorkan keberadaan sumber daya: milik orang lain
// dijawab 404, bukan 403.
func TestS09_OtherPeoplesResourcesLookNonexistent(t *testing.T) {
	owner := stack(t).register()
	owner.completeProfile()
	slug := owner.startAssessment()

	stranger := stack(t).register()
	if code, _ := stranger.do(http.MethodGet, "/risk-assessments/"+slug, nil); code != http.StatusNotFound {
		t.Fatalf("someone else's assessment answered %d, want 404", code)
	}
	if code, _ := stranger.do(http.MethodPatch, "/risk-assessments/"+slug+"/personalize", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("personalizing someone else's assessment answered %d, want 404", code)
	}
}

// S10 - Tidak ada bidang debug di kontrak dashboard.
func TestS10_DashboardCarriesNoDebugFields(t *testing.T) {
	a := stack(t).register()
	a.completeProfile()
	a.startAssessment()

	code, body := a.do(http.MethodGet, "/dashboard", nil)
	if code != http.StatusOK {
		t.Fatalf("dashboard answered %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	for _, leaked := range []string{"program_raw", "resource_keys", "program_is_null", "program_empty_check"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("the dashboard leaks debug field %q", leaked)
		}
	}
}
