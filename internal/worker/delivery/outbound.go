// Package delivery contains worker-side channel delivery boundaries. It
// accepts durable references from NATS and delegates only after a caller
// has reconstructed the tenant-scoped database session.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// TenantSessionRunner must resolve the connection by ID from trusted storage,
// derive its tenant, and execute fn inside SET LOCAL tenant context. The
// tenant_id present in an event envelope is deliberately not part of this
// interface and is never used for authorization.
type TenantSessionRunner interface {
	RunForConnection(ctx context.Context, connectionID uuid.UUID, fn func(context.Context) error) error
}

type TextSender interface {
	SendText(ctx context.Context, connectionID uuid.UUID, msg domain.OutboundTextMessage) (*domain.SendResult, error)
}

type Handler struct {
	runner TenantSessionRunner
	sender TextSender
}

func NewHandler(runner TenantSessionRunner, sender TextSender) (*Handler, error) {
	if runner == nil || sender == nil {
		return nil, errors.New("channel delivery: runner and sender are required")
	}
	return &Handler{runner: runner, sender: sender}, nil
}

type textEnvelope struct {
	Payload struct {
		ConnectionID   string `json:"connection_id"`
		ToE164         string `json:"to_e164"`
		Text           string `json:"text"`
		IdempotencyKey string `json:"idempotency_key"`
	} `json:"payload"`
}

// Handle decodes only non-secret outbound references and performs delivery in
// a tenant session. It intentionally ignores any tenant_id supplied by NATS.
func (h *Handler) Handle(ctx context.Context, raw []byte) error {
	var envelope textEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("channel delivery: malformed command: %w", ports.ErrPermanent)
	}
	connectionID, err := uuid.Parse(envelope.Payload.ConnectionID)
	if err != nil || connectionID == uuid.Nil {
		return fmt.Errorf("channel delivery: invalid connection reference: %w", ports.ErrPermanent)
	}
	message := domain.OutboundTextMessage{
		ToE164:         envelope.Payload.ToE164,
		Text:           envelope.Payload.Text,
		IdempotencyKey: envelope.Payload.IdempotencyKey,
	}
	return h.runner.RunForConnection(ctx, connectionID, func(scoped context.Context) error {
		_, err := h.sender.SendText(scoped, connectionID, message)
		return err
	})
}

// Retryable is the worker retry policy for provider failures. Authentication,
// configuration, permanent validation, and logged-out sessions are terminal.
func Retryable(err error) bool {
	return errors.Is(err, ports.ErrTransient) ||
		errors.Is(err, ports.ErrRateLimited) ||
		errors.Is(err, ports.ErrProviderUnavailable)
}
