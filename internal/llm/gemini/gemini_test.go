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

// Seluruh test di sini bicara ke httptest.Server di loopback.
//
// Itu bukan pelanggaran R6: yang dilarang adalah memanggil penyedia
// sungguhan - lambat, berbiaya, dan hasilnya berubah tiap jalankan. Server
// palsu di loopback tidak meninggalkan mesin, dan ia satu-satunya cara menguji
// perilaku HTTP yang sebenarnya: status, backoff, dan batas ukuran.

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

// TestAnAnswerComesBack adalah jalur normal.
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

	// Kunci lewat header, tidak pernah di URL. Query string muncul di log
	// proxy dan riwayat; header tidak.
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

// TestARateLimitIsRetriedAndThenSucceeds adalah alasan backoff ada.
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

// TestABadRequestIsNotRetried menghemat kuota dan mempercepat kegagalannya.
//
// Permintaan yang ditolak karena bentuknya salah akan ditolak dengan cara yang
// sama berapa kali pun diulang.
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

// TestARateLimitThatNeverLiftsIsReported menjaga galatnya tetap bisa dikenali.
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

// TestAServerErrorIsRetried menjaga 5xx tetap dianggap sementara.
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

// TestAnOversizedAnswerIsRefused menjaga memori worker.
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

// TestACancelledContextStopsTheRetryLoop menjaga worker tetap bisa dimatikan
// di tengah rangkaian percobaan.
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

// TestAFencedAnswerIsUnwrapped menjaga perilaku yang sudah ada di sistem lama.
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

// TestAnEmptyAnswerIsRefused menjaga jawaban kosong tidak tersimpan sebagai
// laporan.
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

// TestABlockedPromptIsReportedAsItself menjaga penolakan filter tidak terlihat
// seperti jawaban kosong biasa.
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

// TestATruncatedAnswerIsVisible menjaga laporan setengah jadi tidak tersimpan
// sebagai laporan utuh.
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

// TestTokenUsageIsReported: angka token datang dari penyedia, bukan ditaksir.
// Bentuk usageMetadata di sini disalin dari jawaban nyata gemini-3.8-flash.
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

// Penyedia yang tidak melaporkan token memberi nol, bukan galat.
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

// quota429 adalah body 429 yang benar-benar dikirim gemini-3.8-flash pada
// 2026-09-07, dipendekkan pada pesan bebasnya saja.
const quota429 = `{"error":{"code":429,"message":"You exceeded your current quota.","status":"RESOURCE_EXHAUSTED",` +
	`"details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"x","url":"y"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"m",` +
	`"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"model":"gemini-3.8-flash"},"quotaValue":"20"}]},` +
	`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"%s"}]}}`

// TestTheProvidersRetryDelayIsHonoured: jeda yang diminta 429 dipakai, bukan
// backoff milidetik sendiri - mengulang lebih cepat hanya membakar percobaan.
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

// Jeda yang diminta tidak boleh melampaui Timeout: "coba lagi besok" tidak
// boleh menahan partisi worker sampai besok.
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

// Kuota yang habis disebut namanya: per hari dan per menit menuntut tindakan
// yang berbeda, dan pesan bebasnya tidak membedakan keduanya.
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

// Retry-After di header menang atas retryDelay di body.
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
