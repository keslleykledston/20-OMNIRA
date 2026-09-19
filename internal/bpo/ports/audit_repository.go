package ports

import (
	"context"

	"github.com/omnira/omnira/internal/bpo/domain"
)

// AuditRepository — interface para operações com eventos de auditoria
type AuditRepository interface {
	// Store — salva novo evento de auditoria
	Store(ctx context.Context, event *domain.AuditEvent) error

	// FindByAccount — lista eventos de uma account
	FindByAccount(ctx context.Context, accountID domain.AccountID, limit, offset int) ([]*domain.AuditEvent, error)

	// FindByTicket — lista eventos de um ticket
	FindByTicket(ctx context.Context, ticketID domain.TicketID, limit, offset int) ([]*domain.AuditEvent, error)

	// FindByAction — lista eventos por ação
	FindByAction(ctx context.Context, accountID domain.AccountID, action domain.AuditAction, limit, offset int) ([]*domain.AuditEvent, error)

	// CountByAccount — conta eventos de uma account
	CountByAccount(ctx context.Context, accountID domain.AccountID) (int, error)
}
