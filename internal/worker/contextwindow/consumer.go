// Package contextwindow consumes the same "a message was persisted" event the routing pipeline consumes
// (CONTEXT.2A) and extends/opens the conversation's context analysis window instead of a per-message job. It is a
// SEPARATE JetStream consumer (its own durable name) on the SAME subject: a problem here never holds up routing,
// and a problem in routing never holds up windowing. Only conversation (1:1) messages are in scope — group
// messages already have their own AI observer (ADR-0055) and are explicitly ignored here, not an error.
package contextwindow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

const (
	streamName   = "OMNIRA_JOBS"
	consumerName = "worker-context-window"
	// Subject is the same outbox event type the routing/intelligence consumer uses (job.inbox.message_persisted.v1):
	// OMNIRA_JOBS is the only stream the outbox feeds, and JetStream fans the same subject out to every durable
	// consumer independently, so a second consumer here duplicates no work and steals nothing from the first.
	Subject = "job.inbox.message_persisted.v1"
)

var ErrPermanent = errors.New("context window consumer: permanent event error")

// envelope is the outbox publisher's message; only the references are used. The tenant is deliberately never read
// from here — ResolveMessage reads it from the stored row.
type envelope struct {
	AggregateID string `json:"aggregate_id"`
	Payload     struct {
		Kind string `json:"kind"`
	} `json:"payload"`
}

// Handler extends/opens one conversation's context window per persisted inbound conversation message. Idempotent by
// construction (Touch's ON CONFLICT DO NOTHING on message membership).
type Handler struct {
	store  ports.ContextWindowStore
	policy contextwindow.Policy
}

func NewHandler(store ports.ContextWindowStore, policy contextwindow.Policy) (*Handler, error) {
	if store == nil {
		return nil, errors.New("context window consumer: store required")
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("context window consumer: %w", err)
	}
	return &Handler{store: store, policy: policy}, nil
}

func (h *Handler) Handle(ctx context.Context, raw []byte) error {
	var ev envelope
	if err := json.Unmarshal(raw, &ev); err != nil {
		return fmt.Errorf("%w: malformed envelope", ErrPermanent)
	}
	if ev.Payload.Kind == "group" {
		return nil // out of scope for this feature (ADR-0055 already covers groups) — not an error
	}
	id, err := uuid.Parse(ev.AggregateID)
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("%w: invalid message reference", ErrPermanent)
	}
	msg, found, err := h.store.ResolveMessage(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return nil // deleted before we saw it, or not an inbound conversation message: nothing to do
	}
	return h.store.Touch(ctx, h.policy, msg.TenantID, msg.ConversationID, id, msg.CreatedAt)
}

type action int

const (
	actAck action = iota
	actTerm
	actNak
)

func classify(err error) action {
	switch {
	case err == nil:
		return actAck
	case errors.Is(err, ErrPermanent):
		return actTerm
	}
	return actNak
}

// StartConsumer creates/updates ONLY its own durable consumer; the stream itself is owned by jobsstream.Ensure at
// startup (shared with every other OMNIRA_JOBS consumer).
func StartConsumer(ctx context.Context, js jetstream.JetStream, h *Handler) (jetstream.ConsumeContext, error) {
	return startConsumerOn(ctx, js, h, streamName, consumerName, Subject)
}

func startConsumerOn(ctx context.Context, js jetstream.JetStream, h *Handler, stream, durable, subject string) (jetstream.ConsumeContext, error) {
	if js == nil || h == nil {
		return nil, errors.New("context window consumer: JetStream and handler are required")
	}
	c, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable: durable, AckPolicy: jetstream.AckExplicitPolicy, FilterSubject: subject, MaxDeliver: 10, AckWait: 30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return c.Consume(func(msg jetstream.Msg) {
		switch classify(h.Handle(ctx, msg.Data())) {
		case actAck:
			_ = msg.Ack()
		case actTerm:
			_ = msg.Term()
		default:
			_ = msg.NakWithDelay(200 * time.Millisecond)
		}
	})
}
