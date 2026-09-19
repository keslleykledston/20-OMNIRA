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
