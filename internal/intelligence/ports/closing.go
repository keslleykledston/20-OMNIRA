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
}
