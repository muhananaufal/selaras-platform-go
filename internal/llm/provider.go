// Package llm is the boundary between this system and the language model
// provider.
//
// That boundary exists for one practical reason: providers change, cost
// money, and cannot be called from tests. What lives in here is only the
// shape of a request and its answer; what knows how to speak HTTP to Google
// lives in a child package, and this package must NOT import it - guarded by
// boundary_test.go.
package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Request is one request to the model.
type Request struct {
	// System is the role instruction. It is separated from Prompt because
	// providers treat it differently, and merging the two into one string
	// would throw that difference away.
	System string

	Prompt string

	// PromptVersion is the version of the template that produced Prompt.
	//
	// It is required. A stored result without its prompt version cannot be
	// explained once the prompt changes: there is no way to know whether a
	// strange answer came from the model or from a template replaced since
	// (F3-09).
	PromptVersion string

	// A Temperature of 0 means "use the provider default". Negative values are
	// refused.
	Temperature float64

	// MaxOutputBytes bounds the size of an answer we are willing to accept.
	//
	// Without a bound, one rambling answer could fill the worker's memory and
	// its database column. Zero means DefaultMaxOutputBytes.
	MaxOutputBytes int

	// JSON asks for the answer as JSON. Providers that support it are asked to
	// enforce it; for those that do not, the answer is still checked on this
	// side.
	JSON bool
}

// DefaultMaxOutputBytes is the default bound on answer size.
//
// The longest personalisation report in the legacy system was far below this;
// the number is chosen loosely so a valid answer is never cut, but still
// bounded so a runaway answer does not exhaust memory.
const DefaultMaxOutputBytes = 256 * 1024

// Response is the model's answer together with what has to be recorded
// alongside it.
type Response struct {
	Text string

	// Model is the name of the model that actually answered, as reported by the
	// provider - not the name requested. The two can differ when the provider
	// reroutes a request, and what has to be recorded is the one that answered.
	Model string

	// PromptVersion is carried back from the request so callers need not pair
	// it up themselves.
	PromptVersion string

	// FinishReason says why the model stopped. An answer cut off by the token
	// limit is not a finished answer, and telling them apart keeps a
	// half-finished report from being stored as a whole one.
	FinishReason string

	// Usage is the tokens the provider reported for this answer. Zero means
	// the provider reported nothing (the fake provider), NOT free; FinOps
	// tells the two apart through the provider name.
	Usage Usage
}

// Usage is the token count of one call, as reported by the provider.
//
// Reported, not estimated: the bytes/4 estimate in docs/finops.md is what
// these numbers replace. Thoughts are separate because thinking models (Gemini
// 3.x) bill them as output even though their text never arrives.
type Usage struct {
	InputTokens    int
	OutputTokens   int
	ThoughtsTokens int
}

// Total is every token billed for that call.
func (u Usage) Total() int {
	return u.InputTokens + u.OutputTokens + u.ThoughtsTokens
}

// Truncated says the answer was cut off.
func (r *Response) Truncated() bool {
	return !strings.EqualFold(r.FinishReason, "stop") && r.FinishReason != ""
}

// Provider is what the system needs from a language model.
//
// This narrow, deliberately. An interface mirroring the provider's full
// capabilities would lock the system to that provider's shape, and the next
// provider would not fit.
type Provider interface {
	// Name is the provider name for logs and metrics.
	Name() string

	// Generate asks for one answer.
	//
	// It MUST honour ctx: an LLM job waits on the network for tens of seconds,
	// and a worker that cannot be stopped in the middle of it holds shutdown
	// until the forced timeout.
	Generate(ctx context.Context, req Request) (*Response, error)
}

// Errors that callers recognise.
var (
	// ErrRateLimited means the provider refused temporarily on quota grounds.
	// It is worth retrying, and that is why it is distinguished from other
	// errors.
	ErrRateLimited = errors.New("the provider is rate limiting us")

	// ErrTruncated means the answer exceeded the requested limit.
	ErrTruncated = errors.New("the answer was cut off")

	// ErrEmptyAnswer means the provider answered with no content. It is not a
	// network failure, so retrying is usually pointless - but storing it as an
	// answer is far worse.
	ErrEmptyAnswer = errors.New("the provider answered with nothing")
)

// Validate checks a request before it is sent anywhere.
//
// It lives here, not in each adapter, so a new provider cannot silently
// loosen the rules.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("an empty prompt would spend a request on nothing")
	}
	if r.PromptVersion == "" {
		return errors.New("a result without its prompt version cannot be explained later")
	}
	if r.Temperature < 0 {
		return fmt.Errorf("temperature %v is below zero", r.Temperature)
	}
	if r.MaxOutputBytes < 0 {
		return fmt.Errorf("max output %d is below zero", r.MaxOutputBytes)
	}
	return nil
}

// Limit returns the size bound in effect.
func (r Request) Limit() int {
	if r.MaxOutputBytes <= 0 {
		return DefaultMaxOutputBytes
	}
	return r.MaxOutputBytes
}
