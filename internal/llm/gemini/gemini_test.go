package gemini_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/gemini"
)

// Every test here speaks to an httptest.Server on loopback.
//
// That is not a violation of R6: what is forbidden is calling the real
// provider - slow, costly, and changing its result on every run. A fake server
// on loopback never leaves the machine, and it is the only way to test the
// actual HTTP behaviour: statuses, backoff, and size limits.

func request() llm.Request {
	return llm.Request{
		Prompt:        "explain this score",
		PromptVersion: "personalization@1",
		JSON:          true,
	}
}

func answer(text string) string {
	return fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":%q}],"role":"model"},`+
		`"finishReason":"STOP"}],"modelVersion":"gemini-test"}`, text)
}

func client(t *testing.T, srv *httptest.Server, tune func(*gemini.Config)) *gemini.Client {
	t.Helper()

	cfg := gemini.Config{
		APIKey:      "not-a-real-key",
		Model:       "gemini-test",
		Endpoint:    srv.URL,
		Timeout:     2 * time.Second,
		MaxAttempts: 3,
		BaseBackoff: time.Millisecond,
		HTTPClient:  srv.Client(),
	}
	if tune != nil {
		tune(&cfg)
	}

	c, err := gemini.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestAnAnswerComesBack is the normal path.
func TestAnAnswerComesBack(t *testing.T) {
	var gotPath, gotKey, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, answer(`{"riskSummary":{}}`))
	}))
	defer srv.Close()

	got, err := client(t, srv, nil).Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got.Text != `{"riskSummary":{}}` {
		t.Fatalf("the answer came back as %q", got.Text)
	}
	if got.Model != "gemini-test" {
		t.Fatalf("the model came back as %q, want the one the provider reported", got.Model)
	}
	if got.PromptVersion != "personalization@1" {
		t.Fatalf("the prompt version was lost: %q", got.PromptVersion)
	}
	if got.Truncated() {
		t.Fatal("an answer that finished with STOP reports itself as truncated")
	}

	if !strings.HasSuffix(gotPath, "/gemini-test:generateContent") {
		t.Fatalf("the request went to %q", gotPath)
	}

	// The key through a header, never in the URL. Query strings show up in
	// proxy logs and history; headers do not.
	if gotKey != "not-a-real-key" {
		t.Fatalf("the api key header is %q", gotKey)
	}
	if strings.Contains(gotPath, "not-a-real-key") {
		t.Fatal("the api key leaked into the URL")
	}
	if !strings.Contains(gotBody, `"response_mime_type":"application/json"`) {
		t.Fatalf("the JSON request did not ask for a JSON answer: %s", gotBody)
	}
}

// TestARateLimitIsRetriedAndThenSucceeds is the reason backoff exists.
func TestARateLimitIsRetriedAndThenSucceeds(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"quota exceeded"}}`)
			return
		}
		fmt.Fprint(w, answer("finally"))
	}))
	defer srv.Close()

	got, err := client(t, srv, nil).Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Text != "finally" {
		t.Fatalf("the answer came back as %q", got.Text)
	}
	if calls.Load() != 3 {
		t.Fatalf("the client made %d attempts, want 3", calls.Load())
	}
}

// TestABadRequestIsNotRetried saves quota and speeds up the failure.
//
// A request refused because its shape is wrong will be refused the same way
// however many times it is repeated.
func TestABadRequestIsNotRetried(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"the model name is wrong"}}`)
	}))
	defer srv.Close()

	_, err := client(t, srv, nil).Generate(context.Background(), request())
	if err == nil {
		t.Fatal("a 400 was accepted as an answer")
	}
	if calls.Load() != 1 {
		t.Fatalf("the client made %d attempts at a 400, want 1", calls.Load())
	}
	if !strings.Contains(err.Error(), "the model name is wrong") {
		t.Fatalf("the provider's own message was lost: %v", err)
	}
}

