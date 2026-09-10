package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Fake is the provider every test uses.
//
// It exists so `go test ./...` never touches the network (R6). Tests that call
// a real provider are slow, cost money, fail when the internet is down, and
// change their result from one run to the next - a test whose result changes
// proves nothing.
//
// Its answers are deterministic: the same prompt always yields the same answer.
// That is what lets an idempotency test tell "done once" from "done twice with
// a coincidentally equal result".
type Fake struct {
	mu sync.Mutex

	// Answer, when set, is used as-is. Empty means a derived, deterministic
	// answer.
	Answer string

	// Err, when set, is returned instead of an answer. It is what makes the
	// failure path testable without shutting anything down.
	Err error

	// FinishReason defaults to "stop". Set otherwise to test a truncated
	// answer.
	FinishReason string

	// Model is the name reported as the responder.
	Model string

	// Delay holds every answer for this duration, while still honouring ctx
	// cancellation. The "slow Gemini" mode for chaos F9-14.
	Delay time.Duration

	// FailFirst makes that many first calls fail, and the rest succeed. The
	// "Gemini fails occasionally" mode: what is tested is the worker's retry
	// path, not merely the failure path.
	FailFirst int

	calls []Request
}

// NewFake creates a fake provider with sensible defaults.
func NewFake() *Fake {
	return &Fake{FinishReason: "stop", Model: "fake-1"}
}

var _ Provider = (*Fake)(nil)

func (f *Fake) Name() string { return "fake" }

// Generate answers without touching anything outside this process.
func (f *Fake) Generate(ctx context.Context, req Request) (*Response, error) {
	// ctx is still honoured even without a network. A fake provider that
	// ignores cancellation would hide a worker that cannot be stopped, and
	// that is exactly what is meant to be tested.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.calls = append(f.calls, req)
	call := len(f.calls)
	err := f.Err
	if err == nil && call <= f.FailFirst {
		err = fmt.Errorf("fake provider fault: call %d of the first %d fails", call, f.FailFirst)
	}
	delay := f.Delay
	answer := f.Answer
	finish := f.FinishReason
	model := f.Model
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	if err != nil {
		return nil, err
	}
	if answer == "" {
		answer = deterministicAnswer(req)
	}
	if len(answer) > req.Limit() {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d byte limit",
			ErrTruncated, len(answer), req.Limit())
	}

	return &Response{
		Text:          answer,
		Model:         model,
		PromptVersion: req.PromptVersion,
		FinishReason:  finish,
	}, nil
}

// Calls returns a copy of the requests received so far.
//
// A copy, not the original slice: returning the original would let the caller
// mutate a record another goroutine is writing.
func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.calls...)
}

// CallCount is the number of requests received so far.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// deterministicAnswer derives an answer from the prompt.
//
// Its shape follows the SHAPE THE PROMPT ASKS FOR, not one shape for all. The
// reason was found by running it: the first version always returned the same
// shape, so the curriculum coaching-svc asked for was stored as a graduation
// report - its consumer tells the two apart by the presence of "weeks", and
// that fake answer had neither.
//
// A fake answer whose shape differs from the real one makes tests pass against
// something that never happens in production.
func deterministicAnswer(req Request) string {
	sum := sha256.Sum256([]byte(req.System + "\x1f" + req.Prompt))
	digest := hex.EncodeToString(sum[:])

	payload := shapeFor(req.PromptVersion, digest)
	payload["generated_by"] = "fake"
	payload["prompt_version"] = req.PromptVersion
	payload["digest"] = digest

	// Marshalling a map[string]any with fixed keys cannot fail, but the error
	// is still not ignored: ignoring it would let an empty answer pass as a
	// valid one.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("{%q:%q}", "error", err.Error())
	}
	return string(encoded)
}

// shapeFor produces an answer skeleton matching the template that asked for
// it.
//
// It is recognised from PromptVersion, which has the form "<template
// name>@<version>". The content is deliberately minimal but CORRECTLY SHAPED:
// what the end-to-end paths test is whether the result can be read and
// stored, not whether its content is clinically meaningful.
func shapeFor(promptVersion, digest string) map[string]any {
	name, _, _ := strings.Cut(promptVersion, "@")

	switch name {
	case "curriculum":
		return map[string]any{
			"program_title":       "Program Palsu untuk Pengujian",
			"program_description": "Kurikulum yang dihasilkan penyedia palsu.",
			"weeks":               fakeWeeks(),
		}

	case "graduation":
		return map[string]any{
			"headline": "Program selesai",
			"summary":  "Ringkasan yang dihasilkan penyedia palsu.",
			"completion": map[string]any{
				"total": 0, "completed": 0, "note": "angka dari penyedia palsu",
			},
			"what_went_well":        []string{"memulai program"},
			"what_to_carry_forward": []string{"kebiasaan harian"},
			"next_step":             "lanjutkan pekan berikutnya",
		}

	case "daily_guide":
		return map[string]any{
			"suggestions": []map[string]any{
				{
					"dish_name":     "Sayur asem dari penyedia palsu",
					"pro_tip":       "Saran praktis yang dihasilkan penyedia palsu.",
					"health_reason": "Alasan kesehatan yang dihasilkan penyedia palsu.",
				},
			},
		}

	case "chat_reply":
		return map[string]any{
			"text":        "Balasan yang dihasilkan penyedia palsu.",
			"suggestions": []string{},
		}

	default:
		// Personalisation and anything not yet recognised: the old shape, which
		// is enough to prove the answer arrives and is stored.
		return map[string]any{}
	}
}

// fakeWeeks produces four weeks holding one main mission per day.
//
// The dates run consecutively without gaps, as the prompt asks: the curriculum
// reader refuses unreadable dates, and a skeleton that breaks its own rules
// proves nothing.
func fakeWeeks() []map[string]any {
	start := time.Now().Truncate(24 * time.Hour)

	weeks := make([]map[string]any, 0, 4)
	day := 0
	for w := 1; w <= 4; w++ {
		tasks := make([]map[string]any, 0, 7)
		for range 7 {
			tasks = append(tasks, map[string]any{
				"task_date": start.AddDate(0, 0, day).Format(time.DateOnly),
				"main_mission": map[string]any{
					"task_type":   "main_mission",
					"title":       "Jalan kaki 20 menit",
					"description": "Tugas yang dihasilkan penyedia palsu.",
				},
				"bonus_challenges": []any{},
			})
			day++
		}
		weeks = append(weeks, map[string]any{
			"week_number": w,
			"title":       fmt.Sprintf("Pekan %d", w),
			"description": "Pekan yang dihasilkan penyedia palsu.",
			"tasks":       tasks,
		})
	}
	return weeks
}

// SetErr replaces the error returned by every subsequent call.
//
// It exists for tests that change the provider's fate WHILE the worker is
// running (quota exhausted, then recovered); writing f.Err directly from
// another goroutine is a data race that -race catches in CI.
func (f *Fake) SetErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Err = err
}
