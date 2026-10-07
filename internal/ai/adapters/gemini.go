package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/ai/ports"
)

// ErrGeminiBlocked is returned when Gemini refuses the prompt or the answer (safety block). It is not a provider outage:
// retrying the same input would be refused again.
var ErrGeminiBlocked = errors.New("gemini: the request or the answer was blocked by the provider")

// GeminiConfig mirrors OpenAIConfig: BaseURL/APIKey/Model/Timeout.
type GeminiConfig struct {
	BaseURL string // default https://generativelanguage.googleapis.com
	APIKey  string
	Model   string
	Timeout time.Duration
}

// GeminiGenerator implements ports.TextGenerator against the Gemini API (POST /v1beta/models/{model}:generateContent).
// Same contract as the OpenAI adapter: net/http only, no automatic retry, bounded output, no tools, and nothing from the
// provider's answer other than the text and the token counters crosses the port. The key travels only in the
// x-goog-api-key header (never in a URL, so it cannot land in a log) and redirects are never followed.
type GeminiGenerator struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

var _ ports.TextGenerator = (*GeminiGenerator)(nil)

const (
	defaultGeminiTextBase = "https://generativelanguage.googleapis.com"
	geminiMaxBody         = 4 << 20
)

// the model goes into the URL path: only a plain model name is allowed (no slash, no "..", no query).
var geminiTextModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

func NewGeminiGenerator(cfg GeminiConfig) (*GeminiGenerator, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = defaultGeminiTextBase
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrProviderNotConfigured
	}
	model := strings.TrimSpace(cfg.Model)
	if strings.TrimSpace(cfg.APIKey) == "" || !geminiTextModelPattern.MatchString(model) || strings.Contains(model, "..") {
		return nil, ErrProviderNotConfigured
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &GeminiGenerator{
		baseURL: u.Scheme + "://" + u.Host,
		apiKey:  strings.TrimSpace(cfg.APIKey),
		model:   model,
		client: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type geminiTextPart struct {
	Text string `json:"text"`
}

type geminiTextContent struct {
	Role  string           `json:"role,omitempty"`
	Parts []geminiTextPart `json:"parts"`
}

type geminiThinking struct {
	ThinkingBudget int `json:"thinkingBudget"`
}

type geminiGenConfig struct {
	MaxOutputTokens int             `json:"maxOutputTokens"`
	Temperature     float64         `json:"temperature"`
	ThinkingConfig  *geminiThinking `json:"thinkingConfig,omitempty"`
}

type geminiTextRequest struct {
	SystemInstruction geminiTextContent   `json:"systemInstruction"`
	Contents          []geminiTextContent `json:"contents"`
	GenerationConfig  geminiGenConfig     `json:"generationConfig"`
}

type geminiTextResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

// thinkingFor decides the reasoning allowance. The output limit of Gemini 2.5 INCLUDES its hidden reasoning, so with a small
// limit (a classification of 200 tokens) a thinking model could spend it all and answer nothing: Flash and Flash-Lite
// have thinking switched off, Pro cannot be switched off (minimum 128) so it gets that minimum plus the same headroom in
// the limit. Other models are left to their own defaults.
func thinkingFor(model string) (cfg *geminiThinking, headroom int) {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "gemini-2.5-flash"):
		return &geminiThinking{ThinkingBudget: 0}, 0
	case strings.HasPrefix(m, "gemini-2.5-pro"):
		return &geminiThinking{ThinkingBudget: 128}, 128
	}
	return nil, 0
}

func (g *GeminiGenerator) Generate(ctx context.Context, req ports.GenerateRequest) (ports.GenerateResponse, error) {
	if g == nil || g.client == nil {
		return ports.GenerateResponse{}, ErrProviderNotConfigured
	}
	thinking, headroom := thinkingFor(g.model)
	wire := geminiTextRequest{
		SystemInstruction: geminiTextContent{Parts: []geminiTextPart{{Text: req.Instructions}}},
		Contents:          []geminiTextContent{{Role: "user", Parts: []geminiTextPart{{Text: req.Input}}}},
		GenerationConfig:  geminiGenConfig{MaxOutputTokens: req.MaxOutputTokens + headroom, Temperature: 0, ThinkingConfig: thinking},
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: encode request: %v", ErrProviderMalformedResponse, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/v1beta/models/"+g.model+":generateContent", bytes.NewReader(body))
	if err != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: build request: %v", ErrProviderMalformedResponse, err)
	}
	httpReq.Header.Set("x-goog-api-key", g.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := g.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return ports.GenerateResponse{}, ErrProviderTimeout
		}
		// the text of a net/http error may carry the URL, never a header; still, only the cause is wrapped
		return ports.GenerateResponse{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, errors.Unwrap(err))
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(res.Body, geminiMaxBody))
	if err != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: read response", ErrProviderUnavailable)
	}

	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ports.GenerateResponse{}, ErrProviderUnauthorized
	case res.StatusCode == http.StatusTooManyRequests:
		return ports.GenerateResponse{}, ErrProviderRateLimited
	case res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusGatewayTimeout:
		return ports.GenerateResponse{}, ErrProviderTimeout
	case res.StatusCode >= 500:
		return ports.GenerateResponse{}, ErrProviderUnavailable
	case res.StatusCode != http.StatusOK:
		return ports.GenerateResponse{}, fmt.Errorf("%w: status %d", ErrProviderMalformedResponse, res.StatusCode)
	}

	var parsed geminiTextResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: decode response", ErrProviderMalformedResponse)
	}
	// usage is reported even when nothing was produced: a blocked or truncated call may still have been billed
	u := parsed.UsageMetadata
	out := ports.GenerateResponse{InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount}
	if parsed.PromptFeedback.BlockReason != "" {
		return out, ErrGeminiBlocked
	}
	if len(parsed.Candidates) == 0 {
		return out, fmt.Errorf("%w: no candidate in response", ErrProviderMalformedResponse)
	}
	var sb strings.Builder
	for _, p := range parsed.Candidates[0].Content.Parts {
		if !p.Thought {
			sb.WriteString(p.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		switch parsed.Candidates[0].FinishReason {
		case "SAFETY", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII", "RECITATION":
			return out, ErrGeminiBlocked
		}
		return out, fmt.Errorf("%w: no text in response (%s)", ErrProviderMalformedResponse, parsed.Candidates[0].FinishReason)
	}
	out.OutputText = text
	return out, nil
}
