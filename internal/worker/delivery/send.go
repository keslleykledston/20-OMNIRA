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
	MessageID    uuid.UUID
	ConnectionID uuid.UUID
	ToE164       string
	// ProviderChatID é o endereço da conversa no provedor. Quando presente, é
	// ele que endereça o envio; derivar do telefone falha silenciosamente com
	// contatos endereçados por LID no WhatsApp.
	ProviderChatID string
	Text           string
	Status         string
	// ReservedProviderMessageID is the stable, pre-reserved provider message
	// id (PILOT.4A1) — set BEFORE the first SendText attempt, in its own
	// committed transaction, distinct from ProviderMessageID below. Empty
	// until a reservation exists (e.g. the provider doesn't support one, or
	// none has been made yet).
	ReservedProviderMessageID string
	// ProviderMessageID is set ONLY after a confirmed successful provider
	// response (MarkSent) — this semantic is unchanged by PILOT.4A1.
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
	// EnsureReservedProviderMessageID durably persists a stable provider
	// message id for messageID in its OWN committed transaction, separate
	// from any transaction that later calls the provider — this is the
	// central durability property PILOT.4A1 depends on (see package doc on
	// Handle). If a reservation already exists (even from a prior attempt
	// that later crashed before SendText ran), that exact value is returned
	// and generate is never called. Race-safe: if two callers race to
	// reserve concurrently, exactly one persisted value wins and the other
	// caller reads and reuses it — generate may be called more than once
	// across racing callers, but at most one resulting id is ever persisted
	// or used for delivery.
	EnsureReservedProviderMessageID(ctx context.Context, messageID uuid.UUID, generate func(context.Context) (string, error)) (string, error)
}

type TextSender interface {
	SendText(ctx context.Context, connectionID uuid.UUID, msg domain.OutboundTextMessage) (*domain.SendResult, error)
	// NewMessageID reserves a stable, provider-generated message id with no
	// delivery side effect. Returns ports.ErrCapabilityNotSupported if the
	// provider behind connectionID does not offer this.
	NewMessageID(ctx context.Context, connectionID uuid.UUID) (string, error)
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
// Two phases, PILOT.4A1:
//
//  1. RESERVATION — obtain (or reuse) a stable provider message id and commit
//     it to Postgres in its own transaction, BEFORE any provider call.
//  2. DELIVERY — call the provider with that exact id, then mark the result.
//
// Why the split matters: a worker crash (or any error) between a successful
// SendText and the commit of MarkSent used to leave no durable trace that the
// send had already happened — a redelivery would call SendText again, and
// without a stable id the provider had no way to recognize it as the same
// message (previously documented here as an accepted, if narrow, duplicate
// window). Because the reserved id is committed BEFORE delivery starts, it
// survives that exact crash: a redelivery reads the same persisted id and
// resends it unchanged. WAHA/GOWS 2026.8.2 has been runtime-proven
// (PILOT.4A0) to deduplicate a repeated send carrying an identical message id
// down to exactly one visible WhatsApp delivery — so reusing the same id on
// retry is safe against duplicate customer-visible sends for this exact
// provider/version, not a general at-least-once-becomes-exactly-once claim.
func (h *Handler) Handle(ctx context.Context, raw []byte, attempt int) error {
	var job envelope
	if err := json.Unmarshal(raw, &job); err != nil {
		return fmt.Errorf("%w: malformed envelope", ErrPermanent)
	}
	messageID, err := uuid.Parse(job.AggregateID)
	if err != nil || messageID == uuid.Nil {
		return fmt.Errorf("%w: invalid message reference", ErrPermanent)
	}

	// Read-only peek (own short transaction) to learn the connection and
	// whether a reservation or terminal status already exists, before
	// deciding whether phase 1 is even needed.
	var peek *OutboundJob
	if err := h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
		var err error
		peek, err = h.store.LockOutbound(scoped, messageID)
		return err
	}); err != nil {
		return err
	}
	if peek == nil {
		return fmt.Errorf("%w: unknown message", ErrPermanent)
	}
	if peek.Status != "queued" || peek.ProviderMessageID != "" {
		h.count(ctx, "already_processed")
		return nil
	}
	if !peek.ConnectionActive || peek.ConnectionID == uuid.Nil {
		h.count(ctx, "channel_inactive")
		return h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
			return h.store.MarkFailed(scoped, messageID, "channel_not_active")
		})
	}

	// PHASE 1 — RESERVATION. Skipped entirely (no provider call, no new id)
	// when a reservation already exists — this is what makes redelivery,
	// timeout retry and worker-restart safe: reuse, never re-mint.
	//
	// Runs inside its own tenant session (RunForMessage) — NOT the row lock
	// LockOutbound takes — because resolving the connection/provider (needed
	// to call the provider at all) goes through RLS and requires a tenant
	// GUC to be set. What must never happen, and doesn't here, is holding the
	// message row's FOR UPDATE lock while this network call is in flight.
	reservedID := peek.ReservedProviderMessageID
	if reservedID == "" {
		err = h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
			var genErr error
			reservedID, genErr = h.store.EnsureReservedProviderMessageID(scoped, messageID, func(genCtx context.Context) (string, error) {
				return h.sender.NewMessageID(genCtx, peek.ConnectionID)
			})
			return genErr
		})
		switch {
		case errors.Is(err, ports.ErrCapabilityNotSupported):
			// Provider has no stable-id mechanism: fall back to the
			// pre-PILOT.4A1 behavior (message id as the idempotency key) —
			// unchanged for any provider without this capability.
			reservedID = messageID.String()
		case err != nil:
			return err
		}
	}

	// PHASE 2 — DELIVERY. Re-lock: status may have changed since the peek
	// (e.g. another delivery already completed it), so the terminal-state
	// guard runs again for real, inside the transaction that decides the
	// outcome.
	return h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
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
			ToE164: out.ToE164, ProviderChatID: out.ProviderChatID, Text: out.Text, IdempotencyKey: reservedID,
		})
		switch {
		case sendErr == nil:
			h.count(scoped, "sent")
			return h.store.MarkSent(scoped, messageID, result.ProviderMessageID)
		case Retryable(sendErr) && attempt < h.maxAttempts:
			h.count(scoped, "retry")
			return sendErr // rolls the transaction back; the message stays queued, reservation stays committed
		case Retryable(sendErr):
			h.count(scoped, "retries_exhausted")
			return h.store.MarkFailed(scoped, messageID, "retries_exhausted:"+Classify(sendErr))
		default:
			h.count(scoped, "failed")
			return h.store.MarkFailed(scoped, messageID, Classify(sendErr))
		}
	})
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
