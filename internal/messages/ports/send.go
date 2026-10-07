package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrConversationChanged: the conversation's assignee or channel changed between the checks and the insert.
var ErrConversationChanged = errors.New("messages: conversation changed, retry")

// SendContext is what the send use case needs to know about a conversation,
// read inside the caller's tenant session (RLS + explicit tenant filter).
type SendContext struct {
	ConversationID  uuid.UUID
	AssignedTo      *uuid.UUID
	ConnectionID    *uuid.UUID
	ConnectionReady bool // connection exists, is active and supports text
	ToE164          string
	// Provider is the connection's provider (e.g. meta_cloud); LastInboundAt is when the contact last wrote in this
	// conversation. Together they decide whether the provider's 24 h customer-service window allows free text.
	Provider      string
	LastInboundAt *time.Time
	// Closed: the attendance was finalized (ADR-0020). A reply would be stored in a closed conversation while the contact's next
	// message starts a NEW one, splitting the context, so sending is refused.
	Closed bool
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
	//
	// The insertion re-checks, in the SAME statement and under a row lock on the conversation, that the
	// conversation still uses the same active text channel and (when requireAssignee) is still assigned to
	// the sender — closing the window between LoadSendContext and the insert (reassign/unassign/channel
	// change). When the state changed it returns ErrConversationChanged and inserts nothing.
	InsertQueued(ctx context.Context, sender uuid.UUID, in SendContext, body, idempotencyKey, requestHash string, requireAssignee bool) (msg *QueuedMessage, replayed bool, err error)
	// LoadTemplate reads a template that belongs to the given connection (nil when it is not that connection's).
	LoadTemplate(ctx context.Context, connectionID, templateID uuid.UUID) (*Template, error)
	// InsertQueuedTemplate is InsertQueued plus the template record, in the same transaction. body is the rendered text
	// shown in the inbox; tpl is what the provider receives.
	InsertQueuedTemplate(ctx context.Context, sender uuid.UUID, in SendContext, body, idempotencyKey, requestHash string, requireAssignee bool, tpl TemplateSend) (msg *QueuedMessage, replayed bool, err error)
}

// Template is an approved WhatsApp Cloud API template of a connection, as the send use case needs it.
type Template struct {
	ID            uuid.UUID
	Name          string
	Language      string
	Body          string
	Status        string
	VariableCount int
	Sendable      bool
}

// TemplateSend is what a queued template message carries for the provider: the body variables in order.
type TemplateSend struct {
	Name     string
	Language string
	Params   []string
}

// PermissionChecker resolves role permissions of a user in the TenantContext tenant.
type PermissionChecker interface {
	HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error)
}
