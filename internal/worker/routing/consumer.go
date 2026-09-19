package routing

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName   = "OMNIRA_JOBS"
	consumerName = "worker-routing"
	routingJob   = "job.routing.assign.v1"
)

func StartConsumer(ctx context.Context, js jetstream.JetStream, handler *Handler) (jetstream.ConsumeContext, error) {
	if js == nil || handler == nil {
		return nil, errors.New("routing worker: JetStream and handler are required")
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{"job.>"},
		Storage:  jetstream.FileStorage,
	}); err != nil {
		return nil, err
	}
	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: routingJob,
		MaxDeliver:    10,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return consumer.Consume(func(msg jetstream.Msg) {
		err := handler.Handle(ctx, msg.Data())
		switch {
		case err == nil:
			_ = msg.Ack()
		case errors.Is(err, ErrPermanent):
			_ = msg.Term()
		default:
			_ = msg.NakWithDelay(5 * time.Second)
		}
	})
}
