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

// captureServer records the last request body/headers it received and
// answers with a canned response — no real OpenAI traffic anywhere in this
// file (PRODUCT.7C1: no real OpenAI call during implementation/tests).
func captureServer(t *testing.T, statusCode int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured.body = raw
		captured.authHeader = r.Header.Get("Authorization")
		captured.path = r.URL.Path
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

type capturedRequest struct {
	body       []byte
	authHeader string
	path       string
}

func (c *capturedRequest) decoded(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.body, &m); err != nil {
		t.Fatalf("request body is not valid JSON: %v (body=%s)", err, c.body)
	}
	return m
}

const successBody = `{"output":[{"type":"message","content":[{"type":"output_text","text":"resumo gerado"}]}]}`

func TestOpenAIGenerator_RequestShape_EnforcesPrivacyAndSafetyFields(t *testing.T) {
	srv, captured := captureServer(t, http.StatusOK, successBody)
	gen, err := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "test-key-do-not-log", Model: "test-model", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewOpenAIGenerator: %v", err)
	}
	_, err = gen.Generate(context.Background(), ports.GenerateRequest{
		Instructions:    "instrução fixa",
		Input:           "CUSTOMER: oi",
		MaxOutputTokens: 300,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if captured.path != "/responses" {
		t.Fatalf("path = %q, want /responses", captured.path)
	}
	req := captured.decoded(t)

	if req["store"] != false {
		t.Fatalf("store = %v, want false", req["store"])
	}
	if req["background"] != false {
		t.Fatalf("background = %v, want false", req["background"])
	}
	if req["model"] != "test-model" {
		t.Fatalf("model = %v, want test-model", req["model"])
	}
	if req["max_output_tokens"].(float64) != 300 {
		t.Fatalf("max_output_tokens = %v, want 300", req["max_output_tokens"])
	}
	reasoning, ok := req["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "none" {
		t.Fatalf("reasoning.effort = %v, want none", req["reasoning"])
	}
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Fatalf("tools = %v, want an explicit empty array", req["tools"])
	}
	if req["instructions"] != "instrução fixa" {
		t.Fatalf("instructions = %v", req["instructions"])
	}
	if req["input"] != "CUSTOMER: oi" {
		t.Fatalf("input = %v", req["input"])
	}
}

func TestOpenAIGenerator_RequestNeverContainsForbiddenFields(t *testing.T) {
	srv, captured := captureServer(t, http.StatusOK, successBody)
	gen, err := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "secret-key-value", Model: "m", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewOpenAIGenerator: %v", err)
	}
	if _, err := gen.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "CUSTOMER: +5511999999999 joao@example.com", MaxOutputTokens: 300}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	req := captured.decoded(t)
	forbidden := []string{"tenant_id", "conversation_id", "user_id", "phone", "email", "external_id", "metadata", "previous_response_id"}
	for _, key := range forbidden {
		if _, present := req[key]; present {
			t.Fatalf("request body must never contain field %q", key)
		}
	}
	// The credential itself must reach the provider only via the
	// Authorization header, never inside the JSON body.
	if strings.Contains(string(captured.body), "secret-key-value") {
		t.Fatal("API key leaked into the request body — it must only appear in the Authorization header")
	}
	if captured.authHeader != "Bearer secret-key-value" {
		t.Fatalf("Authorization header = %q", captured.authHeader)
	}
}

func TestOpenAIGenerator_ParsesOutputText(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, successBody)
	gen, _ := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second})
	resp, err := gen.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "y", MaxOutputTokens: 300})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.OutputText != "resumo gerado" {
		t.Fatalf("OutputText = %q", resp.OutputText)
	}
}

func TestOpenAIGenerator_Unauthorized(t *testing.T) {
	srv, _ := captureServer(t, http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`)
	gen, _ := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second})
	_, err := gen.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "y", MaxOutputTokens: 300})
	if !errors.Is(err, ErrOpenAIUnauthorized) {
		t.Fatalf("err = %v, want ErrOpenAIUnauthorized", err)
	}
}

func TestOpenAIGenerator_RateLimited(t *testing.T) {
	srv, _ := captureServer(t, http.StatusTooManyRequests, `{"error":{"message":"rate limit"}}`)
	gen, _ := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second})
	_, err := gen.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "y", MaxOutputTokens: 300})
	if !errors.Is(err, ErrOpenAIRateLimited) {
		t.Fatalf("err = %v, want ErrOpenAIRateLimited", err)
	}
}

func TestOpenAIGenerator_ServerError(t *testing.T) {
	srv, _ := captureServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	gen, _ := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second})
	_, err := gen.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "y", MaxOutputTokens: 300})
	if !errors.Is(err, ErrOpenAIUnavailable) {
		t.Fatalf("err = %v, want ErrOpenAIUnavailable", err)
	}
}

func TestOpenAIGenerator_Timeout_NoRetry(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(successBody))
	}))
	t.Cleanup(srv.Close)
	gen, _ := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Millisecond})
	_, err := gen.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "y", MaxOutputTokens: 300})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(err, ErrOpenAITimeout) && !errors.Is(err, ErrOpenAIUnavailable) {
		t.Fatalf("err = %v, want a timeout/unavailable classification", err)
	}
	// Exactly one attempt: a plain http.Client with no wrapper performs no
	// automatic retries (PRODUCT.7C1 §12) — this is a property of the
	// implementation choice (net/http directly, no vendor SDK), verified
	// here rather than assumed.
	if callCount != 1 {
		t.Fatalf("server received %d requests, want exactly 1 (no automatic retry)", callCount)
	}
}

func TestOpenAIGenerator_MalformedResponse(t *testing.T) {
	srv, _ := captureServer(t, http.StatusOK, `{"unexpected":"shape"}`)
	gen, _ := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second})
	_, err := gen.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "y", MaxOutputTokens: 300})
	if !errors.Is(err, ErrOpenAIMalformedResponse) {
		t.Fatalf("err = %v, want ErrOpenAIMalformedResponse", err)
	}
}

func TestNewOpenAIGenerator_RejectsIncompleteConfig(t *testing.T) {
	if _, err := NewOpenAIGenerator(OpenAIConfig{Model: "m"}); !errors.Is(err, ErrOpenAINotConfigured) {
		t.Fatalf("missing API key: err = %v, want ErrOpenAINotConfigured", err)
	}
	if _, err := NewOpenAIGenerator(OpenAIConfig{APIKey: "k"}); !errors.Is(err, ErrOpenAINotConfigured) {
		t.Fatalf("missing model: err = %v, want ErrOpenAINotConfigured", err)
	}
}

func TestOpenAIGenerator_ReportsUsageForAccountingOnly(t *testing.T) {
	body := `{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":812,"output_tokens":37,"total_tokens":849}}`
	srv, _ := captureServer(t, http.StatusOK, body)
	gen, _ := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second})
	resp, err := gen.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "y", MaxOutputTokens: 300})
	if err != nil || resp.InputTokens != 812 || resp.OutputTokens != 37 {
		t.Fatalf("usage: %+v %v", resp, err)
	}
	// a response without usage is fine: zero, not an error
	srv2, _ := captureServer(t, http.StatusOK, successBody)
	gen2, _ := NewOpenAIGenerator(OpenAIConfig{BaseURL: srv2.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second})
	if resp, err := gen2.Generate(context.Background(), ports.GenerateRequest{Instructions: "x", Input: "y", MaxOutputTokens: 300}); err != nil || resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Fatalf("no usage: %+v %v", resp, err)
	}
}
