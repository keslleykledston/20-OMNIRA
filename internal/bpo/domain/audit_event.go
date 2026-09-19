package domain

import (
	"time"

	"github.com/google/uuid"
)

// AuditAction — tipo de ação auditada
type AuditAction string

const (
	AuditActionAccountCreated    AuditAction = "account_created"
	AuditActionAccountSuspended  AuditAction = "account_suspended"
	AuditActionAccountReactivated AuditAction = "account_reactivated"
	AuditActionTicketCreated     AuditAction = "ticket_created"
	AuditActionTicketAssigned    AuditAction = "ticket_assigned"
	AuditActionFirstResponse     AuditAction = "first_response"
	AuditActionTicketResolved    AuditAction = "ticket_resolved"
	AuditActionTicketClosed      AuditAction = "ticket_closed"
)

// AuditEvent — evento de auditoria
type AuditEvent struct {
	ID              uuid.UUID              `json:"id"`
	TenantID        uuid.UUID              `json:"tenant_id"`
	AccountID       AccountID              `json:"account_id"`
	TicketID        *TicketID              `json:"ticket_id"`
	ActorID         uuid.UUID              `json:"actor_id"`
	Action          AuditAction            `json:"action"`
	EntityType      string                 `json:"entity_type"` // "account" ou "ticket"
	EntityID        string                 `json:"entity_id"`
	OldValues       map[string]interface{} `json:"old_values"`
	NewValues       map[string]interface{} `json:"new_values"`
	Reason          string                 `json:"reason"`
	CreatedAt       time.Time              `json:"created_at"`
}

// NewAuditEvent — cria novo evento de auditoria
func NewAuditEvent(
	tenantID, accountID, actorID uuid.UUID,
	action AuditAction,
	entityType, entityID string,
	oldValues, newValues map[string]interface{},
	reason string,
) *AuditEvent {
	return &AuditEvent{
		ID:         uuid.New(),
		TenantID:   tenantID,
		AccountID:  accountID,
		ActorID:    actorID,
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		OldValues:  oldValues,
		NewValues:  newValues,
		Reason:     reason,
		CreatedAt:  time.Now().UTC(),
	}
}

// NewAccountAuditEvent — cria evento de auditoria para account
func NewAccountAuditEvent(
	tenantID, accountID, actorID uuid.UUID,
	action AuditAction,
	oldValues, newValues map[string]interface{},
	reason string,
) *AuditEvent {
	return NewAuditEvent(
		tenantID, accountID, actorID,
		action,
		"account", accountID.String(),
		oldValues, newValues,
		reason,
	)
}

// NewTicketAuditEvent — cria evento de auditoria para ticket
func NewTicketAuditEvent(
	tenantID, accountID, actorID uuid.UUID,
	ticketID TicketID,
	action AuditAction,
	oldValues, newValues map[string]interface{},
	reason string,
) *AuditEvent {
	event := NewAuditEvent(
		tenantID, accountID, actorID,
		action,
		"ticket", ticketID.String(),
		oldValues, newValues,
		reason,
	)
	event.TicketID = &ticketID
	return event
}
