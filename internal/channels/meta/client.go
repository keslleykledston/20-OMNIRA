package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// DefaultGraphBase / DefaultGraphVersion: the WhatsApp Cloud API lives in the Graph API.
const (
	DefaultGraphBase    = "https://graph.facebook.com"
	DefaultGraphVersion = "v21.0"
	maxMediaBytes       = 100 << 20 // WhatsApp's own ceiling for documents
	maxJSONBytes        = 1 << 20
)

// Client talks to the Graph API. It holds no credential: every call receives the connection's access token, resolved
// server-side from the encrypted store, and never logs it. Errors carry only a classification and the provider's
// numeric code: never the response body (it can echo phone numbers or message text).
type Client struct {
	http    *http.Client
	base    string
	version string
}

var (
	versionPattern = regexp.MustCompile(`^v[0-9]{1,2}\.[0-9]$`)
	digitsPattern  = regexp.MustCompile(`^[0-9]{5,20}$`)
)

// NewClient validates the base URL (https only, no credentials) and API version.
func NewClient(base, version string, hc *http.Client) (*Client, error) {
	if strings.TrimSpace(base) == "" {
		base = DefaultGraphBase
	}
	if strings.TrimSpace(version) == "" {
		version = DefaultGraphVersion
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return nil, fmt.Errorf("%w: invalid Graph API base URL", ports.ErrConfiguration)
	}
	if !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("%w: invalid Graph API version", ports.ErrConfiguration)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if hc.CheckRedirect == nil {
		cp := *hc // never mutate the caller's client
		cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		hc = &cp
	}
	return &Client{http: hc, base: u.Scheme + "://" + u.Host, version: version}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// graphError is the provider's error envelope; only the codes are used.
type graphError struct {
	Error struct {
		Code    int `json:"code"`
		Subcode int `json:"error_subcode"`
	} `json:"error"`
}

// classify turns a non-2xx answer into the sentinel the delivery worker understands. The decisive question for a
// WRITE is "can the provider already have executed this?" because Meta has no idempotency key: only answers that prove
// "not executed" are retryable.
func classify(status int, body []byte) error {
	var ge graphError
	_ = json.Unmarshal(body, &ge)
	code := ge.Error.Code
	switch {
	case code == 131047:
		return fmt.Errorf("%w: code=%d", ports.ErrSessionWindowClosed, code)
	case status == http.StatusUnauthorized || code == 190 || code == 102 || code == 10 || (code >= 200 && code <= 299):
		return fmt.Errorf("%w: code=%d", ports.ErrAuthentication, code)
	case status == http.StatusTooManyRequests || code == 4 || code == 17 || code == 32 || code == 613 || code == 130429 || code == 131056 || code == 80007:
		// throttled before processing: nothing was sent, a later attempt is safe
		return fmt.Errorf("%w: code=%d", ports.ErrRateLimited, code)
	case status >= 500:
		return fmt.Errorf("%w: http=%d code=%d", ports.ErrOutcomeUnknown, status, code)
	case status >= 400:
		return fmt.Errorf("%w: http=%d code=%d", ports.ErrPermanent, status, code)
	default:
		return fmt.Errorf("%w: http=%d", ports.ErrUnknown, status)
	}
}

// transportError separates "the request never left" (safe to retry) from "it may have been executed" (a timeout or a
// reset after the request was written: not safe to retry a write).
func transportError(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return fmt.Errorf("%w: connect failed", ports.ErrTransient)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return fmt.Errorf("%w: dns failed", ports.ErrTransient)
	}
	return fmt.Errorf("%w: transport", ports.ErrOutcomeUnknown)
}

func (c *Client) do(ctx context.Context, method, path, token string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("%w: encode", ports.ErrPermanent)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/"+c.version+path, rd)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: request", ports.ErrPermanent)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, transportError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
	if err != nil {
		return resp.StatusCode, nil, transportError(err)
	}
	return resp.StatusCode, data, nil
}

