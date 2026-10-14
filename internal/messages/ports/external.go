package ports

import (
	"context"

	"github.com/google/uuid"
)

// ExternalOutboundStore is the outbound store as an external application's write path uses it (ADR-0046 H5). The Postgres store implements it next to
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
