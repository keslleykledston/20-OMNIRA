package ports

import (
	"context"

	"github.com/google/uuid"
)

// SystemOutboundStore is the outbound store as the bot (a non-human sender) uses it. The Postgres store implements both
// this and OutboundStore; the human Sender never sees this interface.
type SystemOutboundStore interface {
	LoadSendContext(ctx context.Context, conversationID uuid.UUID) (*SendContext, error)
	// InsertQueuedSystem queues text with sent_by_user_id = NULL and enqueues the delivery job, idempotently by key. It
	// refuses (ErrConversationChanged) when a human got assigned or the channel changed since LoadSendContext: the bot
	// never talks over an operator.
	InsertQueuedSystem(ctx context.Context, in SendContext, body, idempotencyKey, requestHash string) (msg *QueuedMessage, replayed bool, err error)
}

// InteractiveOption is one tappable option of an interactive message.
type InteractiveOption struct {
	ID    string
	Title string
}

// InteractiveSend is what a queued interactive message carries for the provider.
type InteractiveSend struct {
	Body      string // the question shown above the options
	ListLabel string
	Options   []InteractiveOption
}

// SystemInteractiveStore is the optional capability to queue a bot menu as buttons/list. text is the full numbered-text
// menu: it is what the inbox shows and what is sent on a provider without buttons.
type SystemInteractiveStore interface {
	InsertQueuedSystemInteractive(ctx context.Context, in SendContext, text, idempotencyKey, requestHash string, itx InteractiveSend) (msg *QueuedMessage, replayed bool, err error)
}
