package connectors

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
	// ErrCRMAmbiguous: more than one distinct CRM contact matches the lookup. The first one is NEVER picked: a wrong
	// binding would send one customer's data to another (ADR-0018). Callers leave the contact unbound.
	ErrCRMAmbiguous error = ambiguousError{}
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
		return nil, fmt.Errorf("%w: %v", ErrCRMUnavailable, transportCause(err))
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

func (c *K3GCRMClient) post(ctx context.Context, path string, payload any) ([]byte, error) {
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot marshal payload", ErrCRMRejected)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot build request", ErrCRMRejected)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCRMUnavailable, transportCause(err))
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

// CRMContact é o contato como gravado no CRM: identidade única, endereço de
// comunicação, e vínculo com empresa.
type CRMContact struct {
	ID        string
	Name      string
	Phone     string
	Email     string
	CompanyID string
}

// CRMCompany é a empresa como o OMNIRA precisa dela: identidade, rótulo para o
// operador escolher, e o documento para conferência. O resto do cadastro fica
// no CRM — o core não deve conhecer campos de módulo do CRM.
type CRMCompany struct {
	ID       string
	Name     string
	CNPJ     string
	City     string
	State    string
	IsActive bool
}

// ListCompanies traz as empresas do CRM para o operador escolher a qual
// empresa o atendimento pertence.
//
// Só lê. A lista é o "cadastro prévio": o OMNIRA não cria empresa, porque
// quem é cliente de quem é decisão que já foi tomada no CRM.
func (c *K3GCRMClient) ListCompanies(ctx context.Context) ([]CRMCompany, error) {
	if c == nil || c.client == nil {
		return nil, ErrCRMNotConfigured
	}
	body, err := c.get(ctx, "/api/companies")
	if err != nil {
		return nil, err
	}
	var page struct {
		Companies []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			RazaoSocial string `json:"razaoSocial"`
			CNPJ        string `json:"cnpj"`
			City        string `json:"city"`
			State       string `json:"state"`
			IsActive    bool   `json:"isActive"`
		} `json:"companies"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("%w: unexpected companies response", ErrCRMRejected)
	}
	out := make([]CRMCompany, 0, len(page.Companies))
	for _, raw := range page.Companies {
		// O CRM tem nome fantasia e razão social; o operador reconhece pelo
		// primeiro, mas nem toda empresa preenche os dois.
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			name = strings.TrimSpace(raw.RazaoSocial)
		}
		if strings.TrimSpace(raw.ID) == "" || name == "" {
			continue
		}
		out = append(out, CRMCompany{
			ID: raw.ID, Name: name, CNPJ: strings.TrimSpace(raw.CNPJ),
			City: strings.TrimSpace(raw.City), State: strings.TrimSpace(raw.State),
			IsActive: raw.IsActive,
		})
	}
	return out, nil
}

// ambiguousError carries the Ambiguous marker so layers that must not import this package can still recognise it.
type ambiguousError struct{}

func (ambiguousError) Error() string   { return "k3gcrm: more than one CRM contact matches; refusing to pick one" }
func (ambiguousError) Ambiguous() bool { return true }

// FindCustomerByPhone procura contato no CRM por telefone e empresa.
// Retorna nil se não encontrar. A resposta nunca é lida por posição: só contam os contatos que realmente são deste
// telefone e desta empresa; se sobrar mais de um contato DISTINTO, é ErrCRMAmbiguous.
func (c *K3GCRMClient) FindCustomerByPhone(ctx context.Context, phone, companyID string) (*CRMContact, error) {
	if c == nil || c.client == nil {
		return nil, ErrCRMNotConfigured
	}
	phone = strings.TrimSpace(phone)
	companyID = strings.TrimSpace(companyID)
	if phone == "" || companyID == "" {
		return nil, fmt.Errorf("%w: phone and companyId are required", ErrCRMRejected)
	}
	body, err := c.get(ctx, fmt.Sprintf("/api/crm/contacts?phone=%s&companyId=%s", 
		strings.ReplaceAll(phone, "+", "%2B"), companyID))
	if err != nil {
		return nil, err
	}
	var page struct {
		Contacts []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Phone     string `json:"phone"`
			Email     string `json:"email"`
			CompanyID string `json:"companyId"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("%w: unexpected contacts response", ErrCRMRejected)
	}
	wantPhone := onlyDigits(phone)
	var found *CRMContact
	for _, raw := range page.Contacts {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			continue
		}
		if cid := strings.TrimSpace(raw.CompanyID); cid != "" && cid != companyID {
			continue // another company's contact is never this one
		}
		if p := onlyDigits(raw.Phone); p != "" && p != wantPhone {
			continue
		}
		if found != nil {
			if found.ID == id {
				continue // the same contact listed twice
			}
			return nil, ErrCRMAmbiguous
		}
		found = &CRMContact{
			ID:        id,
			Name:      strings.TrimSpace(raw.Name),
			Phone:     strings.TrimSpace(raw.Phone),
			Email:     strings.TrimSpace(raw.Email),
			CompanyID: strings.TrimSpace(raw.CompanyID),
		}
	}
	return found, nil
}