// SendText sends a free-text message inside the 24 h window and returns the provider's message id (wamid).
// to is the recipient's number in E.164 digits. A 2xx answer without an id is an unknown outcome: the message may
// have been sent.
func (c *Client) SendText(ctx context.Context, token, phoneNumberID, toDigits, text string) (string, error) {
	if !digitsPattern.MatchString(phoneNumberID) || !digitsPattern.MatchString(toDigits) || strings.TrimSpace(text) == "" || token == "" {
		return "", fmt.Errorf("%w: recipient, text and credentials are required", ports.ErrPermanent)
	}
	status, data, err := c.do(ctx, http.MethodPost, "/"+phoneNumberID+"/messages", token, map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                toDigits,
		"type":              "text",
		"text":              map[string]any{"preview_url": false, "body": text},
	})
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", classify(status, data)
	}
	var ok struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if json.Unmarshal(data, &ok) != nil || len(ok.Messages) == 0 || ok.Messages[0].ID == "" {
		return "", fmt.Errorf("%w: accepted without a message id", ports.ErrOutcomeUnknown)
	}
	return ok.Messages[0].ID, nil
}

// uploadSlots bounds the multipart bodies built at once (each is a copy of up to 16 MiB).
var uploadSlots = make(chan struct{}, 2)

// UploadMedia stores a file with Meta and returns its media id. An upload has no customer-visible effect, so a failure that would be
// "outcome unknown" for a message is simply retryable here.
func (c *Client) UploadMedia(ctx context.Context, token, phoneNumberID, mime, fileName string, data []byte) (string, error) {
	if !digitsPattern.MatchString(phoneNumberID) || token == "" || mime == "" || len(data) == 0 || len(data) > maxMediaBytes {
		return "", fmt.Errorf("%w: media, type and credentials are required", ports.ErrPermanent)
	}
	select {
	case uploadSlots <- struct{}{}:
		defer func() { <-uploadSlots }()
	case <-ctx.Done():
		return "", fmt.Errorf("%w: waiting for an upload slot", ports.ErrTransient)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("messaging_product", "whatsapp")
	_ = mw.WriteField("type", mime)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(fileName)))
	hdr.Set("Content-Type", mime)
	part, err := mw.CreatePart(hdr)
	if err != nil {
		return "", fmt.Errorf("%w: encode", ports.ErrPermanent)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("%w: encode", ports.ErrPermanent)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("%w: encode", ports.ErrPermanent)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+c.version+"/"+phoneNumberID+"/media", &buf)
	if err != nil {
		return "", fmt.Errorf("%w: request", ports.ErrPermanent)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	hc := *c.http
	hc.Timeout = 90 * time.Second
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return "", retryableUpload(transportError(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
	if err != nil {
		return "", retryableUpload(transportError(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", retryableUpload(classify(resp.StatusCode, body))
	}
	var ok struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &ok) != nil || ok.ID == "" {
		return "", fmt.Errorf("%w: upload accepted without a media id", ports.ErrTransient)
	}
	return ok.ID, nil
}

// retryableUpload: an ambiguous upload failure is safe to repeat (nothing was delivered to anyone).
func retryableUpload(err error) error {
	if errors.Is(err, ports.ErrOutcomeUnknown) {
		return fmt.Errorf("%w: upload", ports.ErrTransient)
	}
	return err
}

// SendMedia sends a previously uploaded media id inside the 24 h window. Same guarantees as SendText: an ambiguous failure is
// ports.ErrOutcomeUnknown, never retried automatically.
func (c *Client) SendMedia(ctx context.Context, token, phoneNumberID, toDigits, kind, mediaID, caption, fileName string) (string, error) {
	if !digitsPattern.MatchString(phoneNumberID) || !digitsPattern.MatchString(toDigits) || token == "" || mediaID == "" {
		return "", fmt.Errorf("%w: recipient, media and credentials are required", ports.ErrPermanent)
	}
	media := map[string]any{"id": mediaID}
	switch kind {
	case "image", "video":
		if caption != "" {
			media["caption"] = caption
		}
	case "document":
		if caption != "" {
			media["caption"] = caption
		}
		if fileName != "" {
			media["filename"] = fileName
		}
	case "audio":
	default:
		return "", fmt.Errorf("%w: unsupported media kind", ports.ErrPermanent)
	}
	status, data, err := c.do(ctx, http.MethodPost, "/"+phoneNumberID+"/messages", token, map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual", "to": toDigits, "type": kind, kind: media,
	})
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", classify(status, data)
	}
	var ok struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if json.Unmarshal(data, &ok) != nil || len(ok.Messages) == 0 || ok.Messages[0].ID == "" {
		return "", fmt.Errorf("%w: accepted without a message id", ports.ErrOutcomeUnknown)
	}
	return ok.Messages[0].ID, nil
}

// PhoneInfo is what a READ-ONLY probe learns about the number behind the token.
type PhoneInfo struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	VerifiedName       string `json:"verified_name"`
	QualityRating      string `json:"quality_rating"`
	Status             string `json:"status"`
	NameStatus         string `json:"name_status"`
}

