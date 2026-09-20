package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeIXC implementa as SUPOSIÇÕES documentadas em ixc.go. Ele prova o
// comportamento do adapter (tradução, erros, retry, idempotência), não a
// compatibilidade com o IXC real — essa só a credencial de verdade prova.
type fakeIXC struct {
	creates  atomic.Int32
	requests atomic.Int32
	handler  func(w http.ResponseWriter, r *http.Request, body map[string]any)
}

func (f *fakeIXC) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:token")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.handler != nil {
			f.handler(w, r, body)
			return
		}
		f.defaultHandler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeIXC) defaultHandler(w http.ResponseWriter, r *http.Request, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	listing := r.Header.Get("ixcsoft") == "listar"
	switch {
	case strings.HasSuffix(r.URL.Path, "/cliente") && listing:
		if body["query"] == "5592991740090" || body["query"] == "12345678901" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "success", "total": 1,
				"registros": []map[string]any{{"id": "4471", "razao": "Cliente Teste"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "success", "total": 0, "registros": []map[string]any{}})
	case strings.HasSuffix(r.URL.Path, "/su_oss_chamado") && listing:
		if body["query"] == "9001" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "success", "total": 1,
				"registros": []map[string]any{{
					"id": "9001", "id_cliente": "4471", "titulo": "Sem sinal",
					"status": "EN", "data_abertura": "2026-09-20 03:00:00",
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "success", "total": 0, "registros": []map[string]any{}})
	case strings.HasSuffix(r.URL.Path, "/su_oss_chamado") && r.Method == http.MethodPost:
		f.creates.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "success", "id": "9001"})
	case strings.Contains(r.URL.Path, "/su_oss_chamado/") && r.Method == http.MethodPut:
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "success"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newIXC(t *testing.T, baseURL string) *IXCConnector {
	t.Helper()
	c, err := NewIXCConnector(IXCConfig{BaseURL: baseURL, User: "user", Token: "token", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIXCRequiresCompleteConfiguration(t *testing.T) {
	for _, cfg := range []IXCConfig{
		{},
		{BaseURL: "https://erp.example", User: "u"},
		{BaseURL: "https://erp.example", Token: "t"},
		{BaseURL: "erp.example", User: "u", Token: "t"}, // sem esquema
	} {
		if _, err := NewIXCConnector(cfg); !errors.Is(err, ErrIXCNotConfigured) {
			t.Fatalf("configuração incompleta aceita: %+v (err=%v)", cfg, err)
		}
	}
}

func TestIXCFindCustomerByPhoneAndDocument(t *testing.T) {
	fake := &fakeIXC{}
	c := newIXC(t, fake.server(t).URL)
	ctx := context.Background()

	// O telefone chega formatado do WhatsApp; o adapter normaliza.
	id, err := c.FindCustomer(ctx, "+55 (92) 99174-0090")
	if err != nil {
		t.Fatal(err)
	}
	if id != "4471" {
		t.Fatalf("customer id inesperado: %q", id)
	}

	if _, err := c.FindCustomer(ctx, "123.456.789-01"); err != nil {
		t.Fatalf("busca por documento falhou: %v", err)
	}

	if _, err := c.FindCustomer(ctx, "+5511000000000"); !errors.Is(err, ErrIXCNotFound) {
		t.Fatalf("assinante inexistente deveria ser not found: %v", err)
	}
	if _, err := c.FindCustomer(ctx, "sem digitos"); !errors.Is(err, ErrIXCRejected) {
		t.Fatalf("consulta vazia deveria ser rejeitada: %v", err)
	}
}

func TestIXCTicketLifecycleTranslatesStatus(t *testing.T) {
	fake := &fakeIXC{}
	c := newIXC(t, fake.server(t).URL)
	ctx := context.Background()

	id, err := c.CreateTicket(ctx, "4471", "Sem sinal desde ontem")
	if err != nil {
		t.Fatal(err)
	}
	if id != "9001" {
		t.Fatalf("ticket id inesperado: %q", id)
	}

	ticket, err := c.GetTicket(ctx, "9001")
	if err != nil {
		t.Fatal(err)
	}
	// O domínio não conhece "EN": a tradução é responsabilidade do adapter.
	if ticket.Status != "in_progress" {
		t.Fatalf("status não traduzido: %q", ticket.Status)
	}
	if ticket.CustomerID != "4471" || ticket.Subject != "Sem sinal" {
		t.Fatalf("ticket mal mapeado: %+v", ticket)
	}
	if ticket.CreatedAt.IsZero() {
		t.Fatal("data de abertura não parseada")
	}

	if err := c.UpdateTicket(ctx, "9001", "in_progress"); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseTicket(ctx, "9001"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateTicket(ctx, "9001", "estado_inventado"); !errors.Is(err, ErrIXCRejected) {
		t.Fatalf("status desconhecido deveria ser rejeitado: %v", err)
	}
	if _, err := c.GetTicket(ctx, "0000"); !errors.Is(err, ErrIXCNotFound) {
		t.Fatalf("ticket inexistente deveria ser not found: %v", err)
	}
}

// Consultas podem ser repetidas com segurança; abrir chamado não. Sem chave de
// idempotência no IXC, um retry cego abriria um segundo chamado para o mesmo
// cliente — ruído no ERP e no atendimento.
func TestIXCRetriesReadsButNeverDuplicatesTicket(t *testing.T) {
	ctx := context.Background()

	var listAttempts atomic.Int32
	flaky := &fakeIXC{}
	flaky.handler = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Header.Get("ixcsoft") == "listar" {
			if listAttempts.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "success", "total": 1,
				"registros": []map[string]any{{"id": "4471"}},
			})
			return
		}
		flaky.creates.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	c := newIXC(t, flaky.server(t).URL)

	if _, err := c.FindCustomer(ctx, "5592991740090"); err != nil {
		t.Fatalf("leitura não se recuperou de 503 transitório: %v", err)
	}
	if got := listAttempts.Load(); got != 3 {
		t.Fatalf("esperava 3 tentativas de leitura, houve %d", got)
	}

	_, err := c.CreateTicket(ctx, "4471", "assunto")
	if !errors.Is(err, ErrIXCUnavailable) {
		t.Fatalf("erro de criação deveria ser transitório e propagado: %v", err)
	}
	if got := flaky.creates.Load(); got != 1 {
		t.Fatalf("abertura de chamado foi repetida %d vezes — duplicaria o chamado do cliente", got)
	}
}

func TestIXCMapsProviderErrorsToIntent(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"credencial invalida", http.StatusUnauthorized, ErrIXCNotConfigured},
		{"sem permissao", http.StatusForbidden, ErrIXCNotConfigured},
		{"recurso ausente", http.StatusNotFound, ErrIXCNotFound},
		{"payload invalido", http.StatusBadRequest, ErrIXCRejected},
		{"rate limit", http.StatusTooManyRequests, ErrIXCUnavailable},
		{"erro do erp", http.StatusBadGateway, ErrIXCUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeIXC{}
			fake.handler = func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(tc.status)
			}
			c := newIXC(t, fake.server(t).URL)
			_, err := c.GetTicket(ctx, "9001")
			if !errors.Is(err, tc.want) {
				t.Fatalf("status %d → %v, esperado %v", tc.status, err, tc.want)
			}
		})
	}
}

// A credencial não pode vazar: ela identifica o provedor inteiro no ERP.
func TestIXCNeverLeaksCredentialInErrors(t *testing.T) {
	fake := &fakeIXC{}
	fake.handler = func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusInternalServerError)
	}
	c := newIXC(t, fake.server(t).URL)
	_, err := c.GetTicket(context.Background(), "9001")
	if err == nil {
		t.Fatal("esperava erro")
	}
	if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "Basic") {
		t.Fatalf("credencial vazou na mensagem de erro: %v", err)
	}
}

func TestIXCSatisfiesCRMPort(t *testing.T) {
	var _ CRMConnector = (*IXCConnector)(nil)
}
