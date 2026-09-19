package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
	"github.com/omnira/omnira/internal/bpo/ports"
)

// AuditService — serviço para auditoria de operações BPO
type AuditService struct {
	auditRepo ports.AuditRepository
}

// NewAuditService — cria novo AuditService
func NewAuditService(auditRepo ports.AuditRepository) *AuditService {
	return &AuditService{
		auditRepo: auditRepo,
	}
}

// LogAccountCreated — registra criação de account
func (s *AuditService) LogAccountCreated(ctx context.Context, account *domain.Account, actorID uuid.UUID) error {
	event := domain.NewAccountAuditEvent(
		account.TenantID,
		account.ID,
		actorID,
		domain.AuditActionAccountCreated,
		nil,
		map[string]interface{}{
			"name":  account.Name,
			"type":  account.Type,
			"status": account.Status,
		},
		"account created",
	)

	return s.auditRepo.Store(ctx, event)
}

// LogAccountSuspended — registra suspensão de account
func (s *AuditService) LogAccountSuspended(ctx context.Context, account *domain.Account, actorID uuid.UUID, reason string) error {
	event := domain.NewAccountAuditEvent(
		account.TenantID,
		account.ID,
		actorID,
		domain.AuditActionAccountSuspended,
		map[string]interface{}{"status": "active"},
		map[string]interface{}{"status": account.Status},
		reason,
	)

	return s.auditRepo.Store(ctx, event)
}

// LogAccountReactivated — registra reativação de account
func (s *AuditService) LogAccountReactivated(ctx context.Context, account *domain.Account, actorID uuid.UUID) error {
	event := domain.NewAccountAuditEvent(
		account.TenantID,
		account.ID,
		actorID,
		domain.AuditActionAccountReactivated,
		map[string]interface{}{"status": "suspended"},
		map[string]interface{}{"status": "active"},
		"account reactivated",
	)

	return s.auditRepo.Store(ctx, event)
}

// LogTicketCreated — registra criação de ticket
func (s *AuditService) LogTicketCreated(ctx context.Context, ticket *domain.Ticket, actorID uuid.UUID) error {
	event := domain.NewTicketAuditEvent(
		ticket.TenantID,
		ticket.AccountID,
		actorID,
		ticket.ID,
		domain.AuditActionTicketCreated,
		nil,
		map[string]interface{}{
			"subject":  ticket.Subject,
			"priority": ticket.Priority,
			"status":   ticket.Status,
		},
		"ticket created",
	)

	return s.auditRepo.Store(ctx, event)
}

// LogTicketAssigned — registra atribuição de ticket
func (s *AuditService) LogTicketAssigned(ctx context.Context, ticket *domain.Ticket, actorID uuid.UUID) error {
	oldAssignedTo := "unassigned"
	if ticket.AssignedToID != nil {
		oldAssignedTo = ticket.AssignedToID.String()
	}

	newAssignedTo := "unassigned"
	if ticket.AssignedToID != nil {
		newAssignedTo = ticket.AssignedToID.String()
	}

	event := domain.NewTicketAuditEvent(
		ticket.TenantID,
		ticket.AccountID,
		actorID,
		ticket.ID,
		domain.AuditActionTicketAssigned,
		map[string]interface{}{"assigned_to": oldAssignedTo},
		map[string]interface{}{"assigned_to": newAssignedTo},
		"ticket assigned to agent",
	)

	return s.auditRepo.Store(ctx, event)
}

// LogFirstResponse — registra primeira resposta
func (s *AuditService) LogFirstResponse(ctx context.Context, ticket *domain.Ticket, actorID uuid.UUID) error {
	event := domain.NewTicketAuditEvent(
		ticket.TenantID,
		ticket.AccountID,
		actorID,
		ticket.ID,
		domain.AuditActionFirstResponse,
		nil,
		map[string]interface{}{"first_response_at": ticket.FirstResponseAt},
		"first response recorded",
	)

	return s.auditRepo.Store(ctx, event)
}

// LogTicketResolved — registra resolução de ticket
func (s *AuditService) LogTicketResolved(ctx context.Context, ticket *domain.Ticket, actorID uuid.UUID) error {
	event := domain.NewTicketAuditEvent(
		ticket.TenantID,
		ticket.AccountID,
		actorID,
		ticket.ID,
		domain.AuditActionTicketResolved,
		map[string]interface{}{"status": "in_progress"},
		map[string]interface{}{"status": ticket.Status},
		"ticket resolved",
	)

	return s.auditRepo.Store(ctx, event)
}

// LogTicketClosed — registra fechamento de ticket
func (s *AuditService) LogTicketClosed(ctx context.Context, ticket *domain.Ticket, actorID uuid.UUID) error {
	event := domain.NewTicketAuditEvent(
		ticket.TenantID,
		ticket.AccountID,
		actorID,
		ticket.ID,
		domain.AuditActionTicketClosed,
		map[string]interface{}{"status": "resolved"},
		map[string]interface{}{"status": ticket.Status},
		"ticket closed",
	)

	return s.auditRepo.Store(ctx, event)
}

// GetAuditTrail — obtém histórico de auditoria
type AuditTrailEntry struct {
	ID        string                 `json:"id"`
	Action    string                 `json:"action"`
	EntityType string                `json:"entity_type"`
	EntityID  string                 `json:"entity_id"`
	ActorID   string                 `json:"actor_id"`
	OldValues map[string]interface{} `json:"old_values"`
	NewValues map[string]interface{} `json:"new_values"`
	Reason    string                 `json:"reason"`
	CreatedAt string                 `json:"created_at"`
}

// GetAccountAuditTrail — obtém trail de auditoria da account
func (s *AuditService) GetAccountAuditTrail(ctx context.Context, accountID domain.AccountID, limit, offset int) ([]AuditTrailEntry, error) {
	events, err := s.auditRepo.FindByAccount(ctx, accountID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to get audit trail: %w", err)
	}

	var entries []AuditTrailEntry
	for _, event := range events {
		entries = append(entries, AuditTrailEntry{
			ID:        event.ID.String(),
			Action:    string(event.Action),
			EntityType: event.EntityType,
			EntityID:  event.EntityID,
			ActorID:   event.ActorID.String(),
			OldValues: event.OldValues,
			NewValues: event.NewValues,
			Reason:    event.Reason,
			CreatedAt: event.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	return entries, nil
}

// GetTicketAuditTrail — obtém trail de auditoria do ticket
func (s *AuditService) GetTicketAuditTrail(ctx context.Context, ticketID domain.TicketID, limit, offset int) ([]AuditTrailEntry, error) {
	events, err := s.auditRepo.FindByTicket(ctx, ticketID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to get audit trail: %w", err)
	}

	var entries []AuditTrailEntry
	for _, event := range events {
		entries = append(entries, AuditTrailEntry{
			ID:        event.ID.String(),
			Action:    string(event.Action),
			EntityType: event.EntityType,
			EntityID:  event.EntityID,
			ActorID:   event.ActorID.String(),
			OldValues: event.OldValues,
			NewValues: event.NewValues,
			Reason:    event.Reason,
			CreatedAt: event.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	return entries, nil
}
