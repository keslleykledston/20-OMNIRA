package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/ai/ports"
)

// TestGeminiLiveSmoke makes ONE tiny real call (a few tokens, no customer data). It runs only when
// OMNIRA_GEMINI_LIVE_TEST_KEY is set, so CI and the normal suite never reach the network.
func TestGeminiLiveSmoke(t *testing.T) {
	key := os.Getenv("OMNIRA_GEMINI_LIVE_TEST_KEY")
	if key == "" {
		t.Skip("OMNIRA_GEMINI_LIVE_TEST_KEY not set")
	}
	model := os.Getenv("OMNIRA_GEMINI_LIVE_TEST_MODEL")
	if model == "" {
		model = "gemini-3.5-flash-lite"
	}
	g, err := NewGeminiGenerator(GeminiConfig{APIKey: key, Model: model, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := g.Generate(context.Background(), ports.GenerateRequest{
		Instructions: "Você resume em uma frase curta, em português.", Input: "CUSTOMER: meu boleto venceu ontem, quero a segunda via.\nAGENT: enviei por e-mail.", MaxOutputTokens: 200})
	if err != nil {
		t.Fatalf("live call: %v", err)
	}
	if resp.OutputText == "" || resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Fatalf("unexpected live response: %+v", resp)
	}
	t.Logf("model=%s in=%d out=%d text=%q", model, resp.InputTokens, resp.OutputTokens, resp.OutputText)
}
