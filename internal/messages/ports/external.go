package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrTemplateGone: the template that was loaded is no longer approved and sendable for that line (it changed between the read and the insert).
var ErrTemplateGone = errors.New("messages: the template is no longer approved for this line")

// ExternalOutboundStore is the outbound store as an external application's write path uses it (ADR-0048 H5). The Postgres store implements it next to
// OutboundStore and SystemOutboundStore; the human Sender never sees it. There is NO idempotency here: the receipt of the command (written in the same
// transaction by the caller) is what makes a retry return the same message, so a message of an application carries a NULL idempotency_key.
type ExternalOutboundStore interface {
	LoadSendContext(ctx context.Context, conversationID uuid.UUID) (*SendContext, error)
	LoadTemplate(ctx context.Context, connectionID, templateID uuid.UUID) (*Template, error)
	// InsertQueuedExternal queues text with sent_by_installation_id = installation (sent_by_user_id stays NULL) and its delivery job. The SAME statement
	// proves, under a row lock on the conversation, that it is still open, still on the same active text channel and still held by THIS installation at
	// exactly this control version; otherwise it inserts nothing and returns ErrConversationChanged.
	InsertQueuedExternal(ctx context.Context, installation uuid.UUID, controlVersion int64, in SendContext, body, requestHash string) (*QueuedMessage, error)
	// InsertQueuedExternalTemplate is InsertQueuedExternal plus the template record, in the same transaction.
	InsertQueuedExternalTemplate(ctx context.Context, installation uuid.UUID, controlVersion int64, in SendContext, body, requestHash string, tpl TemplateSend) (*QueuedMessage, error)
}

// ExternalNoteStore persists an internal note as a message that is NEVER queued for delivery (ADR-0053): the implementation has no job/outbox
// write for this call, period — that is the actual barrier against a note reaching a provider, not a flag some other code path must remember to
// check. Same hold/version re-check as InsertQueuedExternal, under a row lock on the conversation.
type ExternalNoteStore interface {
	InsertNote(ctx context.Context, installation, conversationID uuid.UUID, controlVersion int64, body string) (*QueuedMessage, error)
}
