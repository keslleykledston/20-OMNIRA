package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event types of the operational timeline. The first segment says the entity, the last the fact; the outbox name adds ".v1" (see OutboxName).
const (
	EvSessionOpened     = "service_session.opened"
	EvSessionClassified = "service_session.classified"
	EvSessionStatus     = "service_session.status_changed"
	EvSessionClosed     = "service_session.closed"
	EvSessionReopened   = "service_session.reopened"
)

// Actor kinds of an event.
const (
	ActorUser        = "user"
	ActorSystem      = "system"
	ActorFlow        = "flow"
	ActorAI          = "ai"
	ActorIntegration = "integration"
)

// MaxEventPayload bounds a payload in bytes: it carries ids, enums and counts, never text.
const MaxEventPayload = 2048

// SchemaVersion is the version of the payload shapes below.
const SchemaVersion = 1

// ContextEvent is one thing that happened. It is history: written once, never changed. The state of things lives in the entities; a message is referred to
// by id, never copied. Note is the one free text allowed (a reason a person typed); it never goes to the outbox.
type ContextEvent struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	EventType      string
	EntityType     string
	EntityID       uuid.UUID
	SessionID      *uuid.UUID
	CaseID         *uuid.UUID
	ConversationID *uuid.UUID
	ContactID      *uuid.UUID
	CustomerID     *uuid.UUID
	ActorType      string
	ActorID        *uuid.UUID
	SourceSystem   string
	OccurredAt     time.Time
	RecordedAt     time.Time
	SchemaVersion  int
	Payload        map[string]any
	Note           string
	CorrelationID  *uuid.UUID
	CausationID    *uuid.UUID
	DedupKey       string
}

// Validate refuses an event that could leak: a payload over the cap or with a value that is not an id, an enum-like short string, a number or a bool.
func (e ContextEvent) Validate() error {
	if e.EventType == "" || e.EntityType == "" || e.EntityID == uuid.Nil || e.TenantID == uuid.Nil {
		return invalid("event without type, entity or tenant")
	}
	for k, v := range e.Payload {
		switch x := v.(type) {
		case string:
			if len(x) > 64 {
				return invalid("payload %q is a long text: events carry ids and enums only", k)
			}
		case bool, int, int32, int64, float64, nil:
		case uuid.UUID, *uuid.UUID:
		default:
			return invalid("payload %q has an unsupported type", k)
		}
	}
	b, err := json.Marshal(e.Payload)
	if err != nil || len(b) > MaxEventPayload {
		return invalid("event payload over %d bytes", MaxEventPayload)
	}
	return nil
}
