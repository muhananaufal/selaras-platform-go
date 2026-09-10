package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

// The wire shape of the Generative Language API. Only the parts actually used
// are modelled: modelling the whole response means every new field on
// Google's side becomes this repo's business.
type generateRequest struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text"`
}

type generationConfig struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	ResponseMIMEType string   `json:"response_mime_type,omitempty"`
}

type generateResponse struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`

	// PromptFeedback carries the reason when the request itself is refused by
	// the filter. Without reading it, that refusal looks like an ordinary
	// empty answer.
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`

	ModelVersion string `json:"modelVersion"`

	// UsageMetadata: the field names were verified against a real
	// gemini-3.8-flash answer on 2026-09-07, not from memory.
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

func buildRequest(req llm.Request) generateRequest {
	out := generateRequest{
		Contents: []content{{Parts: []part{{Text: req.Prompt}}}},
	}
	if req.System != "" {
		out.SystemInstruction = &content{Parts: []part{{Text: req.System}}}
	}

	cfg := generationConfig{}
	var wanted bool
	if req.Temperature > 0 {
		// A pointer, not a value: an explicitly sent temperature of 0 means
		// "fully deterministic", which differs from "use the provider default".
		// An omitted field conveys the latter.
		t := req.Temperature
		cfg.Temperature = &t
		wanted = true
	}
	if req.JSON {
		cfg.ResponseMIMEType = "application/json"
		wanted = true
	}
	if wanted {
		out.GenerationConfig = &cfg
	}
	return out
}

// decode reads the provider's answer into the shape the system uses.
func decode(raw []byte, req llm.Request) (*llm.Response, error) {
	var parsed generateResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("the gemini answer is not the shape we expect: %w", err)
	}

	if parsed.PromptFeedback.BlockReason != "" {
		// A request blocked by the filter is not a network failure. Repeating the
		// same request will be blocked the same way.
		return nil, fmt.Errorf("gemini blocked the prompt: %s", parsed.PromptFeedback.BlockReason)
	}
	if len(parsed.Candidates) == 0 {
		return nil, llm.ErrEmptyAnswer
	}

	candidate := parsed.Candidates[0]

	var text strings.Builder
	for _, p := range candidate.Content.Parts {
		text.WriteString(p.Text)
	}

	answer := stripFence(text.String())
	if strings.TrimSpace(answer) == "" {
		return nil, llm.ErrEmptyAnswer
	}

	model := parsed.ModelVersion
	if model == "" {
		model = "unknown"
	}

	return &llm.Response{
		Text:          answer,
		Model:         model,
		PromptVersion: req.PromptVersion,
		FinishReason:  candidate.FinishReason,
		Usage: llm.Usage{
			InputTokens:    parsed.UsageMetadata.PromptTokenCount,
			OutputTokens:   parsed.UsageMetadata.CandidatesTokenCount,
			ThoughtsTokens: parsed.UsageMetadata.ThoughtsTokenCount,
		},
	}, nil
}

// stripFence removes the markdown wrapper the model sometimes inserts.
//
// The legacy system did the same [GeminiReportService.php:325-327]. It is
// still needed even though response_mime_type is requested: that request is
// not always honoured, and JSON wrapped in three backticks fails to parse for
// whoever receives it.
func stripFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}

	// The first line is dropped entirely - it holds the fence together with
	// its language label, which may be "json", "JSON", or absent altogether.
	if nl := strings.IndexByte(trimmed, '\n'); nl >= 0 {
		trimmed = trimmed[nl+1:]
	} else {
		return trimmed
	}

	if end := strings.LastIndex(trimmed, "```"); end >= 0 {
		trimmed = trimmed[:end]
	}
	return strings.TrimSpace(trimmed)
}
