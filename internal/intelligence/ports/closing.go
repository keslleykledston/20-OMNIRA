package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ClosingMessage is one text message of a conversation as the closing suggestion sees it.
type ClosingMessage struct {
	Role string // customer | agent | bot
	Text string
	At   time.Time
}

// ClosingReader reads the most recent text messages of ONE conversation of the session's tenant, oldest first.
type ClosingReader interface {
	RecentMessages(ctx context.Context, tenantID, conversationID uuid.UUID, limit int) ([]ClosingMessage, error)
	// RecentMessagesHeldBy is RecentMessages for a Hub agent (ADR-0040 phase 07): the conversation must be open and held by `holder` IN THE SAME STATEMENT that reads the
	// text, so a conversation reassigned or closed after the caller's check yields nothing (there is no window in which the text of a conversation the person no longer
	// holds reaches the provider).
	RecentMessagesHeldBy(ctx context.Context, tenantID, conversationID, holder uuid.UUID, limit int) ([]ClosingMessage, error)
}
