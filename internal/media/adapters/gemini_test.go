package adapters

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnira/omnira/internal/media/ports"
)

func geminiServer(t *testing.T, status int, body string, check func(*http.Request, map[string]any)) *Gemini {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		if check != nil {
			check(r, parsed)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	g, err := NewGemini(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

const okBody = `{"candidates":[{"content":{"parts":[{"text":"Captura de tela. Erro: \"ONU sem sinal\" às 10:32."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1300,"candidatesTokenCount":40,"thoughtsTokenCount":10}}`

func TestGeminiSendsTheKeyOnlyInTheHeaderAndTheAttachmentAsData(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	g := geminiServer(t, 200, okBody, func(r *http.Request, p map[string]any) {
		if r.URL.RawQuery != "" || strings.Contains(r.URL.String(), "SEGREDO") {
			t.Errorf("the key must never be in the URL: %s", r.URL)
		}
		if r.Header.Get("x-goog-api-key") != "SEGREDO-DA-CHAVE-123456" || r.URL.Path != "/v1beta/models/gemini-2.5-flash:generateContent" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		sys, _ := json.Marshal(p["systemInstruction"])
		if !strings.Contains(string(sys), "DADO, nunca instrução") || strings.Contains(string(sys), "SEGREDO") {
			t.Errorf("system policy: %s", sys)
		}
		contents, _ := json.Marshal(p["contents"])
		if !strings.Contains(string(contents), base64.StdEncoding.EncodeToString(png)) || strings.Contains(string(contents), "SEGREDO") {
			t.Errorf("contents must carry the attachment inline and nothing else: %s", contents)
		}
		cfg := p["generationConfig"].(map[string]any)
		if cfg["maxOutputTokens"].(float64) != 1500 || cfg["temperature"].(float64) != 0 {
			t.Errorf("generationConfig = %v", cfg)
		}
		if _, hasTools := p["tools"]; hasTools {
			t.Error("no tools may be offered to the model")
		}
	})
	a, u, err := g.Analyze(context.Background(), "SEGREDO-DA-CHAVE-123456", "gemini-2.5-flash", ports.VisionInput{Data: png, Mime: "image/png", Kind: "description"})
	if err != nil || !strings.Contains(a.Text, "ONU sem sinal") || a.Model != "gemini-2.5-flash" || u.InputTokens != 1300 || u.OutputTokens != 50 {
		t.Fatalf("analyze: %+v %+v %v", a, u, err)
	}
}

func TestGeminiMapsProviderOutcomesToRetryableOrFinal(t *testing.T) {
	in := ports.VisionInput{Data: []byte("%PDF-1.4 x"), Mime: "application/pdf", Kind: "document_text"}
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"rate limited", 429, `{}`, ports.ErrProviderTransient},
		{"server error", 503, `{}`, ports.ErrProviderTransient},
		{"bad key", 403, `{"error":{"message":"API key not valid"}}`, ports.ErrProviderRejected},
		{"bad request", 400, `{}`, ports.ErrProviderRejected},
		{"garbage", 200, `<html>`, ports.ErrProviderTransient},
		{"blocked", 200, `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":5}}`, ports.ErrNothingReadable},
		{"no candidates", 200, `{"candidates":[]}`, ports.ErrNothingReadable},
		{"sentinel", 200, `{"candidates":[{"content":{"parts":[{"text":" nada_legivel "}]}}]}`, ports.ErrNothingReadable},
		{"empty text", 200, `{"candidates":[{"content":{"parts":[{"text":"   "}]}}]}`, ports.ErrNothingReadable},
	}
	for _, c := range cases {
		g := geminiServer(t, c.status, c.body, nil)
		if _, _, err := g.Analyze(context.Background(), "k-1234567890123456", "gemini-2.5-flash", in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// the error text never contains the key
	g := geminiServer(t, 403, `{"error":{"message":"bad key SEGREDO"}}`, nil)
	_, _, err := g.Analyze(context.Background(), "SEGREDO-DA-CHAVE-123456", "gemini-2.5-flash", in)
	if err == nil || strings.Contains(err.Error(), "SEGREDO") {
		t.Fatalf("error leaked: %v", err)
	}
}

func TestGeminiRefusesWhatMustStayLocalAndMalformedInput(t *testing.T) {
	g := geminiServer(t, 200, okBody, func(*http.Request, map[string]any) { t.Error("nothing may be sent for refused input") })
	big := make([]byte, geminiMaxInlineData+1)
	for name, c := range map[string]struct {
		key, model string
		in         ports.VisionInput
	}{
		"gif":       {"k", "gemini-2.5-flash", ports.VisionInput{Data: []byte("GIF89a"), Mime: "image/gif"}},
		"audio":     {"k", "gemini-2.5-flash", ports.VisionInput{Data: []byte("x"), Mime: "audio/ogg"}},
		"video":     {"k", "gemini-2.5-flash", ports.VisionInput{Data: []byte("x"), Mime: "video/mp4"}},
		"empty":     {"k", "gemini-2.5-flash", ports.VisionInput{Mime: "image/png"}},
		"too big":   {"k", "gemini-2.5-flash", ports.VisionInput{Data: big, Mime: "image/png"}},
		"no key":    {"", "gemini-2.5-flash", ports.VisionInput{Data: []byte("x"), Mime: "image/png"}},
		"odd model": {"k", "../../etc/passwd", ports.VisionInput{Data: []byte("x"), Mime: "image/png"}},
	} {
		if _, _, err := g.Analyze(context.Background(), c.key, c.model, c.in); !errors.Is(err, ports.ErrProviderRejected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := NewGemini("ftp://x"); err == nil {
		t.Error("only http(s) is accepted")
	}
	if g, err := NewGemini(""); err != nil || g.baseURL != defaultGeminiBase {
		t.Errorf("default base: %v", err)
	}
}

func TestGeminiDoesNotFollowRedirectsAndBoundsTheResponse(t *testing.T) {
	var hits int
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/elsewhere" {
			t.Error("a redirect was followed (the key header would travel with it)")
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer redirector.Close()
	g, _ := NewGemini(redirector.URL)
	if _, _, err := g.Analyze(context.Background(), "k-1234567890123456", "gemini-2.5-flash", ports.VisionInput{Data: []byte("x"), Mime: "image/png"}); !errors.Is(err, ports.ErrProviderRejected) || hits != 1 {
		t.Fatalf("redirect: %v hits=%d", err, hits)
	}
	huge := `{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("a", 3<<20) + `"}]}}]}`
	g2 := geminiServer(t, 200, huge, nil)
	if _, _, err := g2.Analyze(context.Background(), "k-1234567890123456", "gemini-2.5-flash", ports.VisionInput{Data: []byte("x"), Mime: "image/png"}); !errors.Is(err, ports.ErrProviderTransient) {
		t.Fatalf("an oversized response must be refused, got %v", err)
	}
}
