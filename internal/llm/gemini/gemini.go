// Package gemini speaks HTTP to Google's Generative Language API.
//
// It is split from internal/llm deliberately: the parent package is kept free
// of any network package in its dependency tree, so the fake provider every
// test uses CANNOT touch the network. Everything that knows how to connect
// lives here.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

// DefaultEndpoint is the real API address.
const DefaultEndpoint = "https://generativelanguage.googleapis.com/v1beta/models"

// Config is what this adapter needs.
type Config struct {
	// APIKey has no default, and that is deliberate (ADR-016). A default key
	// means there is a state in which the system runs with a credential nobody
	// ever intended.
	APIKey string

	// Model is the model name, for example "gemini-2.5-flash-lite".
	Model string

	// Endpoint can be pointed at another server for testing. Empty means
	// DefaultEndpoint.
	Endpoint string

	// Timeout is the deadline of ONE attempt, not of the whole series of
	// attempts. The distinction matters: a deadline covering the whole series
	// gives the last attempt an unpredictable remainder of time.
	Timeout time.Duration

	// MaxAttempts includes the first attempt. 1 means no retries.
	MaxAttempts int

	// BaseBackoff is the pause after the first failed attempt. The following
	// pauses double, with jitter.
	BaseBackoff time.Duration

	// HTTPClient can be injected. Empty means a client with the Timeout above.
	HTTPClient *http.Client
}

// Defaults used when Config leaves a field empty.
const (
	defaultTimeout     = 120 * time.Second
	defaultMaxAttempts = 3
	defaultBaseBackoff = time.Second
)

// Client is the LLM provider that speaks to Gemini.
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates the client.
//
// It refuses an incomplete configuration instead of using defaults for
// credentials: a process that fails at start is far easier to explain than a
// process that runs and is then refused by the provider on the first real
// request.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("GEMINI_API_KEY is not set")
	}
	if cfg.Model == "" {
		return nil, errors.New("no gemini model was named")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = defaultBaseBackoff
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, http: httpClient}, nil
}

var _ llm.Provider = (*Client)(nil)

func (c *Client) Name() string { return "gemini" }

// Generate asks for one answer, with retries for failures that are worth
// retrying.
func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		resp, err := c.attempt(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if !retryable(err) {
			// A request refused because its shape is wrong will be refused the same
			// way however many times it is repeated. Repeating it only spends quota
			// and delays the failure.
			return nil, err
		}
		if attempt == c.cfg.MaxAttempts {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.wait(attempt, err)):
		}
	}
	return nil, fmt.Errorf("gemini gave up after %d attempts: %w", c.cfg.MaxAttempts, lastErr)
}

// wait is the pause before the next attempt: our own backoff, or the pause
// the provider asks for if that is longer. Retrying faster than asked only
// burns attempts on the same 429 answer - that is what happened on the
// first real run (docs/finops.md). Bounded by Timeout so a "try again
// tomorrow" request does not hold the partition forever.
func (c *Client) wait(attempt int, err error) time.Duration {
	d := c.backoff(attempt)
	var api *apiError
	if errors.As(err, &api) && api.retryAfter > d {
		d = api.retryAfter
	}
	if d > c.cfg.Timeout {
		d = c.cfg.Timeout
	}
	return d
}

// backoff computes the pause before the next attempt.
//
// Its jitter is not decoration: without it, every worker that failed at the
// same moment would retry at the same moment too, and a provider that has just
// recovered is hit by a wave of the same size straight away.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.cfg.BaseBackoff << (attempt - 1)

	// Full jitter: random in [0, d]. It spreads retries as widely as possible,
	// and that is what keeps the next wave furthest apart.
	//nolint:gosec // This is scheduling, not cryptography.
	return time.Duration(rand.Int64N(int64(d) + 1))
}

