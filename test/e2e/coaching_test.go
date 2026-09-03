package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// Test ini menjalankan sistem yang SEDANG BERJALAN lewat HTTP - lima service,
// broker, dan basis data yang sungguhan.
//
// Ia bukan pengganti test integrasi per paket: yang dibuktikan di sini adalah
// hal yang tidak bisa dibuktikan satu paket sendirian - bahwa permintaan yang
// masuk lewat gateway benar-benar menjadi pekerjaan, dan hasilnya benar-benar
// kembali ke tempat yang menunggunya.
//
// Tanpa TEST_E2E_BASE_URL ia melewati dirinya sendiri; di CI ia GAGAL alih-alih
// dilewati, karena test yang diam-diam melewati dirinya di CI membuat pipeline
// hijau tanpa memeriksa apa pun.

func baseURL(t *testing.T) string {
	t.Helper()

	url := os.Getenv("TEST_E2E_BASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_E2E_BASE_URL is not set; end-to-end tests must not be skipped in CI")
		}
		t.Skip("TEST_E2E_BASE_URL is not set; start the stack with 'task up:apps' to run this test")
	}
	return url
}

// client menjalankan permintaan HTTP terhadap gateway.
type client struct {
	t     *testing.T
	base  string
	token string
	http  *http.Client

	// email disimpan supaya test yang perlu masuk lagi - misalnya untuk
	// membuktikan akun yang dihapus benar-benar hilang - tidak perlu
	// menebaknya kembali dari nama testnya.
	email string
}

// defaultPassword dipakai setiap akun yang dibuat register().
//
// Ia konstanta, bukan literal yang diulang: test penghapusan akun harus
// mengirimkan kata sandi yang SAMA untuk mengonfirmasi, dan dua literal yang
// perlahan menyimpang akan membuat test itu gagal dengan alasan yang salah.
const defaultPassword = "correct-horse-battery"

func newClient(t *testing.T) *client {
	t.Helper()
	return &client{
		t:    t,
		base: baseURL(t),
		// Batas waktu di klien, bukan hanya di ctx: test yang menggantung
		// karena satu service diam akan menahan seluruh suite sampai timeout
		// paketnya, dan pesannya tidak menyebutkan permintaan mana.
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// do menjalankan satu permintaan dan mengembalikan status beserta badannya.
func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encoding the request: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+path, payload)
	if err != nil {
		c.t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			c.t.Logf("closing the response body: %v", err)
		}
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.t.Fatalf("reading the response: %v", err)
	}

	// 204 tidak punya badan, dan memaksanya menjadi JSON akan menggagalkan
	// permintaan yang justru berhasil.
	if len(bytes.TrimSpace(raw)) == 0 {
		return resp.StatusCode, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		c.t.Fatalf("%s %s answered %d with something that is not JSON: %s",
			method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode, decoded
}

// register membuat akun baru dan menyimpan tokennya.
func (c *client) register() {
	c.t.Helper()

	email := fmt.Sprintf("e2e-%d-%s@user.co", time.Now().UnixNano(), c.t.Name())
	code, body := c.do(http.MethodPost, "/api/v1/register", map[string]any{
		"name":                  "E2E",
		"email":                 email,
		"password":              defaultPassword,
		"password_confirmation": defaultPassword,
	})
	if code != http.StatusCreated && code != http.StatusOK {
		c.t.Fatalf("register answered %d: %v", code, body)
	}

	// Token ada di AKAR jawaban, bukan di bawah "data" seperti sumber daya
	// lain. Bentuk itu dipertahankan dari sistem lama, dan test ini pernah
	// gagal karena mengandaikan sebaliknya - yang justru berguna: ekspektasi
	// yang salah tentang bentuk respons adalah hal yang seharusnya ditemukan
	// test ujung ke ujung.
	token, _ := body["access_token"].(string)
	if token == "" {
		c.t.Fatalf("register returned no access token: %v", body)
	}
	c.token = token
	c.email = email
}

// doAnonymous menjalankan permintaan TANPA token.
//
// Dipakai test yang perlu membuktikan sesuatu tentang akun yang tokennya sudah
// tidak berlaku - masuk kembali setelah akun dihapus, misalnya.
func (c *client) doAnonymous(method, path string, body any) (int, map[string]any) {
	c.t.Helper()

	saved := c.token
	c.token = ""
	defer func() { c.token = saved }()

	return c.do(method, path, body)
}

// dig membaca nilai bersarang tanpa memaksa pemanggil menulis type assertion
// berlapis, yang menyembunyikan di lapisan mana bentuknya berubah.
func dig(m map[string]any, keys ...string) any {
	var current any = m
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = asMap[key]
	}
	return current
}

