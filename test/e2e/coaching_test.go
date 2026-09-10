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

// These tests drive the RUNNING system over HTTP - five services, a broker, and
// a real database.
//
// They are no substitute for the per-package integration tests: what is proven
// here is what no single package can prove on its own - that a request entering
// through the gateway really becomes a job, and that its result really comes
// back to where it is awaited.
//
// Without TEST_E2E_BASE_URL they skip themselves; in CI they FAIL instead of
// skipping, because a test that silently skips itself in CI turns the pipeline
// green without checking anything.

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

// client runs HTTP requests against the gateway.
type client struct {
	t     *testing.T
	base  string
	token string
	http  *http.Client

	// email is kept so a test that needs to sign in again - to prove a deleted
	// account is really gone, say - need not reconstruct it from the test
	// name.
	email string
}

// defaultPassword is used by every account register() creates.
//
// It is a constant, not a repeated literal: the account deletion test has to
// send the SAME password to confirm, and two literals slowly drifting apart
// would make that test fail for the wrong reason.
const defaultPassword = "correct-horse-battery"

func newClient(t *testing.T) *client {
	t.Helper()
	return &client{
		t:    t,
		base: baseURL(t),
		// A timeout on the client, not only on ctx: a test that hangs because one
		// service is silent holds the whole suite until the package timeout, and
		// the message does not say which request.
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// do runs one request and returns the status together with the body.
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

	// 204 has no body, and forcing it into JSON would fail a request that
	// actually succeeded.
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

// register creates a new account and keeps its token.
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

	// The token is at the ROOT of the answer, not under "data" like other
	// resources. That shape is kept from the legacy system, and this test once
	// failed by assuming otherwise - which is useful in itself: a wrong
	// expectation about the response shape is exactly what an end-to-end test
	// is meant to find.
	token, _ := body["access_token"].(string)
	if token == "" {
		c.t.Fatalf("register returned no access token: %v", body)
	}
	c.token = token
	c.email = email
}

// doAnonymous runs a request WITHOUT a token.
//
// Used by tests that need to prove something about an account whose token no
// longer works - signing in again after the account is deleted, say.
func (c *client) doAnonymous(method, path string, body any) (int, map[string]any) {
	c.t.Helper()

	saved := c.token
	c.token = ""
	defer func() { c.token = saved }()

	return c.do(method, path, body)
}

// dig reads a nested value without forcing the caller to write layered type
// assertions, which hide the layer at which the shape changed.
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

// TestACoachingProgramRunsFromRequestToCompletedTask is gate F4-17.
//
// Start a program -> the curriculum arrives -> complete a task -> request
// the graduation report. Every step goes over HTTP, and every step crosses
// a service boundary: the gateway, coaching-svc, Kafka, llm-worker, and
// back.
func TestACoachingProgramRunsFromRequestToCompletedTask(t *testing.T) {
	c := newClient(t)
	c.register()

	// 1. The program is started. The answer is 202, NOT 200 with the
	//    curriculum - the legacy system held the HTTP request while the model
	//    worked.
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

	// 2. The curriculum arrives. It comes through Kafka and llm-worker, so
	//    what is awaited is the state of the system - not the answer to one
	//    request.
	program := c.waitForCurriculum(slug, 90*time.Second)

	weeks, _ := dig(program, "data", "weeks").([]any)
	if len(weeks) == 0 {
		t.Fatalf("the curriculum arrived with no weeks: %v", program)
	}

	// The week numbers run consecutively from one. A curriculum with gaps
	// would show a program full of holes.
	for i, rw := range weeks {
		week, _ := rw.(map[string]any)
		number, _ := week["week_number"].(float64)
		if int(number) != i+1 {
			t.Fatalf("week at position %d is numbered %v", i, number)
		}
	}

	// And the end date is computed from the number of weeks that ACTUALLY
	// arrived (F4-18), not from created_at plus 28 days.
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

	// Flipped again, then completed again: idempotency is tested through the
	// real path, not only in the unit.
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

// waitForCurriculum waits for the curriculum to arrive, then returns the
// program.
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
			// A failure is reported AS IT IS, not waited out until the deadline:
			// waiting for something that has already given up only hides the cause
			// behind a timeout message.
			c.t.Fatalf("the curriculum failed: %v", body)
		}
		time.Sleep(2 * time.Second)
	}

	c.t.Fatalf("the curriculum never arrived within %v; last state: %v", timeout, last)
	return nil
}

// assertEndDateMatchesWeeks checks F4-18 through the public API.
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

// firstTaskID takes the id of the first task from the curriculum.
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

// TestSomeoneElsesCoachingProgramIsNotFound is S9 through the real path.
//
// It is tested here, not only in the unit, because authorisation passes
// through three layers: the token is verified by the gateway, user_id is
// passed on over gRPC, and ownership is checked by the service. A mistake
// in any one of them is invisible from any single layer on its own.
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

	// And a program that REALLY does not exist answers the same. Telling the
	// two apart tells the asker that the slug exists.
	if code, _ := stranger.do(http.MethodGet,
		"/api/v1/coaching/programs/tidakadaslugini", nil); code != http.StatusNotFound {
		t.Errorf("a missing program answered %d, want 404", code)
	}
}

// TestAPausedProgramFreezesInteraction is D5 through the real path.
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

	// Resumed again, and interaction comes back to life.
	if code, _ := c.do(http.MethodPatch,
		"/api/v1/coaching/programs/"+slug+"/toggle-program-status", nil); code != http.StatusOK {
		t.Fatalf("resuming the program answered %d", code)
	}
	if code, _ := c.do(http.MethodPost, "/api/v1/coaching/programs/"+slug+"/threads",
		map[string]any{"message": "halo pelatih"}); code != http.StatusAccepted {
		t.Fatalf("opening a thread on a resumed program answered %d, want 202", code)
	}
}

// TestAThreadReplyComesBackFromTheWorker proves the chat reply path.
//
// It is the path that crosses the most boundaries: the message comes in over
// HTTP, the request goes out through the outbox, the worker answers it, and
// the reply comes back to the same thread as a message with the "model" role.
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

	// The title is derived from the first message, with the truncation suffix
	// (D12).
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