// PhoneInfo reads the number's public facts. It never writes: the "Testar conexão" button may be clicked repeatedly.
func (c *Client) PhoneInfo(ctx context.Context, token, phoneNumberID string) (PhoneInfo, error) {
	if !digitsPattern.MatchString(phoneNumberID) || token == "" {
		return PhoneInfo{}, fmt.Errorf("%w: phone number id and token are required", ports.ErrPermanent)
	}
	status, data, err := c.do(ctx, http.MethodGet, "/"+phoneNumberID+"?fields=display_phone_number,verified_name,quality_rating,status,name_status", token, nil)
	if err != nil {
		return PhoneInfo{}, err
	}
	if status < 200 || status > 299 {
		return PhoneInfo{}, classify(status, data)
	}
	var out PhoneInfo
	if json.Unmarshal(data, &out) != nil || out.DisplayPhoneNumber == "" {
		return PhoneInfo{}, fmt.Errorf("%w: unexpected phone number answer", ports.ErrUnknown)
	}
	return out, nil
}

// Template is one message template of the WhatsApp Business Account as Meta reports it.
type Template struct {
	ID       string
	Name     string
	Language string
	Category string
	Status   string
	// Body is the BODY component text with {{1}}, {{2}} placeholders.
	Body          string
	VariableCount int
	// UnsupportedReason is non-empty when sending needs something this version cannot provide (header media or
	// variables, dynamic URL buttons). The template is still listed so the operator knows why it is not offered.
	UnsupportedReason string
}

var placeholderPattern = regexp.MustCompile(`\{\{([0-9]{1,2})\}\}`)

// countPlaceholders returns the highest {{n}} index, which is how many body parameters Meta expects.
func countPlaceholders(text string) int {
	max := 0
	for _, m := range placeholderPattern.FindAllStringSubmatch(text, -1) {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		if n > max {
			max = n
		}
	}
	return max
}

