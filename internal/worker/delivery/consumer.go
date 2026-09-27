package delivery

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName   = "OMNIRA_JOBS"
	consumerName = "worker-channel-send"
	sendSubject  = "job.channel.send_text.v1"
	// MaxDeliver bounds JetStream redelivery; the handler gives up (marks the
	// message failed) at MaxAttempts, always before this limit.
	MaxDeliver = 10
	// MaxInFlight bounds concurrent deliveries: each holds a DB connection (row lock + tenant
	// transaction) for the duration of the provider call, so unbounded concurrency could starve the
	// worker's pool (publisher, routing, realtime bridge).
	MaxInFlight = 8
	MaxAttempts = 8
)

// StartConsumer consumes outbound-send jobs from the shared jobs stream. It
// creates/updates ONLY its own durable consumer — OMNIRA_JOBS itself must
// already exist. Worker startup guarantees this via jobsstream.Ensure before
// StartConsumer is ever called (PILOT.4D3-C1: routing/delivery no longer own
// stream policy, to make config drift between them impossible by
// construction).
func StartConsumer(ctx context.Context, js jetstream.JetStream, handler *Handler) (jetstream.ConsumeContext, error) {
	if js == nil || handler == nil {
		return nil, errors.New("channel delivery: JetStream and handler are required")
	}
	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: sendSubject,
		MaxDeliver:    MaxDeliver,
		MaxAckPending: MaxInFlight,
		AckWait:       60 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return consumer.Consume(func(msg jetstream.Msg) {
		attempt := 1
		if meta, err := msg.Metadata(); err == nil {
			attempt = int(meta.NumDelivered)
		}
		err := handler.Handle(ctx, msg.Data(), attempt)
		switch {
		case err == nil:
			_ = msg.Ack()
		case errors.Is(err, ErrPermanent):
			_ = msg.Term()
		default:
			_ = msg.NakWithDelay(backoff(attempt))
		}
	})
}

func backoff(attempt int) time.Duration {
	d := 5 * time.Second << uint(min(attempt-1, 5))
	if d > 2*time.Minute {
		d = 2 * time.Minute
	}
	return d
}