// attempt runs one request.
func (c *Client) attempt(ctx context.Context, req llm.Request) (*llm.Response, error) {
	// A per-attempt deadline, on top of the caller's ctx. Whichever runs out
	// first applies.
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	body, err := json.Marshal(buildRequest(req))
	if err != nil {
		return nil, fmt.Errorf("encoding the gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/%s:generateContent", strings.TrimSuffix(c.cfg.Endpoint, "/"), c.cfg.Model)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the gemini request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// The key is sent through a header, NOT through the query string as in the
	// legacy system. Query strings show up in proxy logs, history, and error
	// messages; headers do not.
	httpReq.Header.Set("x-goog-api-key", c.cfg.APIKey)

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, &transportError{err: err}
	}
	defer func() {
		// The remainder is drained first so the connection can be reused instead
		// of discarded along with it. An error here is logged, not returned: the
		// answer has already been read, and a failure to clean up the connection
		// does not invalidate it.
		if _, err := io.Copy(io.Discard, io.LimitReader(httpResp.Body, 4<<10)); err != nil {
			slog.Warn("draining the gemini response", "error", err)
		}
		if err := httpResp.Body.Close(); err != nil {
			slog.Warn("closing the gemini response", "error", err)
		}
	}()

	// Read with a bound. Without one, one rambling answer - or a misbehaving
	// server - could fill the worker's memory.
	limit := int64(req.Limit())
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, limit+1))
	if err != nil {
		return nil, &transportError{err: fmt.Errorf("reading the gemini answer: %w", err)}
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: the answer exceeded %d bytes", llm.ErrTruncated, limit)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, statusError(httpResp.StatusCode, raw, httpResp.Header)
	}
	return decode(raw, req)
}

// transportError marks a failure that came from the network, not from the
// provider's answer. It is always worth retrying.
type transportError struct{ err error }

func (e *transportError) Error() string { return "reaching gemini: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// apiError is a refusal from the provider together with its status.
type apiError struct {
	status  int
	message string

	// quota names WHICH quota is exhausted (google.rpc.QuotaFailure), if any.
	// "Per day" and "per minute" call for different actions, and the free-text
	// message does not tell them apart.
	quota string

	// retryAfter is the pause the provider ASKED for (google.rpc.RetryInfo or
	// the Retry-After header). Zero means none was asked for.
	retryAfter time.Duration
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("gemini answered %d: %s", e.status, e.message)
	if e.quota != "" {
		msg += " (quota " + e.quota + ")"
	}
	return msg
}

func (e *apiError) Unwrap() error {
	if e.status == http.StatusTooManyRequests {
		return llm.ErrRateLimited
	}
	return nil
}

// retryable decides whether a failure is worth retrying.
//
// Worth it: network failures, 429, and 5xx - all transient states. Not worth
// it: 4xx other than 429, because the same request will be refused the same
// way. Telling them apart saves quota and speeds up the failure.
func retryable(err error) bool {
	var transport *transportError
	if errors.As(err, &transport) {
		return true
	}

	var api *apiError
	if errors.As(err, &api) {
		return api.status == http.StatusTooManyRequests || api.status >= 500
	}
	return false
}

func statusError(status int, raw []byte, header http.Header) error {
	// The google.rpc.Status shape. The field names were verified against a
	// real 429 answer from gemini-3.8-flash on 2026-09-07 (free quota of 20
	// requests per day per model), not from memory.
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				RetryDelay string `json:"retryDelay"`
				Violations []struct {
					QuotaID string `json:"quotaId"`
				} `json:"violations"`
			} `json:"details"`
		} `json:"error"`
	}

	out := &apiError{status: status, message: strings.TrimSpace(string(raw))}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		out.message = envelope.Error.Message
		for _, d := range envelope.Error.Details {
			if d.RetryDelay != "" {
				if delay, err := time.ParseDuration(d.RetryDelay); err == nil && delay > 0 {
					out.retryAfter = delay
				}
			}
			if len(d.Violations) > 0 && d.Violations[0].QuotaID != "" {
				out.quota = d.Violations[0].QuotaID
			}
		}
	}
	if len(out.message) > 500 {
		out.message = out.message[:500] + "..."
	}

	// Retry-After (seconds) wins when present: it is what the proxies and CDNs
	// in front of the provider read, and it is closer to the real state.
	if secs, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && secs > 0 {
		out.retryAfter = time.Duration(secs) * time.Second
	}
	return out
}
