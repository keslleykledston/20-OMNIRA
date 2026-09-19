package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/outbox/domain"
	"github.com/omnira/omnira/internal/outbox/ports"
)

// OutboxService — lógica de negócio para outbox pattern.
type OutboxService struct {
	repo ports.OutboxEventRepository
}

// NewOutboxService — cria um novo OutboxService.
func NewOutboxService(repo ports.OutboxEventRepository) *OutboxService {
	return &OutboxService{repo: repo}
}

// RecordEvent — registra um evento para publicação (transacional com operação original).
func (s *OutboxService) RecordEvent(
	ctx context.Context,
	tenantID uuid.UUID,
	eventType domain.EventType,
	aggregateType domain.AggregateType,
	aggregateID uuid.UUID,
	correlationID uuid.UUID,
	payload map[string]interface{},
) (*domain.OutboxEvent, error) {
	event, err := domain.NewOutboxEvent(tenantID, eventType, aggregateType, aggregateID, correlationID)
	if err != nil {
		return nil, fmt.Errorf("failed to create outbox event: %w", err)
	}

	if payload != nil {
		event.SetPayload(payload)
	}

	if err := s.repo.Store(ctx, event); err != nil {
		return nil, fmt.Errorf("failed to store outbox event: %w", err)
	}

	return event, nil
}

// GetUnpublishedEvents — busca eventos não publicados (para publisher worker).
func (s *OutboxService) GetUnpublishedEvents(ctx context.Context, limit int) ([]*domain.OutboxEvent, error) {
	events, err := s.repo.FindUnpublished(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch unpublished events: %w", err)
	}
	return events, nil
}

// MarkPublished — marca evento como publicado após envio ao NATS.
func (s *OutboxService) MarkPublished(ctx context.Context, eventID uuid.UUID) error {
	event, err := s.repo.FindByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to fetch event: %w", err)
	}
	if event == nil {
		return fmt.Errorf("event not found")
	}

	event.MarkPublished()
	if err := s.repo.Update(ctx, event); err != nil {
		return fmt.Errorf("failed to update event: %w", err)
	}

	// Cleanup: remover evento após publicado (opcional, implementar com cuidado)
	// if err := s.repo.Delete(ctx, eventID); err != nil {
	//     return fmt.Errorf("failed to delete event: %w", err)
	// }

	return nil
}

// RecordAttempt — registra tentativa de publicação (para retry logic).
func (s *OutboxService) RecordAttempt(ctx context.Context, eventID uuid.UUID) error {
	event, err := s.repo.FindByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to fetch event: %w", err)
	}
	if event == nil {
		return fmt.Errorf("event not found")
	}

	event.RecordAttempt()
	if err := s.repo.Update(ctx, event); err != nil {
		return fmt.Errorf("failed to update event: %w", err)
	}

	return nil
}

// ListTenantEvents — lista eventos de um tenant (auditoria).
func (s *OutboxService) ListTenantEvents(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.OutboxEvent, error) {
	events, err := s.repo.FindByTenant(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list tenant events: %w", err)
	}
	return events, nil
}
