package connectors

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// IXCConnector fala com o ERP IXCSoft, usado por provedores de internet.
//
// ATENÇÃO — o contrato abaixo foi escrito a partir da documentação pública do
// IXC e NÃO foi exercido contra um ambiente real: em 2026-09-20 não havia
// credencial disponível (ver GATE R5). Cada suposição está marcada com
// "SUPOSIÇÃO:" para ser conferida na primeira execução real; o fake server dos
// testes implementa exatamente estas suposições, então os testes passam mesmo
// se elas estiverem erradas. Eles provam o comportamento do adapter, não a
// compatibilidade com o IXC.
//
// SUPOSIÇÃO (autenticação): HTTP Basic com usuário e token da API.
// SUPOSIÇÃO (consulta): POST na tabela com header "ixcsoft: listar" e corpo
//   {qtype, query, oper, page, rp, sortname, sortorder}; resposta
//   {type, total, registros:[...]}.
// SUPOSIÇÃO (inserção): POST na tabela com os campos no corpo; resposta
//   {type:"success", id:"<novo id>"}.
// SUPOSIÇÃO (edição): PUT em <tabela>/<id>.
// SUPOSIÇÃO (tabelas): "cliente" para assinantes, "su_oss_chamado" para
//   chamados de suporte.
type IXCConnector struct {
	baseURL string
	user    string
	token   string
	client  *http.Client
	// now permite teste determinístico de timeout/retry.
	now func() time.Time
}

const (
	ixcTableCustomer = "cliente"
	ixcTableTicket   = "su_oss_chamado"
	ixcMaxAttempts   = 3
)

// IXCConfig — parâmetros de conexão. Nunca é logado nem serializado.
type IXCConfig struct {
	BaseURL string
	User    string
	Token   string
	Timeout time.Duration
}

// ErrIXCNotConfigured indica credencial ausente ou incompleta. É permanente:
// repetir a chamada sem reconfigurar não muda o resultado.
var ErrIXCNotConfigured = errors.New("ixc: connector is not configured")

// ErrIXCUnavailable indica falha transitória do ERP (timeout, 5xx). Pode ser
// repetida.
var ErrIXCUnavailable = errors.New("ixc: provider unavailable")

// ErrIXCRejected indica que o ERP recusou a requisição (4xx, validação). É
// permanente: repetir com o mesmo payload produz o mesmo erro.
var ErrIXCRejected = errors.New("ixc: provider rejected the request")

// ErrIXCNotFound indica que o recurso não existe no ERP.
var ErrIXCNotFound = errors.New("ixc: resource not found")

func NewIXCConnector(cfg IXCConfig) (*IXCConnector, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" || strings.TrimSpace(cfg.User) == "" || strings.TrimSpace(cfg.Token) == "" {
		return nil, ErrIXCNotConfigured
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, fmt.Errorf("%w: base url must be absolute", ErrIXCNotConfigured)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &IXCConnector{
		baseURL: base,
		user:    cfg.User,
		token:   cfg.Token,
		client:  &http.Client{Timeout: timeout},
		now:     time.Now,
	}, nil
}

func (c *IXCConnector) Name() string { return "ixc" }

// Authenticate confirma que a credencial funciona fazendo a consulta mais
// barata possível. Não há endpoint de "ping" no IXC, então uma listagem com
// uma linha serve de sonda.
func (c *IXCConnector) Authenticate(ctx context.Context, _ map[string]interface{}) error {
	if c == nil || c.client == nil {
		return ErrIXCNotConfigured
	}
	_, err := c.list(ctx, ixcTableCustomer, ixcQuery{QType: "cliente.id", Query: "0", Oper: ">", Page: "1", RP: "1"})
	return err
}

