package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	session    SessionRunner
}

// SessionRunner wraps every database operation in a transaction-local RLS
// context. A worker must never rely on state retained by a pool connection.
type SessionRunner func(context.Context, func(context.Context) error) error

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

func (p *Publisher) SetSessionRunner(runner SessionRunner) {
	p.session = runner
}

func (p *Publisher) runSession(ctx context.Context, fn func(context.Context) error) error {
	if p.session != nil {
		return p.session(ctx, fn)
	}
	return fn(ctx)
}

// PublishUnpublished — publica eventos não publicados do outbox.
// Retorna número de eventos publicados.
func (p *Publisher) PublishUnpublished(ctx context.Context) (int, error) {
	var events []*domain.OutboxEvent
	err := p.runSession(ctx, func(scoped context.Context) error {
		var err error
		events, err = p.outboxSvc.GetUnpublishedEvents(scoped, p.batchSize)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get unpublished events: %w", err)
	}

	published := 0
	for _, event := range events {
		if err := p.publishEvent(ctx, event); err != nil {
			// PILOT.4B: for the outbound send job, aggregate_id IS message_id
			// (see internal/messages/adapters/postgres.go's outbox insert) —
			// include it alongside outbox_event_id so an operator can follow
			// a message from acceptance through to a publish failure.
			log.Printf("outbox: publish failed outbox_event_id=%s%s tenant_id=%s: %v", event.ID, messageIDSuffix(event), event.TenantID, err)

			// Record attempt for retry logic
			if err := p.runSession(ctx, func(scoped context.Context) error {
				return p.outboxSvc.RecordAttempt(scoped, event.ID)
			}); err != nil {
				log.Printf("outbox: record attempt failed outbox_event_id=%s%s tenant_id=%s: %v", event.ID, messageIDSuffix(event), event.TenantID, err)
			}
			continue
		}

		// Mark as published
		if err := p.runSession(ctx, func(scoped context.Context) error {
			return p.outboxSvc.MarkPublished(scoped, event.ID)
		}); err != nil {
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

// messageIDSuffix — for the outbound send job, aggregate_id is the message
// id; for every other event type it is a different aggregate (tenant,
// membership, conversation), so nothing message-specific is added.
func messageIDSuffix(event *domain.OutboxEvent) string {
	if event.EventType != domain.JobChannelSendText {
		return ""
	}
	return " message_id=" + event.AggregateID.String()
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
