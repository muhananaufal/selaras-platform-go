package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

// Bentuk kawat Generative Language API. Hanya bagian yang benar-benar dipakai
// yang dimodelkan: memodelkan seluruh respons berarti setiap bidang baru di
// sisi Google menjadi urusan repo ini.
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

	// PromptFeedback membawa alasan saat permintaannya sendiri ditolak filter.
	// Tanpa membacanya, penolakan itu terlihat seperti jawaban kosong biasa.
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`

	ModelVersion string `json:"modelVersion"`
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
		// Pointer, bukan nilai: temperature 0 yang dikirim eksplisit berarti
		// "sepenuhnya deterministik", yang berbeda dari "pakai bawaan
		// penyedia". Bidang yang dihilangkan menyampaikan yang kedua.
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

// decode membaca jawaban penyedia menjadi bentuk yang dipakai sistem.
func decode(raw []byte, req llm.Request) (*llm.Response, error) {
	var parsed generateResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("the gemini answer is not the shape we expect: %w", err)
	}

	if parsed.PromptFeedback.BlockReason != "" {
		// Permintaan yang diblokir filter bukan kegagalan jaringan. Mengulang
		// permintaan yang sama akan diblokir dengan cara yang sama.
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
	}, nil
}

// stripFence membuang pembungkus markdown yang kadang disisipkan model.
//
// Sistem lama melakukan hal yang sama [GeminiReportService.php:325-327]. Ia
// tetap perlu meski response_mime_type sudah diminta: permintaan itu tidak
// selalu dihormati, dan JSON yang terbungkus tiga backtick akan gagal di-parse
// oleh siapa pun yang menerimanya.
func stripFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}

	// Baris pertama dibuang seluruhnya - ia memuat pagar beserta label
	// bahasanya, yang bisa "json", "JSON", atau tidak ada sama sekali.
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