// TestACoachingProgramRunsFromRequestToCompletedTask adalah gate F4-17.
//
// Mulai program -> kurikulum tiba -> selesaikan tugas -> minta laporan
// kelulusan. Setiap langkah lewat HTTP, dan setiap langkah menyeberangi
// batas service: gateway, coaching-svc, Kafka, llm-worker, dan kembali.
func TestACoachingProgramRunsFromRequestToCompletedTask(t *testing.T) {
	c := newClient(t)
	c.register()

	// 1. Program dimulai. Jawabannya 202, BUKAN 200 dengan kurikulumnya -
	//    sistem lama menahan permintaan HTTP selama model bekerja.
	code, body := c.do(http.MethodPost, "/api/v1/coaching/programs", map[string]any{
		"difficulty": "Standar & Konsisten",
	})
	if code != http.StatusAccepted {
		t.Fatalf("starting a program answered %d: %v", code, body)
	}

	slug, _ := dig(body, "data", "slug").(string)
	if slug == "" {
		t.Fatalf("the program has no slug: %v", body)
	}
	if status, _ := dig(body, "data", "curriculum_status").(string); status != "pending" {
		t.Fatalf("a new program has curriculum status %q, want pending", status)
	}

	// 2. Kurikulumnya tiba. Ia datang lewat Kafka dan llm-worker, jadi yang
	//    ditunggu adalah keadaan sistem - bukan jawaban satu permintaan.
	program := c.waitForCurriculum(slug, 90*time.Second)

	weeks, _ := dig(program, "data", "weeks").([]any)
	if len(weeks) == 0 {
		t.Fatalf("the curriculum arrived with no weeks: %v", program)
	}

	// Nomor pekannya berurutan dari satu. Kurikulum yang melompat akan
	// menampilkan program yang berlubang.
	for i, rw := range weeks {
		week, _ := rw.(map[string]any)
		number, _ := week["week_number"].(float64)
		if int(number) != i+1 {
			t.Fatalf("week at position %d is numbered %v", i, number)
		}
	}

	// Dan tanggal akhirnya dihitung dari jumlah pekan yang BENAR-BENAR datang
	// (F4-18), bukan dari created_at ditambah 28 hari.
	assertEndDateMatchesWeeks(t, program, len(weeks))

	// 3. Satu tugas diselesaikan.
	taskID := firstTaskID(t, weeks)

	code, toggled := c.do(http.MethodPatch,
		"/api/v1/coaching/tasks/"+taskID+"/toggle-task-status", nil)
	if code != http.StatusOK {
		t.Fatalf("toggling a task answered %d: %v", code, toggled)
	}
	if done, _ := dig(toggled, "data", "is_completed").(bool); !done {
		t.Fatalf("the task did not report itself completed: %v", toggled)
	}

	// Dibalik lagi, lalu diselesaikan lagi: idempotensi diuji lewat jalur
	// yang sesungguhnya, bukan hanya di unit.
	if code, _ := c.do(http.MethodPatch,
		"/api/v1/coaching/tasks/"+taskID+"/toggle-task-status", nil); code != http.StatusOK {
		t.Fatalf("reopening the task answered %d", code)
	}
	code, again := c.do(http.MethodPatch,
		"/api/v1/coaching/tasks/"+taskID+"/toggle-task-status", nil)
	if code != http.StatusOK {
		t.Fatalf("completing the task again answered %d", code)
	}
	if done, _ := dig(again, "data", "is_completed").(bool); !done {
		t.Fatalf("the task did not report itself completed on the third toggle")
	}

	// 4. Laporan kelulusan diminta. Ia 202 selama masih dibuat.
	code, report := c.do(http.MethodGet,
		"/api/v1/coaching/programs/"+slug+"/graduation-report", nil)
	if code != http.StatusAccepted && code != http.StatusOK {
		t.Fatalf("requesting the graduation report answered %d: %v", code, report)
	}
	if status, _ := dig(report, "data", "status").(string); status == "not_requested" {
		t.Fatalf("the report was not queued: %v", report)
	}
}

