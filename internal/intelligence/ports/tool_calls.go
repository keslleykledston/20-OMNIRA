package ports

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
)

// ErrIdempotencyMismatch: the key was already used for a DIFFERENT request.
var ErrIdempotencyMismatch = errors.New("intelligence: idempotency key reused for a different request")

type ToolCallRepository interface {
	// Begin inserts the call. When the idempotency key already exists, it returns the existing call and inserted=false
	// (ErrIdempotencyMismatch when it was a different tool / topic / arguments). Concurrent identical requests serialise
	// on the unique index, so only one of them ever executes.
	Begin(ctx context.Context, c *domain.ToolCall) (stored *domain.ToolCall, inserted bool, err error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.ToolCall, error)
	List(ctx context.Context, tenantID, topicID uuid.UUID, limit int) ([]domain.ToolCall, error)
	// Transition moves a call from one status to another (guarded: false when it is no longer in `from`).
	Transition(ctx context.Context, tenantID, id uuid.UUID, from, to domain.ToolCallStatus, result json.RawMessage, errText string, decidedBy *uuid.UUID) (bool, error)
}
