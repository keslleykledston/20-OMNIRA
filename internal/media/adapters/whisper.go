package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"

	"github.com/omnira/omnira/internal/media/domain"
	"github.com/omnira/omnira/internal/media/ports"
)

// Whisper talks to the local whisper.cpp server (the omnira-whisper systemd unit: GPU, no internet, runs ffmpeg
// inside its own sandbox). The audio never leaves this machine.
type Whisper struct {
	baseURL string
	model   string
	client  *http.Client
}

var _ ports.Transcriber = (*Whisper)(nil)

// NewWhisper accepts only an http(s) URL with a host. model is recorded with each transcript.
func NewWhisper(baseURL, model string) (*Whisper, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("whisper: invalid base URL")
	}
	return &Whisper{
		baseURL: u.Scheme + "://" + u.Host,
		model:   model,
		client: &http.Client{
			Timeout:       4 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type whisperVerbose struct {
	Language string  `json:"language"`
	Duration float64 `json:"duration"`
	Text     string  `json:"text"`
	// LanguageProbabilities is keyed by ISO code ({"pt": 0.99}); "language" itself comes as a full name.
	LanguageProbabilities map[string]float64 `json:"language_probabilities"`
}

// truncatedAfterSeconds: the server's ffmpeg wrapper stops reading at 10 minutes (ADR-0016 limit).
const truncatedAfterSeconds = 599

func (w *Whisper) Transcribe(ctx context.Context, audio []byte, _ string) (ports.Analysis, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "audio")
	if err != nil {
		return ports.Analysis{}, err
	}
	if _, err := part.Write(audio); err != nil {
		return ports.Analysis{}, err
	}
	_ = mw.WriteField("response_format", "verbose_json")
	_ = mw.WriteField("temperature", "0.0")
	if err := mw.Close(); err != nil {
		return ports.Analysis{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/inference", &body)
	if err != nil {
		return ports.Analysis{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := w.client.Do(req)
	if err != nil {
		return ports.Analysis{}, fmt.Errorf("whisper unavailable: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return ports.Analysis{}, fmt.Errorf("whisper read: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return ports.Analysis{}, fmt.Errorf("whisper returned HTTP %d", res.StatusCode)
	}
	var v whisperVerbose
	if err := json.Unmarshal(raw, &v); err != nil {
		return ports.Analysis{}, fmt.Errorf("whisper: unreadable response")
	}
	text := domain.SanitizeDerivedText(v.Text)
	if text == "" || domain.IsStockHallucination(text) {
		return ports.Analysis{}, ports.ErrNoSpeech
	}
	a := ports.Analysis{Text: text, Language: isoLanguage(v), Model: w.model, Suspicious: domain.LooksLikeInstruction(text)}
	if v.Duration >= truncatedAfterSeconds {
		a.Reason = "truncated_at_10min"
	}
	return a, nil
}

// isoLanguage prefers the ISO code with the highest probability and falls back to the reported value when it
// already looks like a code. Anything else is dropped rather than stored.
func isoLanguage(v whisperVerbose) string {
	best, bestP := "", -1.0
	for code, p := range v.LanguageProbabilities {
		if p > bestP && len(code) >= 2 && len(code) <= 3 {
			if c := cleanLang(code); c != "" {
				best, bestP = c, p
			}
		}
	}
	if best != "" {
		return best
	}
	if len(v.Language) <= 3 {
		return cleanLang(v.Language)
	}
	return ""
}

func cleanLang(l string) string {
	if len(l) < 2 || len(l) > 8 {
		return ""
	}
	for _, r := range l {
		if !(r >= 'a' && r <= 'z' || r == '-') {
			return ""
		}
	}
	return l
}