// CreateContact cria um novo contato no CRM. Retorna o contato com ID atribuído
// ou erro (422 validation, 401 auth, 5xx unavail).
func (c *K3GCRMClient) CreateContact(ctx context.Context, name, phone, companyID string) (*CRMContact, error) {
	if c == nil || c.client == nil {
		return nil, ErrCRMNotConfigured
	}
	name = strings.TrimSpace(name)
	phone = strings.TrimSpace(phone)
	companyID = strings.TrimSpace(companyID)
	if name == "" || phone == "" || companyID == "" {
		return nil, fmt.Errorf("%w: name, phone and companyId are required", ErrCRMRejected)
	}
	payload := map[string]string{"name": name, "phone": phone, "companyId": companyID}
	body, err := c.post(ctx, "/api/crm/contacts", payload)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Phone     string `json:"phone"`
		Email     string `json:"email"`
		CompanyID string `json:"companyId"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: unexpected create response", ErrCRMRejected)
	}
	if strings.TrimSpace(resp.ID) == "" {
		return nil, fmt.Errorf("%w: contact created but no ID returned", ErrCRMRejected)
	}
	return &CRMContact{
		ID:        resp.ID,
		Name:      strings.TrimSpace(resp.Name),
		Phone:     strings.TrimSpace(resp.Phone),
		Email:     strings.TrimSpace(resp.Email),
		CompanyID: strings.TrimSpace(resp.CompanyID),
	}, nil
}

// CRMActivity é a atividade (atendimento) no CRM K3G.
type CRMActivity struct {
	ID        string
	Type      string
	Subject   string
	ContactID string
	CompanyID string
	CreatedAt string
}

// CreateActivity cria nova atividade (atendimento) no CRM.
// Type deve ser um dos: CALL, EMAIL, MEETING, TASK, WHATSAPP, NOTE.
// Subject é obrigatório (ex.: "Solicita orçamento", "Dúvida sobre produto").
// ContactID e CompanyID devem ser UUIDs válidos.
// Retorna activity com ID gerado ou erro (422 validation, 401 auth, 5xx unavail).
func (c *K3GCRMClient) CreateActivity(ctx context.Context, actType, subject, contactID, companyID string) (*CRMActivity, error) {
	if c == nil || c.client == nil {
		return nil, ErrCRMNotConfigured
	}
	actType = strings.TrimSpace(actType)
	subject = strings.TrimSpace(subject)
	contactID = strings.TrimSpace(contactID)
	companyID = strings.TrimSpace(companyID)
	if actType == "" || subject == "" || contactID == "" || companyID == "" {
		return nil, fmt.Errorf("%w: type, subject, contactId and companyId are required", ErrCRMRejected)
	}
	payload := map[string]string{
		"type":       actType,
		"subject":    subject,
		"contactId":  contactID,
		"companyId":  companyID,
	}
	body, err := c.post(ctx, "/api/crm/activities", payload)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Subject   string `json:"subject"`
		ContactID string `json:"contactId"`
		CompanyID string `json:"companyId"`
		CreatedAt string `json:"createdAt"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: unexpected create activity response", ErrCRMRejected)
	}
	if strings.TrimSpace(resp.ID) == "" {
		return nil, fmt.Errorf("%w: activity created but no ID returned", ErrCRMRejected)
	}
	return &CRMActivity{
		ID:        resp.ID,
		Type:      strings.TrimSpace(resp.Type),
		Subject:   strings.TrimSpace(resp.Subject),
		ContactID: strings.TrimSpace(resp.ContactID),
		CompanyID: strings.TrimSpace(resp.CompanyID),
		CreatedAt: strings.TrimSpace(resp.CreatedAt),
	}, nil
}

// transportCause drops the request URL that net/http puts in its errors: the ERP address is the instance's configuration and has no business in logs or in
// the errors that travel up to them (Codex review of phase 04b). The underlying cause (timeout, refused, DNS...) is kept.
func transportCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
