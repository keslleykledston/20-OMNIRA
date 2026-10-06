package flows

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName   = "OMNIRA_JOBS"
	consumerName = "worker-flows"
	inboundJob   = "job.flow.inbound.v1"
	nakDelay     = 5 * time.Second
)

// StartConsumer creates/updates ONLY its own durable consumer: the stream (name, subjects "job.>", retention) is owned by
// jobsstream.Ensure, which the worker runs before any consumer starts.
func StartConsumer(ctx context.Context, js jetstream.JetStream, handler *Handler) (jetstream.ConsumeContext, error) {
	if js == nil || handler == nil {
		return nil, errors.New("flows worker: JetStream and handler are required")
	}
	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: inboundJob,
		MaxDeliver:    10,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return consumer.Consume(func(msg jetstream.Msg) {
		switch AckAction(handler.Handle(ctx, msg.Data())) {
		case AckActionAck:
			_ = msg.Ack()
		case AckActionTerm:
			_ = msg.Term()
		default:
			_ = msg.NakWithDelay(nakDelay)
		}
	})
}

type AckActionKind int

const (
	AckActionNak AckActionKind = iota
	AckActionAck
	AckActionTerm
)

// AckAction classifies a Handle outcome: success acks, a permanent error terminates (never retried), anything else is
// retried with a delay. Pure so it is tested without NATS.
func AckAction(err error) AckActionKind {
	switch {
	case err == nil:
		return AckActionAck
	case errors.Is(err, ErrPermanent):
		return AckActionTerm
	}
	return AckActionNak
}
