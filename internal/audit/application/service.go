package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/audit/domain"
	"github.com/omnira/omnira/internal/audit/ports"
)

// AuditService — lógica de negócio para auditoria.
type AuditService struct {
	repo ports.AuditEventRepository
}

// NewAuditService — cria um novo AuditService.
func NewAuditService(repo ports.AuditEventRepository) *AuditService {
	return &AuditService{repo: repo}
}

// RecordEvent — registra um evento de auditoria.
func (s *AuditService) RecordEvent(
	ctx context.Context,
	tenantID uuid.UUID,
	actorID uuid.UUID,
	action domain.AuditAction,
	resourceType domain.ResourceType,
	resourceID uuid.UUID,
	outcome domain.AuditOutcome,
	correlationID uuid.UUID,
) (*domain.AuditEvent, error) {
	event, err := domain.NewAuditEvent(
		tenantID,
		actorID,
		action,
		resourceType,
		resourceID,
		outcome,
		correlationID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create audit event: %w", err)
	}

	if err := s.repo.Store(ctx, event); err != nil {
		return nil, fmt.Errorf("failed to store audit event: %w", err)
	}

	return event, nil
}

// GetEventsByCorrelation — busca eventos por correlation ID (para rastreabilidade).
func (s *AuditService) GetEventsByCorrelation(ctx context.Context, tenantID, correlationID uuid.UUID) ([]*domain.AuditEvent, error) {
	events, err := s.repo.FindByTenantAndCorrelation(ctx, tenantID, correlationID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch events by correlation: %w", err)
	}
	return events, nil
}

// ListTenantEvents — lista eventos de um tenant (com paginação).
func (s *AuditService) ListTenantEvents(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.AuditEvent, error) {
	events, err := s.repo.FindByTenant(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list tenant events: %w", err)
	}
	return events, nil
}

// ListEventsByAction — lista eventos de um tenant filtrados por ação.
func (s *AuditService) ListEventsByAction(ctx context.Context, tenantID uuid.UUID, action domain.AuditAction, limit, offset int) ([]*domain.AuditEvent, error) {
	events, err := s.repo.FindByAction(ctx, tenantID, action, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list events by action: %w", err)
	}
	return events, nil
}
