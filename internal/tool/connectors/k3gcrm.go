package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// K3GCRMClient fala com o CRM próprio da K3G.
//
// Mapa da API, levantado em 2026-09-20 contra o ambiente real:
//
//	GET /api/users            → {users:[...], page, pageSize, total, totalPages}
//	GET /api/companies        → {companies:[...]}
//	GET /api/crm/contacts     → {contacts:[...]}
//	    /api/crm/{deals,leads,activities,tasks} também existem
//
// Não há /api/crm/tickets: este CRM registra relacionamento, não chamado. O
// atendimento por WhatsApp corresponde a uma activity de tipo WHATSAPP —
// os tipos aceitos são CALL, EMAIL, MEETING, TASK, WHATSAPP e NOTE.
//
// Autenticação por token de API em "Authorization: Bearer <token>". Existe
// POST /api/auth/login com e-mail e senha, mas ele serve à sessão de uma
// pessoa, não a uma integração.
type K3GCRMClient struct {
	baseURL string
	token   string
	client  *http.Client
}

// Erros de intenção, para o chamador saber o que é repetível sem inspecionar
// status HTTP.
var (
	ErrCRMNotConfigured = errors.New("k3gcrm: connector is not configured")
	ErrCRMUnauthorized  = errors.New("k3gcrm: token rejected by the CRM")
	ErrCRMUnavailable   = errors.New("k3gcrm: CRM unavailable")
	ErrCRMRejected      = errors.New("k3gcrm: CRM rejected the request")
)

type K3GCRMConfig struct {
	BaseURL string
	Token   string
	Timeout time.Duration
}

func NewK3GCRMClient(cfg K3GCRMConfig) (*K3GCRMClient, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" || strings.TrimSpace(cfg.Token) == "" {
		return nil, ErrCRMNotConfigured
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, fmt.Errorf("%w: base url must be absolute", ErrCRMNotConfigured)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &K3GCRMClient{baseURL: base, token: cfg.Token, client: &http.Client{Timeout: timeout}}, nil
}

func (c *K3GCRMClient) Name() string { return "k3g_crm" }

// Ping confirma que a credencial é aceita, sem escrever nada.
//
// Usa a listagem de usuários pedindo uma única linha: é o endpoint mais barato
// que exige autenticação. Uma sonda que escrevesse deixaria rastro no CRM a
// cada clique em "Testar".
func (c *K3GCRMClient) Ping(ctx context.Context) error {
	if c == nil || c.client == nil {
		return ErrCRMNotConfigured
	}
	body, err := c.get(ctx, "/api/users?page=1&pageSize=1")
	if err != nil {
		return err
	}
	var page struct {
		Users []map[string]any `json:"users"`
		Total *float64         `json:"total"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		// 200 com corpo inesperado costuma ser portal de login ou proxy no
		// caminho — tratar como sucesso esconderia uma integração quebrada.
		return fmt.Errorf("%w: unexpected response shape", ErrCRMRejected)
	}
	if page.Total == nil && page.Users == nil {
		return fmt.Errorf("%w: response is not the users listing", ErrCRMRejected)
	}
	return nil
}

func (c *K3GCRMClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot build request", ErrCRMRejected)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCRMUnavailable, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read response", ErrCRMUnavailable)
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, ErrCRMUnauthorized
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		return nil, fmt.Errorf("%w: status %d", ErrCRMUnavailable, res.StatusCode)
	case res.StatusCode >= 400:
		return nil, fmt.Errorf("%w: status %d", ErrCRMRejected, res.StatusCode)
	}
	return body, nil
}
