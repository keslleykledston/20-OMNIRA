package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Direction string
type Status string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
	StatusReceived    Status    = "received"
	StatusQueued      Status    = "queued"
	StatusSent        Status    = "sent"
	StatusDelivered   Status    = "delivered"
	StatusRead        Status    = "read"
	StatusFailed      Status    = "failed"
)

type Message struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	ConversationID      uuid.UUID
	ChannelConnectionID *uuid.UUID
	Direction           Direction
	MessageType         string
	Body                string
	ProviderMessageID   string
	Status              Status
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func NewTextMessage(tenantID, conversationID uuid.UUID, direction Direction, body, providerMessageID string) (*Message, error) {
	if tenantID == uuid.Nil || conversationID == uuid.Nil || body == "" {
		return nil, errors.New("message: tenant, conversation and body are required")
	}
	if direction != DirectionInbound && direction != DirectionOutbound {
		return nil, errors.New("message: invalid direction")
	}
	now := time.Now().UTC()
	status := StatusReceived
	if direction == DirectionOutbound {
		status = StatusQueued
	}
	return &Message{ID: uuid.New(), TenantID: tenantID, ConversationID: conversationID, Direction: direction, MessageType: "text", Body: body, ProviderMessageID: providerMessageID, Status: status, CreatedAt: now, UpdatedAt: now}, nil
}
