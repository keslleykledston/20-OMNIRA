package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/application"
	"github.com/omnira/omnira/internal/bpo/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestAuditHandlersCompile(t *testing.T) {
	auditRepo := application.NewMockAuditRepository()
	auditService := application.NewAuditService(auditRepo)
	_ = NewAuditHandlers(auditService)
}

func TestGetAccountAuditTrailHandler(t *testing.T) {
	auditRepo := application.NewMockAuditRepository()
	auditService := application.NewAuditService(auditRepo)
	handlers := NewAuditHandlers(auditService)

	tenantCtx := createTenantContext()
	accountID := uuid.New()

	// Log um evento
	account := domain.NewAccount(tenantCtx.TenantID, uuid.New(), tenantCtx.ActorID, "Support", "", domain.AccountTypeOperator, 50, 1000, domain.SLAConfiguration{})
	account.ID = accountID
	auditService.LogAccountCreated(context.Background(), account, tenantCtx.ActorID)

	req := httptest.NewRequest("GET", "/api/v1/accounts/"+accountID.String()+"/audit", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", accountID.String())
	rec := httptest.NewRecorder()

	handlers.GetAccountAuditTrail(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var trail []application.AuditTrailEntry
	if err := json.NewDecoder(rec.Body).Decode(&trail); err != nil {
		t.Errorf("failed to decode trail: %v", err)
	}

	if len(trail) != 1 {
		t.Errorf("expected 1 entry, got %d", len(trail))
	}
}

func TestGetTicketAuditTrailHandler(t *testing.T) {
	auditRepo := application.NewMockAuditRepository()
	auditService := application.NewAuditService(auditRepo)
	handlers := NewAuditHandlers(auditService)

	tenantCtx := createTenantContext()
	accountID := uuid.New()
	ticketID := uuid.New()

	// Log um evento
	ticket := domain.NewTicket(accountID, tenantCtx.TenantID, uuid.New(), "Test", "", domain.TicketPriorityMedium, domain.SLAConfiguration{})
	ticket.ID = ticketID
	auditService.LogTicketCreated(context.Background(), ticket, tenantCtx.ActorID)

	req := httptest.NewRequest("GET", "/api/v1/accounts/"+accountID.String()+"/tickets/"+ticketID.String()+"/audit", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", accountID.String())
	req.SetPathValue("ticketId", ticketID.String())
	rec := httptest.NewRecorder()

	handlers.GetTicketAuditTrail(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var trail []application.AuditTrailEntry
	if err := json.NewDecoder(rec.Body).Decode(&trail); err != nil {
		t.Errorf("failed to decode trail: %v", err)
	}

	if len(trail) != 1 {
		t.Errorf("expected 1 entry, got %d", len(trail))
	}
}
