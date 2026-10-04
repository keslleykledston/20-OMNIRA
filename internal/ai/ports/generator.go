// Package ports declares the provider-neutral boundary the AI application
// layer depends on (PRODUCT.7C0/7C1). No adapter type (OpenAI request/
// response shapes, SDK types, etc.) may leak past this package into
// internal/ai/application or any inbox/domain package.
package ports

import "context"

// GenerateRequest is the provider-neutral input to a text generation call.
// Instructions is the fixed, server-owned system prompt (PRODUCT.7C0 §8/9);
// Input is untrusted conversation data, never itself an instruction.
// MaxOutputTokens bounds cost/latency; callers never omit it.
type GenerateRequest struct {
	Instructions    string
	Input           string
	MaxOutputTokens int
}

// GenerateResponse is the provider-neutral output. OutputText is the only
// field the application layer consumes — no provider response ID, no raw
// provider metadata, ever crosses this boundary.
type GenerateResponse struct {
	OutputText string
	// InputTokens / OutputTokens are the provider's own usage counters (0 when it reported none). They exist only for
	// accounting (ADR-0017 Wave 10); no other provider metadata crosses this boundary.
	InputTokens  int
	OutputTokens int
}

// TextGenerator is the single port every AI capability's application layer
// depends on. A concrete adapter (internal/ai/adapters) implements this
// against one real provider; the application layer never imports a vendor
// SDK or constructs a vendor-specific request directly (PRODUCT.7C0 §2).
type TextGenerator interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
}
