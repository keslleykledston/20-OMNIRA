package adapters

import (
	"strings"
	"time"

	"github.com/omnira/omnira/internal/ai/ports"
)

// Provider names accepted by OMNIRA_AI_PROVIDER.
const (
	ProviderOpenAI = "openai"
	ProviderGemini = "gemini"
)

// Provider-neutral names of the errors every TextGenerator adapter returns. They are the same values the OpenAI adapter
// always returned, so callers that classify with errors.Is work for any provider.
var (
	ErrProviderNotConfigured     = ErrOpenAINotConfigured
	ErrProviderUnauthorized      = ErrOpenAIUnauthorized
	ErrProviderRateLimited       = ErrOpenAIRateLimited
	ErrProviderUnavailable       = ErrOpenAIUnavailable
	ErrProviderTimeout           = ErrOpenAITimeout
	ErrProviderMalformedResponse = ErrOpenAIMalformedResponse
)

// SupportedProvider reports whether a TextGenerator exists for the provider name.
func SupportedProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderOpenAI, ProviderGemini:
		return true
	}
	return false
}

// NewTextGenerator is the single place that turns the platform AI configuration into a generator. Every caller (summary
// endpoint, model router, flow AI nodes) goes through it, so adding a provider never means touching them.
func NewTextGenerator(provider, apiKey, model string, timeout time.Duration) (ports.TextGenerator, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderOpenAI:
		g, err := NewOpenAIGenerator(OpenAIConfig{APIKey: apiKey, Model: model, Timeout: timeout})
		if err != nil {
			return nil, err // never a typed-nil pointer inside a non-nil interface
		}
		return g, nil
	case ProviderGemini:
		g, err := NewGeminiGenerator(GeminiConfig{APIKey: apiKey, Model: model, Timeout: timeout})
		if err != nil {
			return nil, err
		}
		return g, nil
	}
	return nil, ErrProviderNotConfigured
}