// TestARateLimitThatNeverLiftsIsReported keeps the error recognisable.
func TestARateLimitThatNeverLiftsIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"quota exceeded"}}`)
	}))
	defer srv.Close()

	_, err := client(t, srv, nil).Generate(context.Background(), request())
	if !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("Generate returned %v, want something that is ErrRateLimited", err)
	}
}

// TestAServerErrorIsRetried keeps 5xx treated as transient.
func TestAServerErrorIsRetried(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	if _, err := client(t, srv, nil).Generate(context.Background(), request()); err == nil {
		t.Fatal("a 502 was accepted as an answer")
	}
	if calls.Load() != 3 {
		t.Fatalf("the client made %d attempts at a 502, want 3", calls.Load())
	}
}

// TestAnOversizedAnswerIsRefused protects the worker's memory.
func TestAnOversizedAnswerIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, answer(strings.Repeat("x", 10_000)))
	}))
	defer srv.Close()

	req := request()
	req.MaxOutputBytes = 512

	_, err := client(t, srv, nil).Generate(context.Background(), req)
	if !errors.Is(err, llm.ErrTruncated) {
		t.Fatalf("Generate returned %v, want ErrTruncated", err)
	}
}

// TestACancelledContextStopsTheRetryLoop keeps the worker stoppable in the
// middle of a series of attempts.
func TestACancelledContextStopsTheRetryLoop(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	c := client(t, srv, func(cfg *gemini.Config) {
		cfg.MaxAttempts = 20
		cfg.BaseBackoff = 200 * time.Millisecond
	})

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.Generate(ctx, request())
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate returned %v, want context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the retry loop took %v to notice the cancellation", elapsed)
	}
}

// TestAFencedAnswerIsUnwrapped keeps a behaviour the legacy system already
// had.
func TestAFencedAnswerIsUnwrapped(t *testing.T) {
	cases := map[string]string{
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"```JSON\n{\"a\":1}\n```": `{"a":1}`,
		"```\n{\"a\":1}\n```":     `{"a":1}`,
		`{"a":1}`:                 `{"a":1}`,
	}

	for raw, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, answer(raw))
		}))

		got, err := client(t, srv, nil).Generate(context.Background(), request())
		srv.Close()

		if err != nil {
			t.Fatalf("Generate for %q: %v", raw, err)
		}
		if got.Text != want {
			t.Errorf("%q unwrapped to %q, want %q", raw, got.Text, want)
		}
	}
}

// TestAnEmptyAnswerIsRefused keeps an empty answer from being stored as a
// report.
func TestAnEmptyAnswerIsRefused(t *testing.T) {
	bodies := []string{
		`{"candidates":[]}`,
		`{"candidates":[{"content":{"parts":[{"text":"   "}]},"finishReason":"STOP"}]}`,
	}

	for _, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, body)
		}))

		_, err := client(t, srv, nil).Generate(context.Background(), request())
		srv.Close()

		if !errors.Is(err, llm.ErrEmptyAnswer) {
			t.Errorf("%s produced %v, want ErrEmptyAnswer", body, err)
		}
	}
}

// TestABlockedPromptIsReportedAsItself keeps a filter refusal from looking
// like an ordinary empty answer.
func TestABlockedPromptIsReportedAsItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"promptFeedback":{"blockReason":"SAFETY"}}`)
	}))
	defer srv.Close()

	_, err := client(t, srv, nil).Generate(context.Background(), request())
	if err == nil || !strings.Contains(err.Error(), "SAFETY") {
		t.Fatalf("Generate returned %v, want the block reason", err)
	}
}

// TestATruncatedAnswerIsVisible keeps a half-finished report from being
// stored as a whole one.
func TestATruncatedAnswerIsVisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"half a rep"}]},`+
			`"finishReason":"MAX_TOKENS"}],"modelVersion":"gemini-test"}`)
	}))
	defer srv.Close()

	got, err := client(t, srv, nil).Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !got.Truncated() {
		t.Fatal("an answer that stopped at MAX_TOKENS reports itself as complete")
	}
}

// TestAClientWithoutAKeyIsRefused menjaga ADR-016: kredensial tanpa bawaan.
func TestAClientWithoutAKeyIsRefused(t *testing.T) {
	if _, err := gemini.New(gemini.Config{Model: "gemini-test"}); err == nil {
		t.Fatal("a client with no API key was created")
	}
	if _, err := gemini.New(gemini.Config{APIKey: "k"}); err == nil {
		t.Fatal("a client with no model was created")
	}
}

