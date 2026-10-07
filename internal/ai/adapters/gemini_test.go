package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/ai/ports"
)

type geminiCapture struct {
	body   []byte
	path   string
	rawURL string
	header http.Header
	hits   int
}

func geminiServer(t *testing.T, status int, body string) (*httptest.Server, *geminiCapture) {
	t.Helper()
	c := &geminiCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits++
		c.body, _ = io.ReadAll(r.Body)
		c.path, c.rawURL, c.header = r.URL.Path, r.URL.String(), r.Header.Clone()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

const geminiOK = `{"candidates":[{"content":{"parts":[{"text":" resumo gerado "}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":15,"thoughtsTokenCount":5}}`

func newGem(t *testing.T, base, model string) *GeminiGenerator {
	t.Helper()
	g, err := NewGeminiGenerator(GeminiConfig{BaseURL: base, APIKey: "k-secret-do-not-log-0123456789", Model: model, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("NewGeminiGenerator: %v", err)
	}
	return g
}

func TestGeminiRequestShapeAndKeyOnlyInTheHeader(t *testing.T) {
	srv, c := geminiServer(t, 200, geminiOK)
	g := newGem(t, srv.URL, "gemini-2.5-flash")
	resp, err := g.Generate(context.Background(), ports.GenerateRequest{Instructions: "regra fixa", Input: "CUSTOMER: oi", MaxOutputTokens: 300})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.OutputText != "resumo gerado" || resp.InputTokens != 120 || resp.OutputTokens != 20 {
		t.Fatalf("response = %+v (thought tokens must count as output)", resp)
	}
	if c.path != "/v1beta/models/gemini-2.5-flash:generateContent" {
		t.Fatalf("path = %q", c.path)
	}
	if c.header.Get("x-goog-api-key") != "k-secret-do-not-log-0123456789" {
		t.Fatalf("key header missing")
	}
	if strings.Contains(c.rawURL, "k-secret") || strings.Contains(string(c.body), "k-secret") || c.header.Get("Authorization") != "" {
		t.Fatalf("the key must travel only in x-goog-api-key (url=%q)", c.rawURL)
	}
	var req map[string]any
	if err := json.Unmarshal(c.body, &req); err != nil {
		t.Fatal(err)
	}
	gc := req["generationConfig"].(map[string]any)
	if gc["maxOutputTokens"].(float64) != 300 || gc["temperature"].(float64) != 0 {
		t.Fatalf("generationConfig = %v", gc)
	}
	if tc := gc["thinkingConfig"].(map[string]any); tc["thinkingBudget"].(float64) != 0 {
		t.Fatalf("flash must have thinking off: %v", tc)
	}
	si := req["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
	if si != "regra fixa" {
		t.Fatalf("systemInstruction = %v", si)
	}
	contents := req["contents"].([]any)
	if len(contents) != 1 || contents[0].(map[string]any)["role"] != "user" || contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"] != "CUSTOMER: oi" {
		t.Fatalf("contents = %v", contents)
	}
	// nothing that lets the model act or the provider keep state
	for _, forbidden := range []string{"tools", "toolConfig", "cachedContent", "safetySettings", "responseMimeType"} {
		if _, ok := req[forbidden]; ok {
			t.Fatalf("request must not carry %q", forbidden)
		}
	}
}

func TestGeminiProThinkingHasHeadroomInTheOutputLimit(t *testing.T) {
	srv, c := geminiServer(t, 200, geminiOK)
	if _, err := newGem(t, srv.URL, "gemini-2.5-pro").Generate(context.Background(), ports.GenerateRequest{Input: "x", MaxOutputTokens: 200}); err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	_ = json.Unmarshal(c.body, &req)
	gc := req["generationConfig"].(map[string]any)
	if gc["maxOutputTokens"].(float64) != 328 || gc["thinkingConfig"].(map[string]any)["thinkingBudget"].(float64) != 128 {
		t.Fatalf("pro generationConfig = %v", gc)
	}
	srv2, c2 := geminiServer(t, 200, geminiOK)
	if _, err := newGem(t, srv2.URL, "gemini-9-future").Generate(context.Background(), ports.GenerateRequest{Input: "x", MaxOutputTokens: 200}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(c2.body), "thinkingConfig") {
		t.Fatalf("an unknown model must be left to its own defaults")
	}
}

func TestGeminiErrorMappingNeverRetriesAndNeverLeaksBody(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{401, ErrProviderUnauthorized}, {403, ErrProviderUnauthorized}, {429, ErrProviderRateLimited},
		{408, ErrProviderTimeout}, {504, ErrProviderTimeout}, {500, ErrProviderUnavailable}, {503, ErrProviderUnavailable}, {400, ErrProviderMalformedResponse},
	}
	for _, tc := range cases {
		srv, c := geminiServer(t, tc.status, `{"error":{"message":"conteudo do cliente: segredo-123"}}`)
		_, err := newGem(t, srv.URL, "gemini-2.5-flash").Generate(context.Background(), ports.GenerateRequest{Input: "x", MaxOutputTokens: 10})
		if !errors.Is(err, tc.want) {
			t.Fatalf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
		if strings.Contains(err.Error(), "segredo-123") || strings.Contains(err.Error(), "k-secret") {
			t.Fatalf("status %d: provider body or key leaked into the error: %v", tc.status, err)
		}
		if c.hits != 1 {
			t.Fatalf("status %d: %d requests, want exactly 1 (no automatic retry)", tc.status, c.hits)
		}
	}
}

func TestGeminiBlockedAndEmptyAnswers(t *testing.T) {
	cases := map[string]struct {
		body string
		want error
	}{
		"prompt blocked":     {`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":9}}`, ErrGeminiBlocked},
		"answer blocked":     {`{"candidates":[{"finishReason":"SAFETY","content":{"parts":[]}}]}`, ErrGeminiBlocked},
		"no candidates":      {`{"candidates":[]}`, ErrProviderMalformedResponse},
		"truncated no text":  {`{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[]}}]}`, ErrProviderMalformedResponse},
		"only thought parts": {`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"pensando","thought":true}]}}]}`, ErrProviderMalformedResponse},
		"not json":           {`<html>`, ErrProviderMalformedResponse},
	}
	for name, tc := range cases {
		srv, _ := geminiServer(t, 200, tc.body)
		_, err := newGem(t, srv.URL, "gemini-2.5-flash").Generate(context.Background(), ports.GenerateRequest{Input: "x", MaxOutputTokens: 10})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	// a blocked call may still have been billed: the usage comes back with the error
	srv, _ := geminiServer(t, 200, `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":9}}`)
	resp, _ := newGem(t, srv.URL, "gemini-2.5-flash").Generate(context.Background(), ports.GenerateRequest{Input: "x", MaxOutputTokens: 10})
	if resp.InputTokens != 9 || resp.OutputText != "" {
		t.Fatalf("blocked response = %+v", resp)
	}
}

func TestGeminiThoughtPartsAreNeverReturnedAsTheAnswer(t *testing.T) {
	srv, _ := geminiServer(t, 200, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"raciocinio interno","thought":true},{"text":"resposta"}]}}]}`)
	resp, err := newGem(t, srv.URL, "gemini-2.5-flash").Generate(context.Background(), ports.GenerateRequest{Input: "x", MaxOutputTokens: 10})
	if err != nil || resp.OutputText != "resposta" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestGeminiTimeoutAndRedirectsAreNotFollowed(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	g, _ := NewGeminiGenerator(GeminiConfig{BaseURL: slow.URL, APIKey: "k-secret-do-not-log-0123456789", Model: "gemini-2.5-flash", Timeout: 50 * time.Millisecond})
	if _, err := g.Generate(context.Background(), ports.GenerateRequest{Input: "x", MaxOutputTokens: 10}); err == nil {
		t.Fatal("a slow provider must fail")
	}
	target, tc := geminiServer(t, 200, geminiOK)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err := newGem(t, redirect.URL, "gemini-2.5-flash").Generate(context.Background(), ports.GenerateRequest{Input: "x", MaxOutputTokens: 10})
	if err == nil || tc.hits != 0 {
		t.Fatalf("a redirect must not be followed (err=%v, hits on target=%d): the key header would travel with it", err, tc.hits)
	}
}

func TestGeminiConstructionRejectsUnsafeConfig(t *testing.T) {
	bad := []GeminiConfig{
		{APIKey: "", Model: "gemini-2.5-flash"},
		{APIKey: "k", Model: ""},
		{APIKey: "k", Model: "../v1/admin"},
		{APIKey: "k", Model: "a/b"},
		{APIKey: "k", Model: "m?key=x"},
		{APIKey: "k", Model: "a..b"},
		{APIKey: "k", Model: "gemini-2.5-flash", BaseURL: "file:///etc"},
	}
	for _, cfg := range bad {
		if _, err := NewGeminiGenerator(cfg); !errors.Is(err, ErrProviderNotConfigured) {
			t.Fatalf("%+v: err = %v, want not configured", cfg, err)
		}
	}
}

func TestNewTextGeneratorPicksTheProviderAndNeverReturnsATypedNil(t *testing.T) {
	g, err := NewTextGenerator("Gemini", "k", "gemini-2.5-flash", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.(*GeminiGenerator); !ok {
		t.Fatalf("got %T", g)
	}
	g, err = NewTextGenerator("openai", "k", "gpt-x", time.Second)
	if _, ok := g.(*OpenAIGenerator); err != nil || !ok {
		t.Fatalf("got %T err=%v", g, err)
	}
	for _, p := range []string{"openai", "gemini"} {
		g, err := NewTextGenerator(p, "", "m", time.Second)
		if err == nil || g != nil {
			t.Fatalf("%s without a key: g=%v err=%v (must be a true nil interface)", p, g, err)
		}
	}
	if g, err := NewTextGenerator("anthropic", "k", "m", time.Second); err == nil || g != nil {
		t.Fatalf("unsupported provider must fail closed")
	}
	if !SupportedProvider("gemini") || SupportedProvider("x") {
		t.Fatal("SupportedProvider")
	}
}
