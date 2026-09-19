// Package delivery is the worker-side outbound channel delivery. A job carries
// only a message reference; text, recipient and connection are read from the
// database inside a tenant session derived from that persisted message.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ErrPermanent marks a job that can never succeed (malformed reference); the
// consumer terminates it instead of redelivering.
var ErrPermanent = errors.New("channel delivery: permanent job error")

// OutboundJob is the persisted state needed to deliver one queued message.
type OutboundJob struct {
	MessageID         uuid.UUID
	ConnectionID      uuid.UUID
	ToE164            string
	Text              string
	Status            string
	ProviderMessageID string
	ConnectionActive  bool
}

// OutboundStore is the persistence boundary of the worker. Every method after
// RunForMessage executes inside the tenant session it opens.
type OutboundStore interface {
	// RunForMessage resolves the tenant from the persisted message (never from
	// the NATS envelope) and runs fn inside SET LOCAL tenant context.
	RunForMessage(ctx context.Context, messageID uuid.UUID, fn func(context.Context) error) error
	// LockOutbound reads the message under a row lock; (nil, nil) when absent.
	LockOutbound(ctx context.Context, messageID uuid.UUID) (*OutboundJob, error)
	MarkSent(ctx context.Context, messageID uuid.UUID, providerMessageID string) error
	MarkFailed(ctx context.Context, messageID uuid.UUID, reason string) error
}

type TextSender interface {
	SendText(ctx context.Context, connectionID uuid.UUID, msg domain.OutboundTextMessage) (*domain.SendResult, error)
}

type Handler struct {
	store       OutboundStore
	sender      TextSender
	maxAttempts int
	outcomes    metric.Int64Counter
}

// NewHandler builds the delivery handler. After maxAttempts deliveries of a
// still-retryable failure the message is marked failed instead of retried.
func NewHandler(store OutboundStore, sender TextSender, maxAttempts int) (*Handler, error) {
	if store == nil || sender == nil || maxAttempts < 1 {
		return nil, errors.New("channel delivery: store, sender and maxAttempts are required")
	}
	counter, _ := otel.Meter("omnira/delivery").Int64Counter("channel_delivery_total")
	return &Handler{store: store, sender: sender, maxAttempts: maxAttempts, outcomes: counter}, nil
}

type envelope struct {
	AggregateID string `json:"aggregate_id"`
}

// Handle delivers one queued message. nil acks the job (delivered, recorded as
// failed, or already processed); ErrPermanent terminates it; any other error
// asks for a redelivery.
//
// At-least-once note: the provider has no idempotency key, so a crash between a
// successful provider call and the commit of MarkSent can send twice. The row
// lock plus the queued-only guard prevent every other duplicate path.
func (h *Handler) Handle(ctx context.Context, raw []byte, attempt int) error {
	var job envelope
	if err := json.Unmarshal(raw, &job); err != nil {
		return fmt.Errorf("%w: malformed envelope", ErrPermanent)
	}
	messageID, err := uuid.Parse(job.AggregateID)
	if err != nil || messageID == uuid.Nil {
		return fmt.Errorf("%w: invalid message reference", ErrPermanent)
	}
	err = h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
		out, err := h.store.LockOutbound(scoped, messageID)
		if err != nil {
			return err
		}
		if out == nil {
			return fmt.Errorf("%w: unknown message", ErrPermanent)
		}
		if out.Status != "queued" || out.ProviderMessageID != "" {
			h.count(scoped, "already_processed")
			return nil
		}
		if !out.ConnectionActive || out.ConnectionID == uuid.Nil {
			h.count(scoped, "channel_inactive")
			return h.store.MarkFailed(scoped, messageID, "channel_not_active")
		}
		result, sendErr := h.sender.SendText(scoped, out.ConnectionID, domain.OutboundTextMessage{
			ToE164: out.ToE164, Text: out.Text, IdempotencyKey: messageID.String(),
		})
		switch {
		case sendErr == nil:
			h.count(scoped, "sent")
			return h.store.MarkSent(scoped, messageID, result.ProviderMessageID)
		case Retryable(sendErr) && attempt < h.maxAttempts:
			h.count(scoped, "retry")
			return sendErr // rolls the transaction back; the message stays queued
		case Retryable(sendErr):
			h.count(scoped, "retries_exhausted")
			return h.store.MarkFailed(scoped, messageID, "retries_exhausted:"+Classify(sendErr))
		default:
			h.count(scoped, "failed")
			return h.store.MarkFailed(scoped, messageID, Classify(sendErr))
		}
	})
	return err
}

func (h *Handler) count(ctx context.Context, outcome string) {
	if h.outcomes != nil {
		h.outcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// Retryable is the worker retry policy for provider failures. Authentication,
// configuration, permanent validation, and logged-out sessions are terminal.
func Retryable(err error) bool {
	return errors.Is(err, ports.ErrTransient) ||
		errors.Is(err, ports.ErrRateLimited) ||
		errors.Is(err, ports.ErrProviderUnavailable)
}

// Classify maps a provider error to a low-cardinality reason. Provider error
// text is never persisted or exposed: it may contain identifiers.
func Classify(err error) string {
	switch {
	case errors.Is(err, ports.ErrAuthentication):
		return "authentication"
	case errors.Is(err, ports.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ports.ErrSessionDisconnected):
		return "session_disconnected"
	case errors.Is(err, ports.ErrProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, ports.ErrNotConfigured), errors.Is(err, ports.ErrConfiguration):
		return "configuration"
	case errors.Is(err, ports.ErrTransient):
		return "transient"
	case errors.Is(err, ports.ErrPermanent):
		return "rejected"
	default:
		return "unknown"
	}
}
