package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
)

// Tests

func TestCreateAccount(t *testing.T) {
	accountRepo := NewMockAccountRepository()
	ticketRepo := NewMockTicketRepository()
	svc := NewBPOService(accountRepo, ticketRepo)

	tenantID := uuid.New()
	operatorID := uuid.New()
	createdBy := uuid.New()

	slaConfig := domain.SLAConfiguration{
		FirstResponseTime: 30,
		ResolutionTime:    24,
	}

	account, err := svc.CreateAccount(ctx, tenantID, operatorID, createdBy, "Support", "Main support account", domain.AccountTypeOperator, 50, 1000, slaConfig)

	if err != nil {
		t.Errorf("create account failed: %v", err)
	}

	if account.Status != domain.AccountStatusActive {
		t.Errorf("account should be active")
	}
}

func TestCreateTicket(t *testing.T) {
	accountRepo := NewMockAccountRepository()
	ticketRepo := NewMockTicketRepository()
	svc := NewBPOService(accountRepo, ticketRepo)

	ctx := context.Background()
	tenantID := uuid.New()
	operatorID := uuid.New()
	createdBy := uuid.New()

	slaConfig := domain.SLAConfiguration{
		FirstResponseTime: 30,
		ResolutionTime:    24,
	}

	// Create account first
	account, _ := svc.CreateAccount(ctx, tenantID, operatorID, createdBy, "Support", "", domain.AccountTypeOperator, 50, 1000, slaConfig)

	// Create ticket
	customerID := uuid.New()
	ticket, err := svc.CreateTicket(ctx, account.ID, tenantID, customerID, "API Error", "500 error", domain.TicketPriorityHigh)

	if err != nil {
		t.Errorf("create ticket failed: %v", err)
	}

	if ticket.Status != domain.TicketStatusOpen {
		t.Errorf("ticket should be open")
	}
}

func TestAssignTicket(t *testing.T) {
	accountRepo := NewMockAccountRepository()
	ticketRepo := NewMockTicketRepository()
	svc := NewBPOService(accountRepo, ticketRepo)

	ctx := context.Background()

	// Create account and ticket
	tenantID := uuid.New()
	operatorID := uuid.New()
	createdBy := uuid.New()

	account, _ := svc.CreateAccount(ctx, tenantID, operatorID, createdBy, "Support", "", domain.AccountTypeOperator, 50, 1000, domain.SLAConfiguration{ResolutionTime: 24})
	ticket, _ := svc.CreateTicket(ctx, account.ID, tenantID, uuid.New(), "Test", "", domain.TicketPriorityMedium)

	// Assign ticket
	agentID := uuid.New()
	err := svc.AssignTicket(ctx, ticket.ID, agentID)

	if err != nil {
		t.Errorf("assign failed: %v", err)
	}

	// Verify
	updated, _ := ticketRepo.FindByID(ctx, ticket.ID)
	if updated.Status != domain.TicketStatusInProgress {
		t.Errorf("ticket should be in progress")
	}
}

func TestResolveTicket(t *testing.T) {
	accountRepo := NewMockAccountRepository()
	ticketRepo := NewMockTicketRepository()
	svc := NewBPOService(accountRepo, ticketRepo)

	ctx := context.Background()

	// Create and setup ticket
	account, _ := svc.CreateAccount(ctx, uuid.New(), uuid.New(), uuid.New(), "Support", "", domain.AccountTypeOperator, 50, 1000, domain.SLAConfiguration{})
	ticket, _ := svc.CreateTicket(ctx, account.ID, uuid.New(), uuid.New(), "Test", "", domain.TicketPriorityLow)

	// Resolve
	err := svc.ResolveTicket(ctx, ticket.ID)

	if err != nil {
		t.Errorf("resolve failed: %v", err)
	}

	// Verify
	updated, _ := ticketRepo.FindByID(ctx, ticket.ID)
	if updated.Status != domain.TicketStatusResolved {
		t.Errorf("ticket should be resolved")
	}
}

func TestGetAccountMetrics(t *testing.T) {
	accountRepo := NewMockAccountRepository()
	ticketRepo := NewMockTicketRepository()
	svc := NewBPOService(accountRepo, ticketRepo)

	ctx := context.Background()

	account, _ := svc.CreateAccount(ctx, uuid.New(), uuid.New(), uuid.New(), "Support", "", domain.AccountTypeOperator, 50, 1000, domain.SLAConfiguration{})

	metrics, err := svc.GetAccountMetrics(ctx, account.ID)

	if err != nil {
		t.Errorf("get metrics failed: %v", err)
	}

	if metrics["open"] != 0 {
		t.Errorf("should have 0 open tickets initially")
	}
}

var ctx = context.Background()
