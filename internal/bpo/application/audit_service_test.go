package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
)

func TestLogAccountCreated(t *testing.T) {
	auditRepo := NewMockAuditRepository()
	svc := NewAuditService(auditRepo)

	ctx := context.Background()
	tenantID := uuid.New()
	actorID := uuid.New()

	account := domain.NewAccount(tenantID, uuid.New(), actorID, "Support", "", domain.AccountTypeOperator, 50, 1000, domain.SLAConfiguration{})

	err := svc.LogAccountCreated(ctx, account, actorID)

	if err != nil {
		t.Errorf("failed to log account created: %v", err)
	}

	if len(auditRepo.events) != 1 {
		t.Errorf("expected 1 event, got %d", len(auditRepo.events))
	}

	event := auditRepo.events[0]
	if event.Action != domain.AuditActionAccountCreated {
		t.Errorf("expected account_created action, got %s", event.Action)
	}
}

func TestLogTicketCreated(t *testing.T) {
	auditRepo := NewMockAuditRepository()
	svc := NewAuditService(auditRepo)

	ctx := context.Background()
	accountID := uuid.New()
	tenantID := uuid.New()
	actorID := uuid.New()

	ticket := domain.NewTicket(accountID, tenantID, uuid.New(), "Test", "", domain.TicketPriorityMedium, domain.SLAConfiguration{})

	err := svc.LogTicketCreated(ctx, ticket, actorID)

	if err != nil {
		t.Errorf("failed to log ticket created: %v", err)
	}

	if len(auditRepo.events) != 1 {
		t.Errorf("expected 1 event, got %d", len(auditRepo.events))
	}

	event := auditRepo.events[0]
	if event.Action != domain.AuditActionTicketCreated {
		t.Errorf("expected ticket_created action, got %s", event.Action)
	}
}

func TestGetAccountAuditTrail(t *testing.T) {
	auditRepo := NewMockAuditRepository()
	svc := NewAuditService(auditRepo)

	ctx := context.Background()
	tenantID := uuid.New()
	actorID := uuid.New()

	account := domain.NewAccount(tenantID, uuid.New(), actorID, "Support", "", domain.AccountTypeOperator, 50, 1000, domain.SLAConfiguration{})

	svc.LogAccountCreated(ctx, account, actorID)

	trail, err := svc.GetAccountAuditTrail(ctx, account.ID, 100, 0)

	if err != nil {
		t.Errorf("failed to get audit trail: %v", err)
	}

	if len(trail) != 1 {
		t.Errorf("expected 1 entry, got %d", len(trail))
	}

	if trail[0].Action != "account_created" {
		t.Errorf("expected account_created action, got %s", trail[0].Action)
	}
}
