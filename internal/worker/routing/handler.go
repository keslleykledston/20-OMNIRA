package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var ErrPermanent = errors.New("routing worker: permanent job error")

type ConversationRunner interface {
	RunForConversation(context.Context, uuid.UUID, func(context.Context) error) error
}

type Assigner interface {
	AssignRoundRobin(context.Context, uuid.UUID) (uuid.UUID, error)
}

type Handler struct {
	runner   ConversationRunner
	assigner Assigner
}

func NewHandler(runner ConversationRunner, assigner Assigner) (*Handler, error) {
	if runner == nil || assigner == nil {
		return nil, errors.New("routing worker: runner and assigner are required")
	}
	return &Handler{runner: runner, assigner: assigner}, nil
}

type envelope struct {
	AggregateID string `json:"aggregate_id"`
}

// Handle trusts only the persisted conversation reference. Any tenant_id in
// the envelope is intentionally ignored and never becomes authorization.
func (h *Handler) Handle(ctx context.Context, raw []byte) error {
	var job envelope
	if err := json.Unmarshal(raw, &job); err != nil {
		return fmt.Errorf("%w: malformed envelope", ErrPermanent)
	}
	conversationID, err := uuid.Parse(job.AggregateID)
	if err != nil || conversationID == uuid.Nil {
		return fmt.Errorf("%w: invalid conversation reference", ErrPermanent)
	}
	return h.runner.RunForConversation(ctx, conversationID, func(scoped context.Context) error {
		_, err := h.assigner.AssignRoundRobin(scoped, conversationID)
		return err
	})
}
