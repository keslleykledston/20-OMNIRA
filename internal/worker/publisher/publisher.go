package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/omnira/omnira/internal/outbox/application"
	"github.com/omnira/omnira/internal/outbox/domain"
)

// Publisher — publica eventos do outbox para NATS JetStream.
type Publisher struct {
	outboxSvc  *application.OutboxService
	js         jetstream.JetStream
	batchSize  int
	maxRetries int
}

// NewPublisher — cria um novo Publisher.
func NewPublisher(
	outboxSvc *application.OutboxService,
	js jetstream.JetStream,
	batchSize int,
	maxRetries int,
) *Publisher {
	if batchSize <= 0 {
		batchSize = 10
	}
	if maxRetries <= 0 {
		maxRetries = 3
	}

	return &Publisher{
		outboxSvc:  outboxSvc,
		js:         js,
		batchSize:  batchSize,
		maxRetries: maxRetries,
	}
}

// PublishUnpublished — publica eventos não publicados do outbox.
// Retorna número de eventos publicados.
func (p *Publisher) PublishUnpublished(ctx context.Context) (int, error) {
	events, err := p.outboxSvc.GetUnpublishedEvents(ctx, p.batchSize)
	if err != nil {
		return 0, fmt.Errorf("failed to get unpublished events: %w", err)
	}

	published := 0
	for _, event := range events {
		if err := p.publishEvent(ctx, event); err != nil {
			// Log error but continue com próximos eventos
			fmt.Printf("failed to publish event %s: %v\n", event.ID, err)

			// Record attempt for retry logic
			if err := p.outboxSvc.RecordAttempt(ctx, event.ID); err != nil {
				fmt.Printf("failed to record attempt for event %s: %v\n", event.ID, err)
			}
			continue
		}

		// Mark as published
		if err := p.outboxSvc.MarkPublished(ctx, event.ID); err != nil {
			fmt.Printf("failed to mark event published %s: %v\n", event.ID, err)
			continue
		}

		published++
	}

	return published, nil
}

// publishEvent — publica um evento individual para NATS.
func (p *Publisher) publishEvent(ctx context.Context, event *domain.OutboxEvent) error {
	// Preparar payload
	payload := map[string]interface{}{
		"id":             event.ID.String(),
		"tenant_id":      event.TenantID.String(),
		"event_type":     string(event.EventType),
		"aggregate_type": string(event.AggregateType),
		"aggregate_id":   event.AggregateID.String(),
		"correlation_id": event.CorrelationID.String(),
		"causation_id":   event.CausationID.String(),
		"payload":        event.Payload,
		"timestamp":      event.CreatedAt.Format(time.RFC3339),
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Subject pattern: events.{event_type}.{aggregate_type}
	subject := subjectForEvent(event)

	// Publicar com retries
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second): // exponential backoff
			}
		}

		// Publish para JetStream
		_, err := p.js.Publish(ctx, subject, payloadJSON, jetstream.WithMsgID(event.ID.String()))
		if err == nil {
			return nil
		}

		// Todos os erros são considerados retryable
		continue
	}

	return fmt.Errorf("max retries exceeded publishing to %s", subject)
}

func subjectForEvent(event *domain.OutboxEvent) string {
	if strings.HasPrefix(string(event.EventType), "job.") {
		return string(event.EventType)
	}
	return fmt.Sprintf("events.%s.%s", event.EventType, event.AggregateType)
}

// Start — inicia loop de publicação (worker loop).
func (p *Publisher) Start(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			published, err := p.PublishUnpublished(ctx)
			if err != nil {
				fmt.Printf("error publishing events: %v\n", err)
				continue
			}
			if published > 0 {
				fmt.Printf("published %d events\n", published)
			}
		}
	}
}
