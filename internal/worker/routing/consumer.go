package routing

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/omnira/omnira/internal/routing/application"
)

const (
	streamName   = "OMNIRA_JOBS"
	consumerName = "worker-routing"
	routingJob   = "job.routing.assign.v1"
)

func StartConsumer(ctx context.Context, js jetstream.JetStream, handler *Handler) (jetstream.ConsumeContext, error) {
	return startConsumer(ctx, js, handler, streamName, consumerName, routingJob, "job.>")
}

func startConsumer(ctx context.Context, js jetstream.JetStream, handler *Handler, stream, durable, subject, streamSubject string) (jetstream.ConsumeContext, error) {
	if js == nil || handler == nil {
		return nil, errors.New("routing worker: JetStream and handler are required")
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     stream,
		Subjects: []string{streamSubject},
		Storage:  jetstream.FileStorage,
	}); err != nil {
		return nil, err
	}
	consumer, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: subject,
		MaxDeliver:    10,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return consumer.Consume(func(msg jetstream.Msg) {
		err := handler.Handle(ctx, msg.Data())
		switch ackAction(err) {
		case ackActionAck:
			_ = msg.Ack()
		case ackActionTerm:
			_ = msg.Term()
		default:
			_ = msg.NakWithDelay(nakDelay)
		}
	})
}

const nakDelay = 5 * time.Second

type routingAckAction int

const (
	ackActionNak routingAckAction = iota
	ackActionAck
	ackActionTerm
)

// ackAction is the pure classification of a Handle() outcome into the exact
// JetStream ack/nak/term decision — separated from Consume's closure so it
// can be tested directly, deterministically, and without a real NATS
// connection (PILOT.4D2 §9E: proving errors.Is still reaches the ACK branch
// through a wrapped error, for example).
func ackAction(err error) routingAckAction {
	switch {
	case err == nil:
		return ackActionAck
	case errors.Is(err, application.ErrNoEligibleAgent):
		// PILOT.4D2: "no eligible agent right now" is a successfully
		// evaluated business outcome, not a technical failure — the same
		// message can never resolve it (agent availability doesn't change
		// within the redelivery window), so same-message
		// NakWithDelay/MaxDeliver only wastes the retry budget and leaves a
		// permanently-stuck ack floor once MaxDeliver is reached. ACK
		// immediately; the conversation stays unassigned in Postgres, and
		// every future attempt is a NEW job from the liveness sweep or
		// presence wakeup — never a redelivery of this one. Logging/metrics
		// for this outcome live in
		// routing/application.Service.AssignRoundRobin, where
		// tenant_id/conversation_id are already in scope, not here.
		return ackActionAck
	case errors.Is(err, ErrPermanent):
		return ackActionTerm
	default:
		return ackActionNak
	}
}