type templateComponent struct {
	Type    string `json:"type"`
	Format  string `json:"format"`
	Text    string `json:"text"`
	Buttons []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"buttons"`
}

func toTemplate(raw struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Language   string              `json:"language"`
	Category   string              `json:"category"`
	Status     string              `json:"status"`
	Components []templateComponent `json:"components"`
}) Template {
	t := Template{ID: raw.ID, Name: raw.Name, Language: raw.Language, Category: raw.Category, Status: raw.Status}
	for _, c := range raw.Components {
		switch strings.ToUpper(c.Type) {
		case "BODY":
			t.Body = c.Text
			t.VariableCount = countPlaceholders(c.Text)
		case "HEADER":
			if f := strings.ToUpper(c.Format); f != "" && f != "TEXT" {
				t.UnsupportedReason = "cabeçalho com mídia (" + strings.ToLower(f) + ")"
			} else if countPlaceholders(c.Text) > 0 {
				t.UnsupportedReason = "cabeçalho com variável"
			}
		case "BUTTONS":
			for _, b := range c.Buttons {
				if strings.ToUpper(b.Type) == "URL" && countPlaceholders(b.URL) > 0 {
					t.UnsupportedReason = "botão com link dinâmico"
				}
			}
		}
	}
	return t
}

// ListTemplates reads the account's message templates (read-only), following Meta's paging up to a sane bound.
func (c *Client) ListTemplates(ctx context.Context, token, wabaID string) ([]Template, error) {
	if !digitsPattern.MatchString(wabaID) || token == "" {
		return nil, fmt.Errorf("%w: waba id and token are required", ports.ErrPermanent)
	}
	var out []Template
	path := "/" + wabaID + "/message_templates?fields=id,name,language,category,status,components&limit=100"
	for page := 0; page < 10; page++ {
		status, data, err := c.do(ctx, http.MethodGet, path, token, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status > 299 {
			return nil, classify(status, data)
		}
		var body struct {
			Data []struct {
				ID         string              `json:"id"`
				Name       string              `json:"name"`
				Language   string              `json:"language"`
				Category   string              `json:"category"`
				Status     string              `json:"status"`
				Components []templateComponent `json:"components"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if json.Unmarshal(data, &body) != nil {
			return nil, fmt.Errorf("%w: unexpected templates answer", ports.ErrUnknown)
		}
		for _, raw := range body.Data {
			out = append(out, toTemplate(raw))
		}
		if body.Paging.Next == "" {
			return out, nil
		}
		// The next page is a full Graph URL; only its path and query are trusted, re-based on our own validated base.
		u, err := url.Parse(body.Paging.Next)
		if err != nil || u.Path == "" {
			return out, nil
		}
		path = strings.TrimPrefix(u.Path, "/"+c.version)
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
	}
	return out, nil
}

// SendTemplate sends an approved template with its body variables, any time (inside or outside the 24 h window).
// Like SendText it has no idempotency key, so an ambiguous failure is ports.ErrOutcomeUnknown and is never retried.
func (c *Client) SendTemplate(ctx context.Context, token, phoneNumberID, toDigits, name, language string, params []string) (string, error) {
	if !digitsPattern.MatchString(phoneNumberID) || !digitsPattern.MatchString(toDigits) || name == "" || language == "" || token == "" {
		return "", fmt.Errorf("%w: recipient, template and credentials are required", ports.ErrPermanent)
	}
	template := map[string]any{"name": name, "language": map[string]any{"code": language}}
	if len(params) > 0 {
		parameters := make([]map[string]any, len(params))
		for i, p := range params {
			parameters[i] = map[string]any{"type": "text", "text": p}
		}
		template["components"] = []map[string]any{{"type": "body", "parameters": parameters}}
	}
	status, data, err := c.do(ctx, http.MethodPost, "/"+phoneNumberID+"/messages", token, map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                toDigits,
		"type":              "template",
		"template":          template,
	})
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", classify(status, data)
	}
	var ok struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if json.Unmarshal(data, &ok) != nil || len(ok.Messages) == 0 || ok.Messages[0].ID == "" {
		return "", fmt.Errorf("%w: accepted without a message id", ports.ErrOutcomeUnknown)
	}
	return ok.Messages[0].ID, nil
}

