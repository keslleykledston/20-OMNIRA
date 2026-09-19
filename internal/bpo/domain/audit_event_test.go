package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewAuditEvent(t *testing.T) {
	tenantID := uuid.New()
	accountID := uuid.New()
	actorID := uuid.New()

	event := NewAuditEvent(
		tenantID, accountID, actorID,
		AuditActionAccountCreated,
		"account", accountID.String(),
		nil,
		map[string]interface{}{"name": "Support"},
		"initial creation",
	)

	if event.ID == uuid.Nil {
		t.Errorf("event id should not be nil")
	}

	if event.TenantID != tenantID {
		t.Errorf("tenant id should match")
	}

	if event.Action != AuditActionAccountCreated {
		t.Errorf("action should be account_created")
	}
}

func TestNewAccountAuditEvent(t *testing.T) {
	tenantID := uuid.New()
	accountID := uuid.New()
	actorID := uuid.New()

	event := NewAccountAuditEvent(
		tenantID, accountID, actorID,
		AuditActionAccountSuspended,
		map[string]interface{}{"status": "active"},
		map[string]interface{}{"status": "suspended"},
		"payment overdue",
	)

	if event.EntityType != "account" {
		t.Errorf("entity type should be account")
	}

	if event.OldValues["status"] != "active" {
		t.Errorf("old status should be active")
	}

	if event.NewValues["status"] != "suspended" {
		t.Errorf("new status should be suspended")
	}
}

func TestNewTicketAuditEvent(t *testing.T) {
	tenantID := uuid.New()
	accountID := uuid.New()
	actorID := uuid.New()
	ticketID := uuid.New()

	event := NewTicketAuditEvent(
		tenantID, accountID, actorID,
		ticketID,
		AuditActionTicketCreated,
		nil,
		map[string]interface{}{"subject": "API Error"},
		"customer request",
	)

	if event.EntityType != "ticket" {
		t.Errorf("entity type should be ticket")
	}

	if event.TicketID == nil || *event.TicketID != ticketID {
		t.Errorf("ticket id should match")
	}
}
