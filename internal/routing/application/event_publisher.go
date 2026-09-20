package application

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

// EventPublisher publishes participant events para notificações realtime
type EventPublisher interface {
	PublishParticipantInvited(ctx context.Context, tenantID, userID uuid.UUID, conversationID uuid.UUID) error
	PublishParticipantTransferred(ctx context.Context, tenantID, newAssigneeID uuid.UUID, fromAssignee *uuid.UUID) error
}

// NatsEventPublisher implementa EventPublisher usando NATS
type NatsEventPublisher struct {
	publisher interface {
		Publish(subject string, data []byte) error
	}
}

func NewNatsEventPublisher(publisher interface {
	Publish(subject string, data []byte) error
}) EventPublisher {
	return &NatsEventPublisher{publisher: publisher}
}

// Subject format para participant events: routing.events.{tenant_id}.{user_id}
func ParticipantEventSubject(tenantID, userID uuid.UUID) string {
	return fmt.Sprintf("routing.events.%s.%s", tenantID, userID)
}

func (p *NatsEventPublisher) PublishParticipantInvited(ctx context.Context, tenantID, userID uuid.UUID, conversationID uuid.UUID) error {
	event := map[string]interface{}{
		"type":            "participant_invited",
		"user_id":         userID.String(),
		"conversation_id": conversationID.String(),
		"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
	}

	body, err := json.Marshal(event)
	if err != nil {
		log.Printf("error marshaling participant_invited event: %v", err)
		return err
	}

	subject := ParticipantEventSubject(tenantID, userID)
	if err := p.publisher.Publish(subject, body); err != nil {
		log.Printf("error publishing participant_invited event: %v", err)
		return err
	}

	return nil
}

func (p *NatsEventPublisher) PublishParticipantTransferred(ctx context.Context, tenantID, newAssigneeID uuid.UUID, fromAssignee *uuid.UUID) error {
	event := map[string]interface{}{
		"type":          "participant_transferred",
		"new_assignee":  newAssigneeID.String(),
		"from_assignee": nil,
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
	}

	if fromAssignee != nil {
		event["from_assignee"] = fromAssignee.String()
	}

	body, err := json.Marshal(event)
	if err != nil {
		log.Printf("error marshaling participant_transferred event: %v", err)
		return err
	}

	subject := ParticipantEventSubject(tenantID, newAssigneeID)
	if err := p.publisher.Publish(subject, body); err != nil {
		log.Printf("error publishing participant_transferred event: %v", err)
		return err
	}

	return nil
}
