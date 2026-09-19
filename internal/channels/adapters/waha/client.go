package waha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrAuthentication = errors.New("waha: authentication failed")
	ErrConfiguration  = errors.New("waha: invalid configuration")
	ErrTransient      = errors.New("waha: transient provider error")
	ErrPermanent      = errors.New("waha: permanent provider error")
)

// Client is thin WAHA transport. It knows no Tenant, ChannelConnection or
// WAHA payload domain; adapter/application layers own those boundaries.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
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

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, result any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, ErrConfiguration
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return res.StatusCode, ErrAuthentication
	}
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		return res.StatusCode, ErrTransient
	}
	if res.StatusCode >= 400 {
		return res.StatusCode, ErrPermanent
	}
	if result != nil {
		if err := json.NewDecoder(res.Body).Decode(result); err != nil {
			return res.StatusCode, fmt.Errorf("waha: decode response: %w", err)
		}
	}
	return res.StatusCode, nil
}
