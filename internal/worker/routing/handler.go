package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/application"
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
	// "Nobody could be assigned" is a business outcome, not a failure. The runner rolls back the whole session when fn returns an error, and
	// with the central routing engine (ADR-0044) that session also holds what the engine just RECORDED about the conversation - the routing
	// state, the decision, the opening/step/SLA timers, the SLA clocks and the closed-hours notice. Rolling that back would silently undo
	// every decision that did not end in an assignment. So the session commits, and the sentinel is returned afterwards for the consumer to
	// acknowledge (it never retries this outcome). With the legacy router nothing was written in that case, so committing changes nothing.
	var noAgent error
	err = h.runner.RunForConversation(ctx, conversationID, func(scoped context.Context) error {
		_, err := h.assigner.AssignRoundRobin(scoped, conversationID)
		if errors.Is(err, application.ErrNoEligibleAgent) {
			noAgent = err
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	return noAgent
}
