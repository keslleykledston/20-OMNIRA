package domain

import "strings"

// ModelPrice is US$ per million tokens. These are ESTIMATES used by the budget guard, not billing: the provider's
// invoice is the source of truth. An unknown model is priced like the most expensive one, so the guard errs on the safe side.
type ModelPrice struct{ InputPerM, OutputPerM float64 }

var modelPrices = map[string]ModelPrice{
	"gemini-2.5-flash-lite": {0.10, 0.40},
	"gemini-2.5-flash":      {0.30, 2.50},
	"gemini-2.5-pro":        {1.25, 10.00},
}

var unknownModelPrice = ModelPrice{1.25, 10.00}

func PriceOf(model string) ModelPrice {
	if p, ok := modelPrices[strings.ToLower(strings.TrimSpace(model))]; ok {
		return p
	}
	return unknownModelPrice
}

// EstimateCostUSD converts the provider's reported usage into dollars.
func EstimateCostUSD(model string, inputTokens, outputTokens int) float64 {
	p := PriceOf(model)
	return (float64(inputTokens)*p.InputPerM + float64(outputTokens)*p.OutputPerM) / 1_000_000
}

// WorstCaseCostUSD bounds one call before it is made: a generous input (a long PDF) plus the full output allowance.
func WorstCaseCostUSD(model string, maxOutputTokens int) float64 {
	const worstInputTokens = 30000
	return EstimateCostUSD(model, worstInputTokens, maxOutputTokens)
}

// VisionMimeAllowed: what the external model is allowed to receive. GIF and anything else stay local.
func VisionMimeAllowed(mime string) bool {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/png", "image/webp", "application/pdf":
		return true
	}
	return false
}

// VisionKindFor maps a cleared attachment to the derived-text kind ("" when it must not be sent).
func VisionKindFor(mime string) string {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/png", "image/webp":
		return "description"
	case "application/pdf":
		return "document_text"
	}
	return ""
}
