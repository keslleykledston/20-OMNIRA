package waha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/channels/ports"
)

var (
	ErrAuthentication      = ports.ErrAuthentication
	ErrConfiguration       = ports.ErrConfiguration
	ErrTransient           = ports.ErrTransient
	ErrPermanent           = ports.ErrPermanent
	ErrRateLimited         = ports.ErrRateLimited
	ErrProviderUnavailable = ports.ErrProviderUnavailable
	ErrSessionDisconnected = ports.ErrSessionDisconnected
	ErrUnknown             = ports.ErrUnknown
)

// Client is thin WAHA transport. It knows no Tenant, ChannelConnection or
// WAHA payload domain; adapter/application layers own those boundaries.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Session is the provider response reduced to fields needed by U2. Raw WAHA
// payloads must not cross adapter boundary.
type Session struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Engine struct {
		Name string `json:"engine"`
	} `json:"engine"`
}

type QRCode struct {
	MIMEType string `json:"mimetype"`
	Data     string `json:"data"`
}

type Account struct {
	ID       string `json:"id"`
	PushName string `json:"pushName"`
}

type sendTextRequest struct {
	Session string `json:"session"`
	ChatID  string `json:"chatId"`
	Text    string `json:"text"`
}

type sendTextResponse struct {
	ID string `json:"id"`
}

type WebhookConfig struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
	HMAC   struct {
		Key string `json:"key"`
	} `json:"hmac"`
}

type sessionCreateRequest struct {
	Name   string `json:"name"`
	Start  bool   `json:"start"`
	Config struct {
		Webhooks []WebhookConfig `json:"webhooks,omitempty"`
	} `json:"config,omitempty"`
}

func NewClient(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, ErrConfiguration
	}
	if apiKey == "" {
		return nil, ErrConfiguration
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, httpClient: httpClient}, nil
}

func (c *Client) Health(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/health", nil, nil)
	return err
}

func (c *Client) CreateSession(ctx context.Context, name string) (Session, error) {
	return c.createSession(ctx, sessionCreateRequest{Name: name, Start: false})
}

func (c *Client) CreateSessionWithWebhook(ctx context.Context, name, webhookURL, hmacKey string) (Session, error) {
	request := sessionCreateRequest{Name: name, Start: false}
	webhook := WebhookConfig{URL: webhookURL, Events: []string{"message.any", "message.ack", "session.status"}}
	webhook.HMAC.Key = hmacKey
	request.Config.Webhooks = []WebhookConfig{webhook}
	return c.createSession(ctx, request)
}

func (c *Client) createSession(ctx context.Context, request sessionCreateRequest) (Session, error) {
	bodyBytes, err := json.Marshal(request)
	if err != nil {
		return Session{}, ErrConfiguration
	}
	body := strings.NewReader(string(bodyBytes))
	var session Session
	_, err = c.do(ctx, http.MethodPost, "/api/sessions", body, &session)
	return session, err
}

func (c *Client) GetSession(ctx context.Context, name string) (Session, error) {
	var session Session
	_, err := c.do(ctx, http.MethodGet, "/api/sessions/"+url.PathEscape(name), nil, &session)
	return session, err
}

func (c *Client) StartSession(ctx context.Context, name string) (Session, error) {
	return c.sessionAction(ctx, name, "start")
}

func (c *Client) StopSession(ctx context.Context, name string) (Session, error) {
	return c.sessionAction(ctx, name, "stop")
}

func (c *Client) RestartSession(ctx context.Context, name string) (Session, error) {
	return c.sessionAction(ctx, name, "restart")
}

func (c *Client) GetQRCode(ctx context.Context, name string) (QRCode, error) {
	var qr QRCode
	_, err := c.doWithHeaders(ctx, http.MethodGet, "/api/"+url.PathEscape(name)+"/auth/qr", nil,
		map[string]string{"Accept": "application/json"}, &qr)
	return qr, err
}

func (c *Client) GetMe(ctx context.Context, name string) (*Account, error) {
	var account *Account
	_, err := c.do(ctx, http.MethodGet, "/api/sessions/"+url.PathEscape(name)+"/me", nil, &account)
	return account, err
}

// SendText calls WAHA's provider-neutral endpoint. The application/outbox
// owns idempotency because WAHA has no stable idempotency-key contract.
func (c *Client) SendText(ctx context.Context, name, chatID, text string) (string, error) {
	body, err := json.Marshal(sendTextRequest{Session: name, ChatID: chatID, Text: text})
	if err != nil {
		return "", ErrConfiguration
	}
	var response sendTextResponse
	if _, err = c.do(ctx, http.MethodPost, "/api/sendText", strings.NewReader(string(body)), &response); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", fmt.Errorf("%w: sendText response missing message id", ErrUnknown)
	}
	return response.ID, nil
}

func (c *Client) sessionAction(ctx context.Context, name, action string) (Session, error) {
	var session Session
	_, err := c.do(ctx, http.MethodPost, "/api/sessions/"+url.PathEscape(name)+"/"+action, nil, &session)
	return session, err
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, result any) (int, error) {
	return c.doWithHeaders(ctx, method, path, body, nil, result)
}

func (c *Client) doWithHeaders(ctx context.Context, method, path string, body io.Reader, headers map[string]string, result any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, ErrConfiguration
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %w: %v", ErrProviderUnavailable, ErrTransient, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return res.StatusCode, ErrAuthentication
	}
	if res.StatusCode == http.StatusTooManyRequests {
		return res.StatusCode, fmt.Errorf("%w: %w", ErrRateLimited, ErrTransient)
	}
	if res.StatusCode == http.StatusConflict {
		return res.StatusCode, fmt.Errorf("%w: %w", ErrSessionDisconnected, ErrPermanent)
	}
	if res.StatusCode >= 500 {
		return res.StatusCode, fmt.Errorf("%w: %w", ErrProviderUnavailable, ErrTransient)
	}
	if res.StatusCode >= 400 {
		return res.StatusCode, ErrPermanent
	}
	if result != nil {
		if err := json.NewDecoder(res.Body).Decode(result); err != nil {
			return res.StatusCode, fmt.Errorf("%w: decode response", ErrUnknown)
		}
	}
	return res.StatusCode, nil
}
