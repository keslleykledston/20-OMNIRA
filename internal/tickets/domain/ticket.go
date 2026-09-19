package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Status string
type Priority string

const (
	StatusOpen       Status   = "open"
	StatusInProgress Status   = "in_progress"
	StatusWaiting    Status   = "waiting"
	StatusResolved   Status   = "resolved"
	StatusClosed     Status   = "closed"
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

type Ticket struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	Status         Status
	Priority       Priority
	Subject        string
	AssignedTo     *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ResolvedAt     *time.Time
	ClosedAt       *time.Time
}

func NewTicket(tenantID, conversationID uuid.UUID, subject string) (*Ticket, error) {
	if tenantID == uuid.Nil || conversationID == uuid.Nil {
		return nil, errors.New("ticket: tenant and conversation are required")
	}
	now := time.Now().UTC()
	return &Ticket{ID: uuid.New(), TenantID: tenantID, ConversationID: conversationID, Status: StatusOpen, Priority: PriorityMedium, Subject: subject, CreatedAt: now, UpdatedAt: now}, nil
}

func (t *Ticket) Resolve() {
	now := time.Now().UTC()
	t.Status, t.ResolvedAt, t.UpdatedAt = StatusResolved, &now, now
}

func (t *Ticket) Close() {
	now := time.Now().UTC()
	t.Status, t.ClosedAt, t.UpdatedAt = StatusClosed, &now, now
}
