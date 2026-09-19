package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/audit/domain"
)

// AuditEventRepository — interface para persistência de audit events.
type AuditEventRepository interface {
	// Store — persiste um evento de auditoria.
	Store(ctx context.Context, event *domain.AuditEvent) error

	// FindByID — busca um evento pelo ID (com RLS).
	FindByID(ctx context.Context, id uuid.UUID) (*domain.AuditEvent, error)

	// FindByTenantAndCorrelation — busca eventos por tenant + correlation ID.
	FindByTenantAndCorrelation(ctx context.Context, tenantID, correlationID uuid.UUID) ([]*domain.AuditEvent, error)

	// FindByTenant — lista eventos de um tenant (com RLS).
	FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.AuditEvent, error)

	// FindByAction — lista eventos de um tenant por tipo de ação.
	FindByAction(ctx context.Context, tenantID uuid.UUID, action domain.AuditAction, limit, offset int) ([]*domain.AuditEvent, error)
}
