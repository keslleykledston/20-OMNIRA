package domain

import (
	"time"

	"github.com/google/uuid"
)

// EventType — tipo de evento publicado.
type EventType string

const (
	EventTenantCreated     EventType = "tenant.created"
	EventTenantDeactivated EventType = "tenant.deactivated"
	EventMembershipGranted EventType = "membership.granted"
	EventMembershipRevoked EventType = "membership.revoked"
	EventChannelSendText   EventType = "channel.message.send_text"
)

// AggregateType — tipo de agregado que gerou o evento.
type AggregateType string

const (
	AggregateTenant     AggregateType = "tenant"
	AggregateMembership AggregateType = "membership"
)

// OutboxEvent — evento pendente de publicação (padrão Outbox).
type OutboxEvent struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	EventType     EventType
	AggregateType AggregateType
	AggregateID   uuid.UUID
	CorrelationID uuid.UUID // Vinculado a operação original
	CausationID   uuid.UUID // Operação que causou este evento
	Payload       map[string]interface{}
	PublishedAt   *time.Time // Null até publicado
	Attempts      int        // Tentativas de publicação
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewOutboxEvent — factory.
func NewOutboxEvent(
	tenantID uuid.UUID,
	eventType EventType,
	aggregateType AggregateType,
	aggregateID uuid.UUID,
	correlationID uuid.UUID,
) (*OutboxEvent, error) {
	if tenantID == uuid.Nil {
		return nil, ErrInvalidTenantID
	}

	if correlationID == uuid.Nil {
		correlationID = uuid.New()
	}

	return &OutboxEvent{
		ID:            uuid.New(),
		TenantID:      tenantID,
		EventType:     eventType,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		CorrelationID: correlationID,
		Payload:       make(map[string]interface{}),
		Attempts:      0,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}, nil
}

// SetPayload — adiciona dados ao payload.
func (e *OutboxEvent) SetPayload(data map[string]interface{}) {
	for k, v := range data {
		e.Payload[k] = v
	}
}

// SetCausationID — rastreia origem.
func (e *OutboxEvent) SetCausationID(id uuid.UUID) {
	e.CausationID = id
}

// MarkPublished — marca como publicado.
func (e *OutboxEvent) MarkPublished() {
	now := time.Now().UTC()
	e.PublishedAt = &now
	e.UpdatedAt = now
}

// RecordAttempt — registra tentativa de publicação.
func (e *OutboxEvent) RecordAttempt() {
	e.Attempts++
	e.UpdatedAt = time.Now().UTC()
}

// IsPublished — verifica se foi publicado.
func (e *OutboxEvent) IsPublished() bool {
	return e.PublishedAt != nil
}
