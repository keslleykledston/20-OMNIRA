package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestSupervisorDashboardHandlersCompile(t *testing.T) {
	roleRepo := application.NewMockSupervisorRoleRepository()
	assignmentRepo := application.NewMockSupervisorAssignmentRepository()
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()

	supervisorSvc := application.NewSupervisorService(roleRepo, assignmentRepo)
	bpoSvc := application.NewBPOService(accountRepo, ticketRepo)
	slaSvc := application.NewSLAService(ticketRepo)
	dashboardSvc := application.NewSupervisorDashboardService(supervisorSvc, bpoSvc, slaSvc, assignmentRepo, accountRepo, ticketRepo)

	_ = NewSupervisorDashboardHandlers(dashboardSvc)
}

func TestGetDashboardHandler(t *testing.T) {
	roleRepo := application.NewMockSupervisorRoleRepository()
	assignmentRepo := application.NewMockSupervisorAssignmentRepository()
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()

	supervisorSvc := application.NewSupervisorService(roleRepo, assignmentRepo)
	bpoSvc := application.NewBPOService(accountRepo, ticketRepo)
	slaSvc := application.NewSLAService(ticketRepo)
	dashboardSvc := application.NewSupervisorDashboardService(supervisorSvc, bpoSvc, slaSvc, assignmentRepo, accountRepo, ticketRepo)
	handlers := NewSupervisorDashboardHandlers(dashboardSvc)

	tenantCtx := createTenantContext()

	req := httptest.NewRequest("GET", "/api/v1/supervisor/dashboard", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	rec := httptest.NewRecorder()

	handlers.GetDashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var dashboard application.SupervisorDashboard
	if err := json.NewDecoder(rec.Body).Decode(&dashboard); err != nil {
		t.Errorf("failed to decode dashboard: %v", err)
	}

	if dashboard.UserID != tenantCtx.ActorID {
		t.Errorf("user id should match")
	}
}

func TestGetAlertsHandler(t *testing.T) {
	roleRepo := application.NewMockSupervisorRoleRepository()
	assignmentRepo := application.NewMockSupervisorAssignmentRepository()
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()

	supervisorSvc := application.NewSupervisorService(roleRepo, assignmentRepo)
	bpoSvc := application.NewBPOService(accountRepo, ticketRepo)
	slaSvc := application.NewSLAService(ticketRepo)
	dashboardSvc := application.NewSupervisorDashboardService(supervisorSvc, bpoSvc, slaSvc, assignmentRepo, accountRepo, ticketRepo)
	handlers := NewSupervisorDashboardHandlers(dashboardSvc)

	tenantCtx := createTenantContext()

	req := httptest.NewRequest("GET", "/api/v1/supervisor/alerts", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	rec := httptest.NewRecorder()

	handlers.GetAlerts(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var alerts []application.SupervisorAlert
	if err := json.NewDecoder(rec.Body).Decode(&alerts); err != nil {
		t.Errorf("failed to decode alerts: %v", err)
	}
}

func TestGetAccountDetailsHandler(t *testing.T) {
	roleRepo := application.NewMockSupervisorRoleRepository()
	assignmentRepo := application.NewMockSupervisorAssignmentRepository()
	accountRepo := application.NewMockAccountRepository()
	ticketRepo := application.NewMockTicketRepository()

	supervisorSvc := application.NewSupervisorService(roleRepo, assignmentRepo)
	bpoSvc := application.NewBPOService(accountRepo, ticketRepo)
	slaSvc := application.NewSLAService(ticketRepo)
	dashboardSvc := application.NewSupervisorDashboardService(supervisorSvc, bpoSvc, slaSvc, assignmentRepo, accountRepo, ticketRepo)
	handlers := NewSupervisorDashboardHandlers(dashboardSvc)

	tenantCtx := createTenantContext()
	accountID := uuid.New()

	req := httptest.NewRequest("GET", "/api/v1/supervisor/accounts/"+accountID.String(), nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", accountID.String())
	rec := httptest.NewRecorder()

	handlers.GetAccountDetails(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var details map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&details); err != nil {
		t.Errorf("failed to decode details: %v", err)
	}

	if details["account_id"] != accountID.String() {
		t.Errorf("account id should match")
	}
}
