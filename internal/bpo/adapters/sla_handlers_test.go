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

func TestSLAHandlersCompile(t *testing.T) {
	ticketRepo := application.NewMockTicketRepository()
	slaService := application.NewSLAService(ticketRepo)
	_ = NewSLAHandlers(slaService)
}

func TestGetSLAComplianceReportHandler(t *testing.T) {
	ticketRepo := application.NewMockTicketRepository()
	slaService := application.NewSLAService(ticketRepo)
	handlers := NewSLAHandlers(slaService)

	tenantCtx := createTenantContext()
	accountID := uuid.New()

	// Criar ticket
	ticket := domain.NewTicket(accountID, tenantCtx.TenantID, uuid.New(), "Test", "", domain.TicketPriorityMedium, domain.SLAConfiguration{})
	ticketRepo.Store(context.Background(), ticket)

	req := httptest.NewRequest("GET", "/api/v1/accounts/"+accountID.String()+"/sla/compliance", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", accountID.String())
	rec := httptest.NewRecorder()

	handlers.GetSLAComplianceReport(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var report application.SLAComplianceReport
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Errorf("failed to decode report: %v", err)
	}

	if report.TotalTickets != 1 {
		t.Errorf("expected 1 ticket, got %d", report.TotalTickets)
	}
}

func TestGetSLASummaryHandler(t *testing.T) {
	ticketRepo := application.NewMockTicketRepository()
	slaService := application.NewSLAService(ticketRepo)
	handlers := NewSLAHandlers(slaService)

	tenantCtx := createTenantContext()
	accountID := uuid.New()

	req := httptest.NewRequest("GET", "/api/v1/accounts/"+accountID.String()+"/sla/summary", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", accountID.String())
	rec := httptest.NewRecorder()

	handlers.GetSLASummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var summary application.SLASummary
	if err := json.NewDecoder(rec.Body).Decode(&summary); err != nil {
		t.Errorf("failed to decode summary: %v", err)
	}

	if summary.Status != "compliant" {
		t.Errorf("expected compliant status, got %s", summary.Status)
	}
}

func TestExportSLAReportHandler(t *testing.T) {
	ticketRepo := application.NewMockTicketRepository()
	slaService := application.NewSLAService(ticketRepo)
	handlers := NewSLAHandlers(slaService)

	tenantCtx := createTenantContext()
	accountID := uuid.New()

	req := httptest.NewRequest("GET", "/api/v1/accounts/"+accountID.String()+"/sla/export", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", accountID.String())
	rec := httptest.NewRecorder()

	handlers.ExportSLAReport(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected json content type, got %s", rec.Header().Get("Content-Type"))
	}
}

func TestExportSLAReportCSVHandler(t *testing.T) {
	ticketRepo := application.NewMockTicketRepository()
	slaService := application.NewSLAService(ticketRepo)
	handlers := NewSLAHandlers(slaService)

	tenantCtx := createTenantContext()
	accountID := uuid.New()

	req := httptest.NewRequest("GET", "/api/v1/accounts/"+accountID.String()+"/sla/export?format=csv", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", accountID.String())
	rec := httptest.NewRecorder()

	handlers.ExportSLAReport(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Header().Get("Content-Type") != "text/csv" {
		t.Errorf("expected csv content type, got %s", rec.Header().Get("Content-Type"))
	}
}
