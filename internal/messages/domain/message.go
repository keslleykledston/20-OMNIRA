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
	// StatusUncertain (PILOT.4A2) — the provider outcome could not be
	// proven after all safe delivery attempts: neither a confirmed success
	// (matching provider id) nor a confirmed deterministic rejection.
	// Distinct from StatusFailed, which is reserved for outcomes OMNIRA can
	// actually prove.
	StatusUncertain Status = "uncertain"
)

type Message struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	ConversationID      uuid.UUID
	ChannelConnectionID *uuid.UUID
	Direction           Direction
	MessageType         string
	Body                string
	MediaRef            string
	MimeType            string
	SizeBytes           int64
	ProviderMessageID   string
	Status              Status
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func NewTextMessage(tenantID, conversationID uuid.UUID, direction Direction, body, providerMessageID string) (*Message, error) {
	return NewMessage(tenantID, conversationID, direction, "text", body, "", "", 0, providerMessageID)
}

func NewMessage(tenantID, conversationID uuid.UUID, direction Direction, messageType, body, mediaRef, mimeType string, sizeBytes int64, providerMessageID string) (*Message, error) {
	if tenantID == uuid.Nil || conversationID == uuid.Nil || messageType == "" || (body == "" && mediaRef == "") || sizeBytes < 0 {
		return nil, errors.New("message: tenant, conversation, type and content are required")
	}
	if direction != DirectionInbound && direction != DirectionOutbound {
		return nil, errors.New("message: invalid direction")
	}
	now := time.Now().UTC()
	status := StatusReceived
	if direction == DirectionOutbound {
		status = StatusQueued
	}
	return &Message{ID: uuid.New(), TenantID: tenantID, ConversationID: conversationID, Direction: direction, MessageType: messageType, Body: body, MediaRef: mediaRef, MimeType: mimeType, SizeBytes: sizeBytes, ProviderMessageID: providerMessageID, Status: status, CreatedAt: now, UpdatedAt: now}, nil
}
