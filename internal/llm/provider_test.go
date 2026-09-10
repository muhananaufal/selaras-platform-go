package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

func request() llm.Request {
	return llm.Request{
		System:        "you are a careful clinician",
		Prompt:        "explain this score",
		PromptVersion: "personalization@1",
		JSON:          true,
	}
}

// TestTheFakeIsDeterministic is the reason this fake provider exists.
//
// A test whose result changes from one run to the next proves nothing, and an
// idempotency test loses all its meaning if two calls produce different
// answers.
func TestTheFakeIsDeterministic(t *testing.T) {
	ctx := context.Background()

	first, err := llm.NewFake().Generate(ctx, request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	second, err := llm.NewFake().Generate(ctx, request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if first.Text != second.Text {
		t.Fatalf("two identical requests answered differently:\n  %s\n  %s", first.Text, second.Text)
	}

	// And different prompts produce different answers - without that, the
	// determinism above could mean "always the same answer whatever the
	// prompt", which proves nothing.
	other := request()
	other.Prompt = "explain something else"
	third, err := llm.NewFake().Generate(ctx, other)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if third.Text == first.Text {
		t.Fatal("a different prompt produced the same answer")
	}
}

// TestTheFakeAnswersJSON keeps the answer shape the same as the real one.
func TestTheFakeAnswersJSON(t *testing.T) {
	got, err := llm.NewFake().Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(got.Text), &decoded); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}
	if decoded["prompt_version"] != "personalization@1" {
		t.Fatalf("the answer carries prompt_version %v, want personalization@1", decoded["prompt_version"])
	}
	if got.PromptVersion != "personalization@1" {
		t.Fatalf("the response carries prompt version %q", got.PromptVersion)
	}
}

// TestARequestWithoutAPromptVersionIsRefused guards F3-09 at its source.
func TestARequestWithoutAPromptVersionIsRefused(t *testing.T) {
	req := request()
	req.PromptVersion = ""

	if _, err := llm.NewFake().Generate(context.Background(), req); err == nil {
		t.Fatal("a request with no prompt version was accepted")
	}
}

// TestAnEmptyPromptIsRefused guards against a wasted request.
func TestAnEmptyPromptIsRefused(t *testing.T) {
	req := request()
	req.Prompt = "   \n\t "

	if _, err := llm.NewFake().Generate(context.Background(), req); err == nil {
		t.Fatal("a blank prompt was accepted")
	}
}

// TestAnOversizedAnswerIsRefused menjaga batas ukuran keluaran.
func TestAnOversizedAnswerIsRefused(t *testing.T) {
	fake := llm.NewFake()
	fake.Answer = strings.Repeat("x", 100)

	req := request()
	req.MaxOutputBytes = 50

	_, err := fake.Generate(context.Background(), req)
	if !errors.Is(err, llm.ErrTruncated) {
		t.Fatalf("Generate returned %v, want ErrTruncated", err)
	}
}

// TestACancelledContextStopsTheCall keeps the worker stoppable.
func TestACancelledContextStopsTheCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := llm.NewFake().Generate(ctx, request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate returned %v, want context.Canceled", err)
	}
}

// TestTheFakeRecordsWhatItWasAsked lets other tests inspect the prompt.
func TestTheFakeRecordsWhatItWasAsked(t *testing.T) {
	fake := llm.NewFake()
	ctx := context.Background()

	for range 3 {
		if _, err := fake.Generate(ctx, request()); err != nil {
			t.Fatalf("Generate: %v", err)
		}
	}

	if fake.CallCount() != 3 {
		t.Fatalf("the fake recorded %d calls, want 3", fake.CallCount())
	}

	calls := fake.Calls()
	calls[0].Prompt = "tampered"
	if fake.Calls()[0].Prompt == "tampered" {
		t.Fatal("Calls handed out the live slice; a caller can rewrite the record")
	}
}

// TestAFailingProviderIsReported keeps the failure path testable.
func TestAFailingProviderIsReported(t *testing.T) {
	fake := llm.NewFake()
	fake.Err = llm.ErrRateLimited

	if _, err := fake.Generate(context.Background(), request()); !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("Generate returned %v, want ErrRateLimited", err)
	}
}

