package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SendContext is what the send use case needs to know about a conversation,
// read inside the caller's tenant session (RLS + explicit tenant filter).
type SendContext struct {
	ConversationID  uuid.UUID
	AssignedTo      *uuid.UUID
	ConnectionID    *uuid.UUID
	ConnectionReady bool // connection exists, is active and supports text
	ToE164          string
}

// QueuedMessage is the persisted outbound message returned to the caller.
type QueuedMessage struct {
	ID             uuid.UUID
	ConversationID uuid.UUID
	Body           string
	Status         string
	CreatedAt      time.Time
	RequestHash    string
}

// OutboundStore persists a queued outbound message and its delivery job
// atomically (one statement, one transaction).
type OutboundStore interface {
	LoadSendContext(ctx context.Context, conversationID uuid.UUID) (*SendContext, error)
	// InsertQueued inserts the message (status queued) and enqueues the delivery
	// job in the Outbox. When (tenant, sender, key) already exists it returns the
	// existing message with replayed=true and inserts nothing.
	InsertQueued(ctx context.Context, sender uuid.UUID, in SendContext, body, idempotencyKey, requestHash string) (msg *QueuedMessage, replayed bool, err error)
}

// PermissionChecker resolves role permissions of a user in the TenantContext tenant.
type PermissionChecker interface {
	HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error)
}
