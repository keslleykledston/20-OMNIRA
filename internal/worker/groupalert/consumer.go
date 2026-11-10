package groupalert

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName   = "OMNIRA_JOBS"
	consumerName = "worker-group-alert"
	slaBreachJob = "job.routing.sla_breached.v1"
)

// StartConsumer subscribes to the SAME durable SLA-breach job the routing engine already publishes (ADR-0044) —
// a second, independent consumer of it, not a new producer. Same stream/durable-consumer shape as
// internal/worker/routing.StartConsumer.
func StartConsumer(ctx context.Context, js jetstream.JetStream, handler *Handler) (jetstream.ConsumeContext, error) {
	if js == nil || handler == nil {
		return nil, errors.New("group alert worker: JetStream and handler are required")
	}
	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: slaBreachJob,
		MaxDeliver:    3,
	})
	if err != nil {
		return nil, err
	}
	return consumer.Consume(func(msg jetstream.Msg) {
		handler.Handle(ctx, msg.Data())
		_ = msg.Ack() // Handle is best-effort by design (see handler.go); there is never a reason to retry this message
	})
}
