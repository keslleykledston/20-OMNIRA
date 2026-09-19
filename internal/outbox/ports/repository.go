package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/outbox/domain"
)

// OutboxEventRepository — interface para persistência de outbox events.
type OutboxEventRepository interface {
	// Store — persiste um evento no outbox.
	Store(ctx context.Context, event *domain.OutboxEvent) error

	// FindByID — busca um evento pelo ID.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.OutboxEvent, error)

	// FindUnpublished — lista eventos não publicados (para publisher).
	FindUnpublished(ctx context.Context, limit int) ([]*domain.OutboxEvent, error)

	// Update — atualiza evento (marcar como publicado, incrementar attempts).
	Update(ctx context.Context, event *domain.OutboxEvent) error

	// Delete — remove evento após publicação confirmada.
	Delete(ctx context.Context, id uuid.UUID) error

	// FindByTenant — lista eventos de um tenant (auditoria).
	FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.OutboxEvent, error)
}
