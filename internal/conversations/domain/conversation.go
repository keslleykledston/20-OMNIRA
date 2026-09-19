package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

type Conversation struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	ContactID           uuid.UUID
	ChannelConnectionID *uuid.UUID
	Status              Status
	Title               string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ClosedAt            *time.Time
}

func NewConversation(tenantID, contactID uuid.UUID, connectionID *uuid.UUID) (*Conversation, error) {
	if tenantID == uuid.Nil || contactID == uuid.Nil {
		return nil, errors.New("conversation: tenant and contact are required")
	}
	now := time.Now().UTC()
	return &Conversation{ID: uuid.New(), TenantID: tenantID, ContactID: contactID, ChannelConnectionID: connectionID, Status: StatusOpen, CreatedAt: now, UpdatedAt: now}, nil
}

func (c *Conversation) Close() {
	now := time.Now().UTC()
	c.Status, c.ClosedAt, c.UpdatedAt = StatusClosed, &now, now
}
