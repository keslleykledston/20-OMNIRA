// Package adapters holds concrete, provider-specific implementations of
// internal/ai/ports.TextGenerator. OpenAI-specific request/response shapes
// live ONLY here — never in internal/ai/application or any inbox/domain
// package (PRODUCT.7C0 §2).
//
// Deliberately implemented with net/http directly, not the official
// github.com/openai/openai-go SDK: every other external-provider connector
// in this repository (K3GCRMClient, K3GTicketingConnector, the WAHA client)
// follows this same convention — a small, auditable HTTP client, never a
// vendor SDK — and a hand-built request is the only way to prove exactly
// (in tests, without a real network call) which fields are and are not
// sent, which this feature's design gate (PRODUCT.7C0) requires repeatedly.
// A vendor SDK would also risk built-in automatic retries this feature's
// contract explicitly forbids (PRODUCT.7C1 §12).
package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/ai/ports"
)

// Sentinel errors the HTTP adapter (internal/ai/adapters/http.go) classifies
// into truthful application-level responses (PRODUCT.7C1 §14) — never a raw
// provider status code or body surfaced to the browser.
var (
	ErrOpenAINotConfigured     = errors.New("openai: generator is not configured")
	ErrOpenAIUnauthorized      = errors.New("openai: credential rejected")
	ErrOpenAIRateLimited       = errors.New("openai: rate limited")
	ErrOpenAIUnavailable       = errors.New("openai: provider unavailable")
	ErrOpenAITimeout           = errors.New("openai: request timed out")
	ErrOpenAIMalformedResponse = errors.New("openai: unexpected response shape")
)

// OpenAIConfig mirrors the shape already used by this repository's other
// provider configs (K3GCRMConfig/K3GTicketingConfig): BaseURL/APIKey/Model/
// Timeout.
type OpenAIConfig struct {
	BaseURL string // default https://api.openai.com/v1
	APIKey  string
	Model   string
	Timeout time.Duration
}

// OpenAIGenerator implements ports.TextGenerator against OpenAI's Responses
// API (POST /responses). Every request explicitly sets store=false (PRODUCT.
// 7C0 §5/§16: no provider-side conversation state, no training/retention
// reliance on a default), background=false (synchronous, PRODUCT.7C0 §17),
// an empty tools array (no tool calling, PRODUCT.7B3 §6/§8), and a bounded
// max_output_tokens — never left to provider defaults.
type OpenAIGenerator struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewOpenAIGenerator(cfg OpenAIConfig) (*OpenAIGenerator, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, ErrOpenAINotConfigured
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	// A plain http.Client performs no automatic retries of any kind — this
	// is a property of the standard library, not a setting to disable, and
	// is exactly the zero-retry contract PRODUCT.7C1 §12 requires.
	return &OpenAIGenerator{
		baseURL: base,
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

// openAIRequest is the exact wire shape sent to /responses. Every field the
// design gate requires is explicit here, never a struct-literal omission
// that could silently rely on a provider default.
type openAIRequest struct {
	Model           string           `json:"model"`
	Input           string           `json:"input"`
	Instructions    string           `json:"instructions"`
	Store           bool             `json:"store"`
	Background      bool             `json:"background"`
	MaxOutputTokens int              `json:"max_output_tokens"`
	Reasoning       *reasoningWire   `json:"reasoning,omitempty"`
	Tools           []map[string]any `json:"tools"`
}

// reasoningWire.Effort is set to "none" per this feature's explicit design
// decision (PRODUCT.7C1). NOTE: this value has not been exercised against
// the real OpenAI API by this implementation slice (no real call is made) —
// whether "none" is accepted for the configured model must be confirmed
// during activation (PRODUCT.7C0 §16/§19), not assumed here.
type reasoningWire struct {
	Effort string `json:"effort"`
}

type openAIResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (g *OpenAIGenerator) Generate(ctx context.Context, req ports.GenerateRequest) (ports.GenerateResponse, error) {
	if g == nil || g.client == nil {
		return ports.GenerateResponse{}, ErrOpenAINotConfigured
	}
	wire := openAIRequest{
		Model:           g.model,
		Input:           req.Input,
		Instructions:    req.Instructions,
		Store:           false,
		Background:      false,
		MaxOutputTokens: req.MaxOutputTokens,
		Reasoning:       &reasoningWire{Effort: "none"},
		Tools:           []map[string]any{},
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: encode request: %v", ErrOpenAIMalformedResponse, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: build request: %v", ErrOpenAIMalformedResponse, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+g.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := g.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return ports.GenerateResponse{}, ErrOpenAITimeout
		}
		return ports.GenerateResponse{}, fmt.Errorf("%w: %v", ErrOpenAIUnavailable, err)
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: read response: %v", ErrOpenAIUnavailable, err)
	}

	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ports.GenerateResponse{}, ErrOpenAIUnauthorized
	case res.StatusCode == http.StatusTooManyRequests:
		return ports.GenerateResponse{}, ErrOpenAIRateLimited
	case res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusGatewayTimeout:
		return ports.GenerateResponse{}, ErrOpenAITimeout
	case res.StatusCode >= 500:
		return ports.GenerateResponse{}, ErrOpenAIUnavailable
	case res.StatusCode >= 400:
		return ports.GenerateResponse{}, fmt.Errorf("%w: status %d", ErrOpenAIMalformedResponse, res.StatusCode)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: decode response", ErrOpenAIMalformedResponse)
	}
	if parsed.Error != nil {
		return ports.GenerateResponse{}, fmt.Errorf("%w: %s", ErrOpenAIUnavailable, parsed.Error.Code)
	}
	var inTok, outTok int
	if parsed.Usage != nil {
		inTok, outTok = parsed.Usage.InputTokens, parsed.Usage.OutputTokens
	}
	for _, item := range parsed.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
				return ports.GenerateResponse{OutputText: strings.TrimSpace(c.Text), InputTokens: inTok, OutputTokens: outTok}, nil
			}
		}
	}
	return ports.GenerateResponse{}, fmt.Errorf("%w: no output_text in response", ErrOpenAIMalformedResponse)
}
