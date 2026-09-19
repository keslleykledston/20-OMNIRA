package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/application"
	"github.com/omnira/omnira/internal/bpo/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestBPOHandlersCompile(t *testing.T) {
	// Verifica que handlers compilam corretamente
	svc := application.NewBPOService(
		application.NewMockAccountRepository(),
		application.NewMockTicketRepository(),
	)
	_ = NewBPOHandlers(svc)
}

func createTenantContext() *tenancydomain.TenantContext {
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	return tc
}

func TestCreateAccountHandler(t *testing.T) {
	svc := application.NewBPOService(
		application.NewMockAccountRepository(),
		application.NewMockTicketRepository(),
	)
	handlers := NewBPOHandlers(svc)

	tenantCtx := createTenantContext()

	body := `{
		"name": "Support Team",
		"description": "Main support",
		"type": "operator",
		"max_team_members": 50,
		"max_tickets_month": 1000,
		"sla_config": {
			"first_response_time": 30,
			"resolution_time": 24
		}
	}`

	req := httptest.NewRequest("POST", "/api/v1/accounts", strings.NewReader(body))
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	rec := httptest.NewRecorder()

	handlers.CreateAccount(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}

	var resp AccountResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Errorf("failed to decode response: %v", err)
	}

	if resp.Name != "Support Team" {
		t.Errorf("expected name 'Support Team', got %s", resp.Name)
	}
}

func TestGetAccountHandler(t *testing.T) {
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()
	svc := application.NewBPOService(accountRepo, ticketRepo)
	handlers := NewBPOHandlers(svc)

	tenantCtx := createTenantContext()

	// Criar account primeiro
	account, _ := svc.CreateAccount(
		context.Background(),
		tenantCtx.TenantID,
		uuid.New(),
		tenantCtx.ActorID,
		"Support",
		"",
		domain.AccountTypeOperator,
		50,
		1000,
		domain.SLAConfiguration{},
	)

	req := httptest.NewRequest("GET", "/api/v1/accounts/"+account.ID.String(), nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", account.ID.String())
	rec := httptest.NewRecorder()

	handlers.GetAccount(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp AccountResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Errorf("failed to decode response: %v", err)
	}

	if resp.Name != "Support" {
		t.Errorf("expected name 'Support', got %s", resp.Name)
	}
}

func TestListAccountsHandler(t *testing.T) {
	svc := application.NewBPOService(
		application.NewMockAccountRepository(),
		application.NewMockTicketRepository(),
	)
	handlers := NewBPOHandlers(svc)

	tenantCtx := createTenantContext()

	req := httptest.NewRequest("GET", "/api/v1/accounts", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	rec := httptest.NewRecorder()

	handlers.ListAccounts(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var respList []AccountResponse
	if err := json.NewDecoder(rec.Body).Decode(&respList); err != nil {
		t.Errorf("failed to decode response: %v", err)
	}
}

func TestCreateTicketHandler(t *testing.T) {
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()
	svc := application.NewBPOService(accountRepo, ticketRepo)
	handlers := NewBPOHandlers(svc)

	tenantCtx := createTenantContext()

	// Criar account primeiro
	account, _ := svc.CreateAccount(
		context.Background(),
		tenantCtx.TenantID,
		uuid.New(),
		tenantCtx.ActorID,
		"Support",
		"",
		domain.AccountTypeOperator,
		50,
		1000,
		domain.SLAConfiguration{},
	)

	body := `{
		"subject": "API Error",
		"description": "500 error",
		"priority": "high"
	}`

	req := httptest.NewRequest("POST", "/api/v1/accounts/"+account.ID.String()+"/tickets", strings.NewReader(body))
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", account.ID.String())
	rec := httptest.NewRecorder()

	handlers.CreateTicket(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}

	var resp TicketResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Errorf("failed to decode response: %v", err)
	}

	if resp.Subject != "API Error" {
		t.Errorf("expected subject 'API Error', got %s", resp.Subject)
	}
}

func TestAssignTicketHandler(t *testing.T) {
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()
	svc := application.NewBPOService(accountRepo, ticketRepo)
	handlers := NewBPOHandlers(svc)

	tenantCtx := createTenantContext()

	// Criar account e ticket
	account, _ := svc.CreateAccount(
		context.Background(),
		tenantCtx.TenantID,
		uuid.New(),
		tenantCtx.ActorID,
		"Support",
		"",
		domain.AccountTypeOperator,
		50,
		1000,
		domain.SLAConfiguration{},
	)

	ticket, _ := svc.CreateTicket(
		context.Background(),
		account.ID,
		tenantCtx.TenantID,
		tenantCtx.ActorID,
		"Test",
		"",
		domain.TicketPriorityMedium,
	)

	agentID := uuid.New()
	body := `{"agent_id": "` + agentID.String() + `"}`

	req := httptest.NewRequest("POST", "/api/v1/accounts/"+account.ID.String()+"/tickets/"+ticket.ID.String()+"/assign", strings.NewReader(body))
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", account.ID.String())
	req.SetPathValue("ticketId", ticket.ID.String())
	rec := httptest.NewRecorder()

	handlers.AssignTicket(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestResolveTicketHandler(t *testing.T) {
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()
	svc := application.NewBPOService(accountRepo, ticketRepo)
	handlers := NewBPOHandlers(svc)

	tenantCtx := createTenantContext()

	// Criar account e ticket
	account, _ := svc.CreateAccount(
		context.Background(),
		tenantCtx.TenantID,
		uuid.New(),
		tenantCtx.ActorID,
		"Support",
		"",
		domain.AccountTypeOperator,
		50,
		1000,
		domain.SLAConfiguration{},
	)

	ticket, _ := svc.CreateTicket(
		context.Background(),
		account.ID,
		tenantCtx.TenantID,
		tenantCtx.ActorID,
		"Test",
		"",
		domain.TicketPriorityLow,
	)

	req := httptest.NewRequest("POST", "/api/v1/accounts/"+account.ID.String()+"/tickets/"+ticket.ID.String()+"/resolve", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", account.ID.String())
	req.SetPathValue("ticketId", ticket.ID.String())
	rec := httptest.NewRecorder()

	handlers.ResolveTicket(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestGetMetricsHandler(t *testing.T) {
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()
	svc := application.NewBPOService(accountRepo, ticketRepo)
	handlers := NewBPOHandlers(svc)

	tenantCtx := createTenantContext()

	// Criar account
	account, _ := svc.CreateAccount(
		context.Background(),
		tenantCtx.TenantID,
		uuid.New(),
		tenantCtx.ActorID,
		"Support",
		"",
		domain.AccountTypeOperator,
		50,
		1000,
		domain.SLAConfiguration{},
	)

	req := httptest.NewRequest("GET", "/api/v1/accounts/"+account.ID.String()+"/metrics", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", account.ID.String())
	rec := httptest.NewRecorder()

	handlers.GetMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var metrics map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&metrics); err != nil {
		t.Errorf("failed to decode metrics: %v", err)
	}

	if _, ok := metrics["open"]; !ok {
		t.Errorf("metrics should contain 'open'")
	}
}