// SendInteractive sends reply buttons (1 to 3 options) or a list (4 to 10), inside the 24 h window. Same delivery
// guarantees as SendText: no idempotency key, so an ambiguous failure is ports.ErrOutcomeUnknown and is never retried.
func (c *Client) SendInteractive(ctx context.Context, token, phoneNumberID, toDigits string, msg domain.OutboundInteractiveMessage) (string, error) {
	if !digitsPattern.MatchString(phoneNumberID) || !digitsPattern.MatchString(toDigits) || token == "" || !domain.InteractiveFits(msg.Body, msg.Options) {
		return "", fmt.Errorf("%w: recipient, credentials and a menu that fits are required", ports.ErrPermanent)
	}
	var interactive map[string]any
	if len(msg.Options) <= domain.MaxInteractiveButtons {
		buttons := make([]map[string]any, len(msg.Options))
		for i, o := range msg.Options {
			buttons[i] = map[string]any{"type": "reply", "reply": map[string]any{"id": o.ID, "title": o.Title}}
		}
		interactive = map[string]any{"type": "button", "body": map[string]any{"text": msg.Body}, "action": map[string]any{"buttons": buttons}}
	} else {
		label := strings.TrimSpace(msg.ListLabel)
		if label == "" || len([]rune(label)) > domain.MaxInteractiveListLabel {
			label = "Ver opções"
		}
		rows := make([]map[string]any, len(msg.Options))
		for i, o := range msg.Options {
			rows[i] = map[string]any{"id": o.ID, "title": o.Title}
		}
		interactive = map[string]any{"type": "list", "body": map[string]any{"text": msg.Body},
			"action": map[string]any{"button": label, "sections": []map[string]any{{"title": "Opções", "rows": rows}}}}
	}
	status, data, err := c.do(ctx, http.MethodPost, "/"+phoneNumberID+"/messages", token, map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual", "to": toDigits, "type": "interactive", "interactive": interactive,
	})
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", classify(status, data)
	}
	var ok struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if json.Unmarshal(data, &ok) != nil || len(ok.Messages) == 0 || ok.Messages[0].ID == "" {
		return "", fmt.Errorf("%w: accepted without a message id", ports.ErrOutcomeUnknown)
	}
	return ok.Messages[0].ID, nil
}

// WebhookSubscribed reports whether at least one app is subscribed to the WhatsApp Business Account's webhooks
// (read-only). It cannot tell WHICH app: it is a hint that the "messages" subscription step was done.
func (c *Client) WebhookSubscribed(ctx context.Context, token, wabaID string) (bool, error) {
	if !digitsPattern.MatchString(wabaID) || token == "" {
		return false, fmt.Errorf("%w: waba id and token are required", ports.ErrPermanent)
	}
	status, data, err := c.do(ctx, http.MethodGet, "/"+wabaID+"/subscribed_apps", token, nil)
	if err != nil {
		return false, err
	}
	if status < 200 || status > 299 {
		return false, classify(status, data)
	}
	var out struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &out) != nil {
		return false, fmt.Errorf("%w: unexpected subscribed_apps answer", ports.ErrUnknown)
	}
	return len(out.Data) > 0, nil
}

// mediaHostAllowed is the SSRF allowlist for the file URL the Graph API hands back: https and a Meta CDN/Graph host.
func mediaHostAllowed(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".fbsbx.com", ".fbcdn.net", ".facebook.com", ".whatsapp.net"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// FetchMedia resolves a media id to its short-lived URL and downloads it (the URL needs the same bearer token). The
// answer's host is checked against the allowlist before the token is sent anywhere.
func (c *Client) FetchMedia(ctx context.Context, token, mediaID string) ([]byte, string, error) {
	if !digitsPattern.MatchString(mediaID) || token == "" {
		return nil, "", fmt.Errorf("%w: media id and token are required", ports.ErrPermanent)
	}
	status, data, err := c.do(ctx, http.MethodGet, "/"+mediaID, token, nil)
	if err != nil {
		return nil, "", err
	}
	if status == http.StatusNotFound || status == http.StatusGone {
		return nil, "", fmt.Errorf("%w: media gone", ports.ErrPermanent)
	}
	if status < 200 || status > 299 {
		return nil, "", classify(status, data)
	}
	var meta struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if json.Unmarshal(data, &meta) != nil || meta.URL == "" {
		return nil, "", fmt.Errorf("%w: media answer without url", ports.ErrUnknown)
	}
	target, err := url.Parse(meta.URL)
	if err != nil || !mediaHostAllowed(target) {
		return nil, "", ports.ErrMediaSourceNotAllowed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: request", ports.ErrPermanent)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: download", ports.ErrTransient)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, "", fmt.Errorf("%w: media gone", ports.ErrPermanent)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("%w: download http=%d", ports.ErrTransient, resp.StatusCode)
	}
	bytesRead, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil || len(bytesRead) > maxMediaBytes {
		return nil, "", fmt.Errorf("%w: media body", ports.ErrTransient)
	}
	mime := meta.MimeType
	if mime == "" {
		mime = resp.Header.Get("Content-Type")
	}
	return bytesRead, mime, nil
}