// TestATruncatedAnswerIsVisible keeps a half-finished report from being
// stored as a whole one.
func TestATruncatedAnswerIsVisible(t *testing.T) {
	fake := llm.NewFake()
	fake.FinishReason = "MAX_TOKENS"

	got, err := fake.Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !got.Truncated() {
		t.Fatal("an answer that stopped at MAX_TOKENS reports itself as complete")
	}

	fake.FinishReason = "STOP"
	got, err = fake.Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Truncated() {
		t.Fatal("an answer that stopped normally reports itself as truncated")
	}
}

// TestTheFakeMatchesTheShapeItsPromptAsksFor closes a gap found by running it.
//
// The first version of the fake provider returned the SAME shape for every
// template. The consequence was real: the coaching consumer tells a curriculum
// from a graduation report by the presence of "weeks", and a fake answer with
// neither was stored as a report - the program kept waiting for its curriculum
// forever.
func TestTheFakeMatchesTheShapeItsPromptAsksFor(t *testing.T) {
	cases := map[string][]string{
		"curriculum@1": {"program_title", "weeks"},
		"graduation@1": {"headline", "completion", "next_step"},
		"chat_reply@1": {"text", "suggestions"},
	}

	for version, keys := range cases {
		req := request()
		req.PromptVersion = version

		got, err := llm.NewFake().Generate(context.Background(), req)
		if err != nil {
			t.Errorf("Generate for %s: %v", version, err)
			continue
		}

		var decoded map[string]any
		if err := json.Unmarshal([]byte(got.Text), &decoded); err != nil {
			t.Errorf("%s did not answer with JSON: %v", version, err)
			continue
		}
		for _, key := range keys {
			if _, ok := decoded[key]; !ok {
				t.Errorf("%s answered without %q: %v", version, key, decoded)
			}
		}
	}

	// And the curriculum really holds consecutively numbered weeks with
	// readable dates - a skeleton that breaks its own prompt's rules proves
	// nothing.
	req := request()
	req.PromptVersion = "curriculum@1"

	got, err := llm.NewFake().Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var curriculum struct {
		Weeks []struct {
			WeekNumber int `json:"week_number"`
			Tasks      []struct {
				TaskDate string `json:"task_date"`
			} `json:"tasks"`
		} `json:"weeks"`
	}
	if err := json.Unmarshal([]byte(got.Text), &curriculum); err != nil {
		t.Fatalf("the curriculum is not readable: %v", err)
	}
	if len(curriculum.Weeks) != 4 {
		t.Fatalf("the fake curriculum has %d weeks, want 4", len(curriculum.Weeks))
	}

	var previous time.Time
	for i, w := range curriculum.Weeks {
		if w.WeekNumber != i+1 {
			t.Fatalf("week at position %d is numbered %d", i, w.WeekNumber)
		}
		for _, task := range w.Tasks {
			date, err := time.Parse(time.DateOnly, task.TaskDate)
			if err != nil {
				t.Fatalf("a task date is unreadable: %v", err)
			}
			if !previous.IsZero() && !date.After(previous) {
				t.Fatalf("task dates are not strictly increasing: %s after %s",
					task.TaskDate, previous.Format(time.DateOnly))
			}
			previous = date
		}
	}
}

// Two disruption modes for chaos F9-14. Both belong to the fake provider so
// "slow Gemini" and "failing Gemini" can be played without Gemini.

func TestTheFakeCanBeSlowAndStillHonoursCancellation(t *testing.T) {
	fake := llm.NewFake()
	fake.Delay = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := fake.Generate(ctx, request())
	if err == nil {
		t.Fatal("a slow fake must give up when the context expires, not answer after it")
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("the fake slept the full delay (%s) instead of stopping at cancellation", elapsed)
	}

	// Given enough time, the answer comes - after the delay.
	fake.Delay = 50 * time.Millisecond
	started = time.Now()
	if _, err := fake.Generate(context.Background(), request()); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("the answer came after %s, before the configured delay", elapsed)
	}
}

func TestTheFakeCanFailTheFirstCalls(t *testing.T) {
	fake := llm.NewFake()
	fake.FailFirst = 2

	for i := 1; i <= 2; i++ {
		if _, err := fake.Generate(context.Background(), request()); err == nil {
			t.Fatalf("call %d should have failed", i)
		}
	}
	if _, err := fake.Generate(context.Background(), request()); err != nil {
		t.Fatalf("call 3 should have succeeded, got: %v", err)
	}
	if got := fake.CallCount(); got != 3 {
		t.Fatalf("failed calls must still be counted as calls, got %d", got)
	}
}
