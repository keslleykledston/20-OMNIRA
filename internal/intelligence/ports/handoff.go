package ports

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
)

// HandoffRepository stores private-handoff invitations. Only the token HASH ever reaches it.
type HandoffRepository interface {
	// Create stores a pending invitation unless the topic already has MaxPendingHandoffs unexpired ones
	// (domain.ErrTooManyHandoffs). The topic must be open.
	Create(ctx context.Context, h *domain.TopicHandoff, tokenHash string, now time.Time) error
	List(ctx context.Context, tenantID, topicID uuid.UUID) ([]domain.TopicHandoff, error)
	// Revoke cancels a PENDING invitation of the topic; false when it is not pending (already used, revoked).
	Revoke(ctx context.Context, tenantID, topicID, handoffID uuid.UUID, now time.Time) (bool, error)
	// Redeem consumes the pending, unexpired invitation with this hash in ONE statement (so two concurrent redemptions
	// can never both win) and returns it; nil when there is none. The topic must still be open.
	Redeem(ctx context.Context, tenantID uuid.UUID, tokenHash string, messageID, conversationID uuid.UUID, now time.Time) (*domain.TopicHandoff, error)
	// SourceGroup is the single group the topic lives in, if exactly one.
	SourceGroup(ctx context.Context, tenantID, topicID uuid.UUID) (*uuid.UUID, error)
}
