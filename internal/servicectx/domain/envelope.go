package domain

import (
	"time"

	"github.com/google/uuid"
)

// PublicEvent is the versioned name a consumer outside this module sees for a stored event. Most are the stored type plus ".v1"; a few facts have the name the
// outside world expects (a case "opened" is announced as created, a status change to resolved/done as resolved/completed).
func PublicEvent(e ContextEvent) string {
	to, _ := e.Payload["to"].(string)
	switch e.EventType {
	case EvCaseOpened:
		return "service_case.created.v1"
	case EvCaseStatus:
		if to == string(CaseResolved) {
			return "service_case.resolved.v1"
		}
		return "service_case.updated.v1"
	case EvTaskStatus:
		if to == string(TaskDone) {
			return "service_task.completed.v1"
		}
	}
	return e.EventType + ".v1"
}

// Envelope is the delivery shape of an event, the same for every type: identifiers, the company, the version, the correlation and the times. It carries the payload
// (ids, enums and counts) and NEVER the note a person typed, a message, or any text of the customer. The delivery itself (signing, retries, the dead letter) is the
// headless webhook engine's job; this is only the stable thing it delivers.
func Envelope(e ContextEvent) map[string]any {
	links := map[string]any{}
	set := func(k string, v *uuid.UUID) {
		if v != nil {
			links[k] = v.String()
		}
	}
	set("session_id", e.SessionID)
	set("case_id", e.CaseID)
	set("conversation_id", e.ConversationID)
	set("contact_id", e.ContactID)
	set("customer_id", e.CustomerID)
	actor := map[string]any{"type": e.ActorType}
	if e.ActorID != nil {
		actor["id"] = e.ActorID.String()
	}
	out := map[string]any{
		"event_id":       e.ID.String(),
		"event_type":     PublicEvent(e),
		"tenant_id":      e.TenantID.String(),
		"schema_version": e.SchemaVersion,
		"occurred_at":    e.OccurredAt.UTC().Format(time.RFC3339Nano),
		"recorded_at":    e.RecordedAt.UTC().Format(time.RFC3339Nano),
		"entity":         map[string]any{"type": e.EntityType, "id": e.EntityID.String()},
		"links":          links,
		"actor":          actor,
		"source_system":  e.SourceSystem,
		"payload":        e.Payload,
	}
	if e.CorrelationID != nil {
		out["correlation_id"] = e.CorrelationID.String()
	}
	if e.CausationID != nil {
		out["causation_id"] = e.CausationID.String()
	}
	return out
}

// AllEventTypes lists every stored event type (the contract test checks the AsyncAPI document against it).
func AllEventTypes() []string {
	return []string{
		EvSessionOpened, EvSessionClassified, EvSessionStatus, EvSessionClosed, EvSessionReopened,
		EvCaseOpened, EvCaseUpdated, EvCaseStatus, EvCaseAssigned, EvCaseSessionLinked,
		EvTaskCreated, EvTaskUpdated, EvTaskStatus, EvTaskAssigned,
		EvExternalLinked, EvExternalUnlinked, EvExternalStatusChanged, EvExternalRequested, EvExternalRequestState,
	}
}
