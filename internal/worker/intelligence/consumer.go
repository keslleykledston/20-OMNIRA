// Package intelligence consumes the "a message was persisted" event and turns it into a durable job (ADR-0017 Wave 4).
// The consumer does nothing else: the heavy work is the job runner's, so a slow or failing provider can never hold up
// the event stream, and a redelivered event can only ever find the job that already exists.
package intelligence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

const (
	streamName   = "OMNIRA_JOBS"
	consumerName = "worker-intelligence"
	// Subject is the outbox event type of a persisted message (a job.* type: OMNIRA_JOBS is the only stream the outbox feeds).
	Subject = "job.inbox.message_persisted.v1"
)

var ErrPermanent = errors.New("intelligence consumer: permanent event error")

// envelope is the outbox publisher's message; only the references are used, and the tenant is deliberately ignored: it is
// read from the stored message.
type envelope struct {
	AggregateID string `json:"aggregate_id"`
	Payload     struct {
		Kind string `json:"kind"`
	} `json:"payload"`
}

// Handler turns one event into a job. It is idempotent by construction (EnsureFromEvent).
type Handler struct{ jobs ports.JobStore }

func NewHandler(jobs ports.JobStore) (*Handler, error) {
	if jobs == nil {
		return nil, errors.New("intelligence consumer: job store required")
	}
	return &Handler{jobs: jobs}, nil
}

func (h *Handler) Handle(ctx context.Context, raw []byte) error {
	var ev envelope
	if err := json.Unmarshal(raw, &ev); err != nil {
		return fmt.Errorf("%w: malformed envelope", ErrPermanent)
	}
	id, err := uuid.Parse(ev.AggregateID)
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("%w: invalid message reference", ErrPermanent)
	}
	kind := ports.KindConversation
	switch ev.Payload.Kind {
	case "group":
		kind = ports.KindGroup
	case "conversation", "":
	default:
		return fmt.Errorf("%w: unknown kind", ErrPermanent)
	}
	if _, err := h.jobs.EnsureFromEvent(ctx, ports.MessageRef{Kind: kind, ID: id}, application.PipelineVersion); err != nil {
		if errors.Is(err, domain.ErrReferenceNotFound) {
			return fmt.Errorf("%w: message not found", ErrPermanent) // deleted before we saw it: nothing to do
		}
		return err
	}
	return nil
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

// StartConsumer creates/updates ONLY its own durable consumer; the stream itself is owned by jobsstream.Ensure at startup.
func StartConsumer(ctx context.Context, js jetstream.JetStream, h *Handler) (jetstream.ConsumeContext, error) {
	return startConsumer(ctx, js, h, streamName, consumerName, Subject)
}

func startConsumer(ctx context.Context, js jetstream.JetStream, h *Handler, stream, durable, subject string) (jetstream.ConsumeContext, error) {
	if js == nil || h == nil {
		return nil, errors.New("intelligence consumer: JetStream and handler are required")
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