// TestTokenUsageIsReported: the token numbers come from the provider, not
// from an estimate. The usageMetadata shape here is copied from a real
// gemini-3.8-flash answer.
func TestTokenUsageIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{}"}],"role":"model"},"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2,"totalTokenCount":110,"thoughtsTokenCount":100},`+
			`"modelVersion":"gemini-3.8-flash"}`)
	}))
	defer srv.Close()

	got, err := client(t, srv, nil).Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := llm.Usage{InputTokens: 8, OutputTokens: 2, ThoughtsTokens: 100}
	if got.Usage != want {
		t.Fatalf("usage came back as %+v, want %+v", got.Usage, want)
	}
	if got.Usage.Total() != 110 {
		t.Fatalf("total is %d, want 110 (the provider's totalTokenCount)", got.Usage.Total())
	}
}

// A provider that reports no tokens gives zero, not an error.
func TestMissingUsageIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, answer(`{}`))
	}))
	defer srv.Close()

	got, err := client(t, srv, nil).Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Usage != (llm.Usage{}) {
		t.Fatalf("usage should be zero without usageMetadata, got %+v", got.Usage)
	}
}

// quota429 is the 429 body gemini-3.8-flash actually sent on 2026-09-07,
// shortened only in its free-text message.
const quota429 = `{"error":{"code":429,"message":"You exceeded your current quota.","status":"RESOURCE_EXHAUSTED",` +
	`"details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"x","url":"y"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"m",` +
	`"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-3.8-flash"},"quotaValue":"20"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"%s"}]}}`

// TestTheProvidersRetryDelayIsHonoured: the pause the 429 asks for is used,
// not our own millisecond backoff - retrying faster only burns attempts.
func TestTheProvidersRetryDelayIsHonoured(t *testing.T) {
	var calls atomic.Int32
	var gaps []time.Time

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gaps = append(gaps, time.Now())
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, quota429, "300ms")
			return
		}
		fmt.Fprint(w, answer("after the delay"))
	}))
	defer srv.Close()

	got, err := client(t, srv, nil).Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Text != "after the delay" {
		t.Fatalf("answer %q", got.Text)
	}
	if len(gaps) != 2 {
		t.Fatalf("%d attempts, want 2", len(gaps))
	}
	if waited := gaps[1].Sub(gaps[0]); waited < 300*time.Millisecond {
		t.Fatalf("the second attempt came after %v, want at least the 300ms the provider asked for", waited)
	}
}

// The requested pause must not exceed Timeout: "try again tomorrow" must
// not hold the worker's partition until tomorrow.
func TestARetryDelayIsCappedByTheTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, quota429, "3600s")
			return
		}
		fmt.Fprint(w, answer("ok"))
	}))
	defer srv.Close()

	started := time.Now()
	_, err := client(t, srv, func(c *gemini.Config) { c.Timeout = 200 * time.Millisecond }).
		Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("waited %v; the hour the provider asked for must be capped by the timeout", took)
	}
}

// The exhausted quota is named: per day and per minute call for different
// actions, and the free-text message does not tell them apart.
func TestTheExhaustedQuotaIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, quota429, "1ms")
	}))
	defer srv.Close()

	_, err := client(t, srv, nil).Generate(context.Background(), request())
	if err == nil {
		t.Fatal("a quota that never lifts must be reported")
	}
	if !strings.Contains(err.Error(), "GenerateRequestsPerDayPerProjectPerModel-FreeTier") {
		t.Fatalf("the error does not name the quota: %v", err)
	}
	if !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("a 429 must still read as a rate limit: %v", err)
	}
}

// Retry-After in the header wins over retryDelay in the body.
func TestRetryAfterHeaderWins(t *testing.T) {
	var calls atomic.Int32
	var gaps []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gaps = append(gaps, time.Now())
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"high demand"}}`)
			return
		}
		fmt.Fprint(w, answer("ok"))
	}))
	defer srv.Close()

	if _, err := client(t, srv, nil).Generate(context.Background(), request()); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if waited := gaps[1].Sub(gaps[0]); waited < time.Second {
		t.Fatalf("waited %v, want at least the 1s Retry-After", waited)
	}
}