// FindCustomer localiza o assinante. O identificador aceito é telefone, CPF ou
// CNPJ — é o que o atendimento por WhatsApp tem em mãos; e-mail raramente
// identifica um assinante de provedor.
func (c *IXCConnector) FindCustomer(ctx context.Context, query string) (string, error) {
	if c == nil || c.client == nil {
		return "", ErrIXCNotConfigured
	}
	digits := onlyDigits(query)
	if digits == "" {
		return "", fmt.Errorf("%w: empty customer query", ErrIXCRejected)
	}
	// SUPOSIÇÃO: estes são os campos consultáveis da tabela cliente. A ordem
	// reflete a especificidade: documento identifica melhor que telefone.
	for _, field := range []string{"cliente.cnpj_cpf", "cliente.telefone_celular", "cliente.fone"} {
		records, err := c.list(ctx, ixcTableCustomer, ixcQuery{
			QType: field, Query: digits, Oper: "=", Page: "1", RP: "1",
		})
		if err != nil {
			return "", err
		}
		if len(records) > 0 {
			if id := stringField(records[0], "id"); id != "" {
				return id, nil
			}
		}
	}
	return "", ErrIXCNotFound
}

// CreateTicket abre um chamado de suporte para o assinante.
//
// A idempotência é responsabilidade do chamador: o IXC não oferece chave de
// idempotência, então repetir esta chamada abre DOIS chamados. Por isso ela
// não é repetida automaticamente em caso de erro de rede — ver doRequest.
func (c *IXCConnector) CreateTicket(ctx context.Context, customerID, subject string) (string, error) {
	if c == nil || c.client == nil {
		return "", ErrIXCNotConfigured
	}
	if strings.TrimSpace(customerID) == "" || strings.TrimSpace(subject) == "" {
		return "", fmt.Errorf("%w: customer and subject are required", ErrIXCRejected)
	}
	// SUPOSIÇÃO: campos mínimos para abrir chamado.
	payload := map[string]string{
		"id_cliente": customerID,
		"titulo":     truncate(subject, 200),
		"mensagem":   truncate(subject, 2000),
		"status":     "N", // SUPOSIÇÃO: N = novo/aberto
		"origem_endereco": "M",
	}
	body, err := c.do(ctx, http.MethodPost, ixcTableTicket, nil, payload, false)
	if err != nil {
		return "", err
	}
	var created struct {
		Type string `json:"type"`
		ID   any    `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return "", fmt.Errorf("%w: malformed create response", ErrIXCRejected)
	}
	id := anyToString(created.ID)
	if id == "" || strings.EqualFold(created.Type, "error") {
		return "", fmt.Errorf("%w: ticket not created", ErrIXCRejected)
	}
	return id, nil
}

func (c *IXCConnector) GetTicket(ctx context.Context, ticketID string) (*CRMTicket, error) {
	if c == nil || c.client == nil {
		return nil, ErrIXCNotConfigured
	}
	if strings.TrimSpace(ticketID) == "" {
		return nil, fmt.Errorf("%w: ticket id is required", ErrIXCRejected)
	}
	records, err := c.list(ctx, ixcTableTicket, ixcQuery{
		QType: "su_oss_chamado.id", Query: ticketID, Oper: "=", Page: "1", RP: "1",
	})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, ErrIXCNotFound
	}
	return ixcTicketFromRecord(records[0]), nil
}

// UpdateTicket move o chamado para um estado canônico. O mapeamento para os
// códigos do IXC fica aqui, no adapter: o domínio não conhece "N"/"EN"/"F".
func (c *IXCConnector) UpdateTicket(ctx context.Context, ticketID, status string) error {
	if c == nil || c.client == nil {
		return ErrIXCNotConfigured
	}
	code, ok := ixcStatusCode(status)
	if !ok {
		return fmt.Errorf("%w: unsupported ticket status %q", ErrIXCRejected, status)
	}
	if strings.TrimSpace(ticketID) == "" {
		return fmt.Errorf("%w: ticket id is required", ErrIXCRejected)
	}
	_, err := c.do(ctx, http.MethodPut, ixcTableTicket+"/"+ticketID, nil, map[string]string{
		"id":     ticketID,
		"status": code,
	}, true)
	return err
}

func (c *IXCConnector) CloseTicket(ctx context.Context, ticketID string) error {
	return c.UpdateTicket(ctx, ticketID, "closed")
}

// --- interno ---

type ixcQuery struct {
	QType     string `json:"qtype"`
	Query     string `json:"query"`
	Oper      string `json:"oper"`
	Page      string `json:"page"`
	RP        string `json:"rp"`
	SortName  string `json:"sortname,omitempty"`
	SortOrder string `json:"sortorder,omitempty"`
}

func (c *IXCConnector) list(ctx context.Context, table string, q ixcQuery) ([]map[string]any, error) {
	body, err := c.do(ctx, http.MethodPost, table, map[string]string{"ixcsoft": "listar"}, q, true)
	if err != nil {
		return nil, err
	}
	var page struct {
		Type      string           `json:"type"`
		Total     any              `json:"total"`
		Registros []map[string]any `json:"registros"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("%w: malformed list response", ErrIXCRejected)
	}
	if strings.EqualFold(page.Type, "error") {
		return nil, fmt.Errorf("%w: provider returned error", ErrIXCRejected)
	}
	return page.Registros, nil
}

