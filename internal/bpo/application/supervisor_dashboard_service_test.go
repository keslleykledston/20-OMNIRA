package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
)

func TestGetSupervisorDashboard(t *testing.T) {
	// Setup repos
	roleRepo := NewMockSupervisorRoleRepository()
	assignmentRepo := NewMockSupervisorAssignmentRepository()
	accountRepo := NewMockAccountRepository()
	ticketRepo := NewMockTicketRepository()

	// Setup services
	supervisorSvc := NewSupervisorService(roleRepo, assignmentRepo)
	bpoSvc := NewBPOService(accountRepo, ticketRepo)
	slaSvc := NewSLAService(ticketRepo)
	dashboardSvc := NewSupervisorDashboardService(supervisorSvc, bpoSvc, slaSvc, assignmentRepo, accountRepo, ticketRepo)

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	createdBy := uuid.New()

	// Create supervisor role
	role, _ := supervisorSvc.CreateSupervisorRole(ctx, tenantID, createdBy, "Supervisor", "", []string{domain.PermissionViewAllAccounts})

	// Create account
	account, _ := bpoSvc.CreateAccount(ctx, tenantID, uuid.New(), createdBy, "Support", "", domain.AccountTypeOperator, 50, 1000, domain.SLAConfiguration{})

	// Assign supervisor
	supervisorSvc.AssignSupervisor(ctx, tenantID, userID, role.ID, createdBy, []uuid.UUID{account.ID})

	// Create ticket
	ticket, _ := bpoSvc.CreateTicket(ctx, account.ID, tenantID, uuid.New(), "Test", "", domain.TicketPriorityMedium)
	_ = ticket

	// Get dashboard
	dashboard, err := dashboardSvc.GetSupervisorDashboard(ctx, userID)

	if err != nil {
		t.Errorf("failed to get dashboard: %v", err)
	}

	if dashboard.UserID != userID {
		t.Errorf("user id should match")
	}

	if dashboard.TotalAccounts != 1 {
		t.Errorf("expected 1 account, got %d", dashboard.TotalAccounts)
	}
}

func TestGetSupervisorAlerts(t *testing.T) {
	// Setup repos
	roleRepo := NewMockSupervisorRoleRepository()
	assignmentRepo := NewMockSupervisorAssignmentRepository()
	accountRepo := NewMockAccountRepository()
	ticketRepo := NewMockTicketRepository()

	// Setup services
	supervisorSvc := NewSupervisorService(roleRepo, assignmentRepo)
	bpoSvc := NewBPOService(accountRepo, ticketRepo)
	slaSvc := NewSLAService(ticketRepo)
	dashboardSvc := NewSupervisorDashboardService(supervisorSvc, bpoSvc, slaSvc, assignmentRepo, accountRepo, ticketRepo)

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	createdBy := uuid.New()

	// Create and assign
	role, _ := supervisorSvc.CreateSupervisorRole(ctx, tenantID, createdBy, "Supervisor", "", []string{})
	account, _ := bpoSvc.CreateAccount(ctx, tenantID, uuid.New(), createdBy, "Support", "", domain.AccountTypeOperator, 50, 1000, domain.SLAConfiguration{})
	supervisorSvc.AssignSupervisor(ctx, tenantID, userID, role.ID, createdBy, []uuid.UUID{account.ID})

	// Get alerts
	alerts, err := dashboardSvc.GetSupervisorAlerts(ctx, userID)

	if err != nil {
		t.Errorf("failed to get alerts: %v", err)
	}

	if alerts == nil {
		t.Errorf("alerts should not be nil")
	}
}

func TestGetEmptyDashboard(t *testing.T) {
	roleRepo := NewMockSupervisorRoleRepository()
	assignmentRepo := NewMockSupervisorAssignmentRepository()
	accountRepo := NewMockAccountRepository()
	ticketRepo := NewMockTicketRepository()

	supervisorSvc := NewSupervisorService(roleRepo, assignmentRepo)
	bpoSvc := NewBPOService(accountRepo, ticketRepo)
	slaSvc := NewSLAService(ticketRepo)
	dashboardSvc := NewSupervisorDashboardService(supervisorSvc, bpoSvc, slaSvc, assignmentRepo, accountRepo, ticketRepo)

	userID := uuid.New()

	dashboard, err := dashboardSvc.GetSupervisorDashboard(context.Background(), userID)

	if err != nil {
		t.Errorf("failed to get dashboard: %v", err)
	}

	if dashboard.TotalAccounts != 0 {
		t.Errorf("expected 0 accounts for user with no assignments")
	}
}
