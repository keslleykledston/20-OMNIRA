package config

import "testing"

func TestAIReadyAcceptsOpenAIAndGeminiOnly(t *testing.T) {
	for _, tc := range []struct {
		provider string
		want     bool
	}{{"openai", true}, {"gemini", true}, {"anthropic", false}, {"", false}} {
		c := &Config{AIEnabled: true, AIProvider: tc.provider, AIModel: "m", AIAPIKey: "k"}
		if got := c.AIReady(); got != tc.want {
			t.Fatalf("provider %q: AIReady = %v, want %v", tc.provider, got, tc.want)
		}
	}
	for name, c := range map[string]*Config{
		"disabled": {AIEnabled: false, AIProvider: "gemini", AIModel: "m", AIAPIKey: "k"},
		"no model": {AIEnabled: true, AIProvider: "gemini", AIAPIKey: "k"},
		"no key":   {AIEnabled: true, AIProvider: "gemini", AIModel: "m"},
	} {
		if c.AIReady() {
			t.Fatalf("%s must not be ready", name)
		}
	}
}

func TestGeminiKeyFallbackOnlyForGeminiAndNeverOverridesTheExplicitKey(t *testing.T) {
	t.Setenv("OMNIRA_GEMINI_API_KEY", "gem-key")
	t.Setenv("OMNIRA_AI_API_KEY", "")
	t.Setenv("OMNIRA_AI_PROVIDER", "gemini")
	if got := Load().AIAPIKey; got != "gem-key" {
		t.Fatalf("gemini fallback key = %q", got)
	}
	t.Setenv("OMNIRA_AI_PROVIDER", " Gemini ")
	if c := Load(); c.AIAPIKey != "gem-key" || c.AIProvider != "gemini" {
		t.Fatalf("provider/key not normalised: %q %q", c.AIProvider, c.AIAPIKey)
	}
	t.Setenv("OMNIRA_AI_API_KEY", "explicit")
	if got := Load().AIAPIKey; got != "explicit" {
		t.Fatalf("explicit key must win, got %q", got)
	}
	// the Gemini key must never be used as the OpenAI key
	t.Setenv("OMNIRA_AI_API_KEY", "")
	t.Setenv("OMNIRA_AI_PROVIDER", "openai")
	if got := Load().AIAPIKey; got != "" {
		t.Fatalf("openai must not borrow the gemini key, got %q", got)
	}
	t.Setenv("OMNIRA_AI_PROVIDER", "")
	if got := Load().AIAPIKey; got != "" {
		t.Fatalf("default provider (openai) must not borrow the gemini key, got %q", got)
	}
}