// waitForCurriculum menunggu kurikulum tiba, lalu mengembalikan programnya.
func (c *client) waitForCurriculum(slug string, timeout time.Duration) map[string]any {
	c.t.Helper()

	deadline := time.Now().Add(timeout)
	var last map[string]any

	for time.Now().Before(deadline) {
		code, body := c.do(http.MethodGet, "/api/v1/coaching/programs/"+slug, nil)
		if code != http.StatusOK {
			c.t.Fatalf("reading the program answered %d: %v", code, body)
		}
		last = body

		switch status, _ := dig(body, "data", "curriculum_status").(string); status {
		case "ready":
			return body
		case "failed":
			// Kegagalan dilaporkan APA ADANYA, bukan ditunggu sampai batas
			// waktu: menunggu sesuatu yang sudah menyerah hanya menyembunyikan
			// sebabnya di balik pesan timeout.
			c.t.Fatalf("the curriculum failed: %v", body)
		}
		time.Sleep(2 * time.Second)
	}

	c.t.Fatalf("the curriculum never arrived within %v; last state: %v", timeout, last)
	return nil
}

// assertEndDateMatchesWeeks memeriksa F4-18 lewat API publik.
func assertEndDateMatchesWeeks(t *testing.T, program map[string]any, weeks int) {
	t.Helper()

	startRaw, _ := dig(program, "data", "start_date").(string)
	endRaw, _ := dig(program, "data", "end_date").(string)

	start, err := time.Parse(time.DateOnly, startRaw)
	if err != nil {
		t.Fatalf("the start date is unreadable: %q", startRaw)
	}
	end, err := time.Parse(time.DateOnly, endRaw)
	if err != nil {
		t.Fatalf("the end date is unreadable: %q", endRaw)
	}

	want := start.AddDate(0, 0, weeks*7)
	if !end.Equal(want) {
		t.Fatalf("a %d-week program ends on %s, want %s - the end date is not derived "+
			"from the weeks that actually arrived (F4-18)",
			weeks, end.Format(time.DateOnly), want.Format(time.DateOnly))
	}
}

// firstTaskID mengambil id tugas pertama dari kurikulum.
func firstTaskID(t *testing.T, weeks []any) string {
	t.Helper()

	for _, rw := range weeks {
		week, _ := rw.(map[string]any)
		tasks, _ := week["tasks"].([]any)
		for _, rt := range tasks {
			task, _ := rt.(map[string]any)
			if id, _ := task["id"].(string); id != "" {
				return id
			}
		}
	}

	t.Fatal("the curriculum arrived without a single task")
	return ""
}

// TestSomeoneElsesCoachingProgramIsNotFound adalah S9 lewat jalur yang
// sesungguhnya.
//
// Ia diuji di sini, bukan hanya di unit, karena otorisasi melewati tiga
// lapisan: token diverifikasi gateway, user_id diteruskan lewat gRPC, dan
// kepemilikan diperiksa service. Kekeliruan di salah satunya tidak terlihat
// dari salah satu lapisan sendirian.
func TestSomeoneElsesCoachingProgramIsNotFound(t *testing.T) {
	owner := newClient(t)
	owner.register()

	code, body := owner.do(http.MethodPost, "/api/v1/coaching/programs", map[string]any{
		"difficulty": "Santai & Bertahap",
	})
	if code != http.StatusAccepted {
		t.Fatalf("starting a program answered %d: %v", code, body)
	}
	slug, _ := dig(body, "data", "slug").(string)

	stranger := newClient(t)
	stranger.register()

	for _, probe := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/coaching/programs/" + slug},
		{http.MethodPatch, "/api/v1/coaching/programs/" + slug + "/toggle-program-status"},
		{http.MethodDelete, "/api/v1/coaching/programs/" + slug},
		{http.MethodGet, "/api/v1/coaching/programs/" + slug + "/graduation-report"},
	} {
		if code, _ := stranger.do(probe.method, probe.path, nil); code != http.StatusNotFound {
			t.Errorf("%s %s answered %d, want 404", probe.method, probe.path, code)
		}
	}

	// Dan program yang MEMANG tidak ada menjawab sama. Membedakan keduanya
	// memberi tahu penanya bahwa slug itu ada.
	if code, _ := stranger.do(http.MethodGet,
		"/api/v1/coaching/programs/tidakadaslugini", nil); code != http.StatusNotFound {
		t.Errorf("a missing program answered %d, want 404", code)
	}
}

