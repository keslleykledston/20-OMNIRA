package adapters

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	presencedomain "github.com/omnira/omnira/internal/presence/domain"
	"github.com/omnira/omnira/internal/presence/ports"
)

// Publisher is the NATS subset the presence bridge needs — same shape as
// internal/worker/realtime.Publisher. Kept as its own tiny interface rather
// than importing that package, to avoid coupling the presence domain to the
// inbox realtime module for one method signature.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Subject is where the presence SSE endpoint subscribes: tenant-scoped only
// (transitions never carry a conversation/queue dimension).
func Subject(tenantID uuid.UUID) string {
	return "presence.changed." + tenantID.String()
}

// TransitionEvent is the NATS/SSE payload. Deliberately minimal — no PII, no
// user name/email, per ADR-0010 §10 and §15.
type TransitionEvent struct {
	TenantID       uuid.UUID             `json:"tenant_id"`
	AgentProfileID uuid.UUID             `json:"agent_profile_id"`
	Status         presencedomain.Status `json:"status"`
	OccurredAt     time.Time             `json:"occurred_at"`
}

type NatsTransitionPublisher struct {
	nc Publisher
}

func NewNatsTransitionPublisher(nc Publisher) *NatsTransitionPublisher {
	return &NatsTransitionPublisher{nc: nc}
}

var _ ports.TransitionPublisher = (*NatsTransitionPublisher)(nil)

// PublishTransition never fires per heartbeat — only the Service/Reaper call
// it, and only on an actual online<->offline flip.
func (p *NatsTransitionPublisher) PublishTransition(_ context.Context, tenantID, agentProfileID uuid.UUID, status presencedomain.Status) error {
	if p == nil || p.nc == nil {
		return nil
	}
	body, err := json.Marshal(TransitionEvent{
		TenantID:       tenantID,
		AgentProfileID: agentProfileID,
		Status:         status,
		OccurredAt:     time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return p.nc.Publish(Subject(tenantID), body)
}
