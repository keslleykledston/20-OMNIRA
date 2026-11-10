// Package groupalert consumes job.routing.sla_breached.v1 (ADR-0044/0057) and, best-effort, notifies the
// tenant's single alert-channel group if one is configured (ADR-0058) — the one exception to "nothing is sent
// to a group". Handle never returns an error and the consumer always acks: a WAHA hiccup or a malformed
// envelope must never retry-storm this consumer or hold up the JetStream queue, and a malformed envelope will
// never un-malform itself on redelivery. internal/groups/application.SLAAlertService already logs and swallows
// its own failures.
package groupalert

import (
	"context"
	"encoding/json"
	"log"

	"github.com/google/uuid"

	groupsapp "github.com/omnira/omnira/internal/groups/application"
)

type envelope struct {
	ID          string `json:"id"`
	AggregateID string `json:"aggregate_id"`
	Payload     struct {
		Metric string `json:"metric"`
	} `json:"payload"`
}

type Handler struct {
	svc *groupsapp.SLAAlertService
}

func NewHandler(svc *groupsapp.SLAAlertService) *Handler {
	return &Handler{svc: svc}
}

// Handle trusts only the persisted conversation reference (same hardening as internal/worker/routing): any
// tenant_id elsewhere in the envelope is never used for authorization — SLAAlertService re-derives the real
// tenant from the conversation id itself.
func (h *Handler) Handle(ctx context.Context, raw []byte) {
	var ev envelope
	if err := json.Unmarshal(raw, &ev); err != nil {
		log.Printf("group alert: malformed envelope, discarded (%v)", err)
		return
	}
	conversationID, err := uuid.Parse(ev.AggregateID)
	if err != nil || conversationID == uuid.Nil {
		return // malformed/foreign reference: nothing this consumer can act on, never worth a retry
	}
	h.svc.HandleBreach(ctx, conversationID, ev.Payload.Metric, ev.ID)
}