// do executa a requisição. `retriable` separa o que pode ser repetido do que
// não pode: uma consulta é segura de repetir, a abertura de chamado não —
// o IXC não tem chave de idempotência, então um retry cego duplicaria o
// chamado do cliente.
func (c *IXCConnector) do(ctx context.Context, method, path string, extraHeaders map[string]string, payload any, retriable bool) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode request", ErrIXCRejected)
	}
	attempts := 1
	if retriable {
		attempts = ixcMaxAttempts
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		body, err := c.attempt(ctx, method, path, extraHeaders, raw)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !errors.Is(err, ErrIXCUnavailable) || attempt == attempts {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		}
	}
	return nil, lastErr
}

func (c *IXCConnector) attempt(ctx context.Context, method, path string, extraHeaders map[string]string, raw []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot build request", ErrIXCRejected)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.token)))
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	res, err := c.client.Do(req)
	if err != nil {
		// Falha de transporte não distingue "não chegou" de "chegou e a
		// resposta se perdeu"; por isso só é repetida onde é seguro repetir.
		return nil, fmt.Errorf("%w: %v", ErrIXCUnavailable, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read response", ErrIXCUnavailable)
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		// Credencial inválida não melhora com repetição.
		return nil, fmt.Errorf("%w: authentication rejected", ErrIXCNotConfigured)
	case res.StatusCode == http.StatusNotFound:
		return nil, ErrIXCNotFound
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		return nil, fmt.Errorf("%w: status %d", ErrIXCUnavailable, res.StatusCode)
	case res.StatusCode >= 400:
		return nil, fmt.Errorf("%w: status %d", ErrIXCRejected, res.StatusCode)
	}
	return body, nil
}

// ixcStatusCode traduz o estado canônico para o código do IXC.
// SUPOSIÇÃO: N=novo, EN=em andamento, F=finalizado.
func ixcStatusCode(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open":
		return "N", true
	case "in_progress":
		return "EN", true
	case "resolved", "closed":
		return "F", true
	default:
		return "", false
	}
}

// ixcStatusCanonical é a tradução inversa. Código desconhecido vira "open" em
// vez de string vazia: melhor um chamado aparecer aberto do que sem estado.
func ixcStatusCanonical(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "F", "C":
		return "closed"
	case "EN", "A":
		return "in_progress"
	default:
		return "open"
	}
}

func ixcTicketFromRecord(record map[string]any) *CRMTicket {
	ticket := &CRMTicket{
		ID:         stringField(record, "id"),
		CustomerID: stringField(record, "id_cliente"),
		Subject:    stringField(record, "titulo"),
		Status:     ixcStatusCanonical(stringField(record, "status")),
	}
	if t, ok := parseIXCTime(stringField(record, "data_abertura")); ok {
		ticket.CreatedAt = t
	}
	if t, ok := parseIXCTime(stringField(record, "ultima_atualizacao")); ok {
		ticket.UpdatedAt = t
	} else {
		ticket.UpdatedAt = ticket.CreatedAt
	}
	return ticket
}

// SUPOSIÇÃO: datas no fuso do servidor IXC, sem offset. Interpretadas como UTC
// para não inventar um fuso; ajustar quando o ambiente real confirmar.
func parseIXCTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func stringField(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	return anyToString(record[key])
}

func anyToString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func onlyDigits(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= max {
		return value
	}
	return string([]rune(value)[:max])
}