// TestAPausedProgramFreezesInteraction adalah D5 lewat jalur yang sesungguhnya.
func TestAPausedProgramFreezesInteraction(t *testing.T) {
	c := newClient(t)
	c.register()

	code, body := c.do(http.MethodPost, "/api/v1/coaching/programs", map[string]any{
		"difficulty": "Intensif & Menantang",
	})
	if code != http.StatusAccepted {
		t.Fatalf("starting a program answered %d: %v", code, body)
	}
	slug, _ := dig(body, "data", "slug").(string)

	if code, _ := c.do(http.MethodPatch,
		"/api/v1/coaching/programs/"+slug+"/toggle-program-status", nil); code != http.StatusOK {
		t.Fatalf("pausing the program answered %d", code)
	}

	code, refused := c.do(http.MethodPost, "/api/v1/coaching/programs/"+slug+"/threads",
		map[string]any{"message": "halo pelatih"})
	if code != http.StatusConflict {
		t.Fatalf("opening a thread on a paused program answered %d, want 409: %v", code, refused)
	}

	// Dilanjutkan lagi, dan interaksinya hidup kembali.
	if code, _ := c.do(http.MethodPatch,
		"/api/v1/coaching/programs/"+slug+"/toggle-program-status", nil); code != http.StatusOK {
		t.Fatalf("resuming the program answered %d", code)
	}
	if code, _ := c.do(http.MethodPost, "/api/v1/coaching/programs/"+slug+"/threads",
		map[string]any{"message": "halo pelatih"}); code != http.StatusAccepted {
		t.Fatalf("opening a thread on a resumed program answered %d, want 202", code)
	}
}

// TestAThreadReplyComesBackFromTheWorker membuktikan jalur balasan chat.
//
// Ia jalur yang paling banyak menyeberang: pesan masuk lewat HTTP, permintaan
// keluar lewat outbox, worker menjawabnya, dan balasannya kembali ke thread
// yang sama sebagai pesan berperan "model".
func TestAThreadReplyComesBackFromTheWorker(t *testing.T) {
	c := newClient(t)
	c.register()

	code, body := c.do(http.MethodPost, "/api/v1/coaching/programs", map[string]any{
		"difficulty": "Standar & Konsisten",
	})
	if code != http.StatusAccepted {
		t.Fatalf("starting a program answered %d: %v", code, body)
	}
	slug, _ := dig(body, "data", "slug").(string)

	code, thread := c.do(http.MethodPost, "/api/v1/coaching/programs/"+slug+"/threads",
		map[string]any{"message": "Saya kesulitan bangun pagi, ada saran?"})
	if code != http.StatusAccepted {
		t.Fatalf("opening a thread answered %d: %v", code, thread)
	}
	threadSlug, _ := dig(thread, "data", "slug").(string)

	// Judulnya diturunkan dari pesan pertama, beserta sufiks pemotongan (D12).
	if title, _ := dig(thread, "data", "title").(string); title != "Saya kesulitan bangun pagi, ada saran?" {
		t.Fatalf("the derived title is %q", title)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		code, shown := c.do(http.MethodGet, "/api/v1/coaching/threads/"+threadSlug, nil)
		if code != http.StatusOK {
			t.Fatalf("reading the thread answered %d: %v", code, shown)
		}

		messages, _ := dig(shown, "data", "messages").([]any)
		for _, rm := range messages {
			message, _ := rm.(map[string]any)
			if role, _ := message["role"].(string); role == "model" {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}

	t.Fatal("the model never replied within 90 seconds")
}
