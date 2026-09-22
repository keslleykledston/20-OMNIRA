package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// LivenessRepository is IAM4.2-B0: routing liveness/retrigger correctness.
// It reuses the existing outbox/NATS/consumer/AssignRoundRobin pipeline —
// it only ever re-enqueues the same job.routing.assign.v1 the original
// routing already publishes; it never assigns anything itself.
type LivenessRepository interface {
	// Retrigger selects up to limit routable, due, unassigned round-robin
	// conversations, bumps their routing_retry_at forward by backoff, and
	// enqueues a fresh job.routing.assign.v1 outbox event for each, all in
	// one transaction (SKIP LOCKED, safe under concurrent callers — the
	// safety sweep and a presence wakeup racing each other retire disjoint
	// rows, never the same one twice).
	//
	// tenantID nil scopes across every tenant (the safety sweep); non-nil
	// scopes to one tenant (a presence wakeup). queueID nil means any
	// round-robin queue; non-nil scopes to that one queue only — a wakeup
	// calls this once per resolved queue rather than passing a list, which
	// keeps the query a single scalar comparison instead of an array
	// parameter.
	//
	// Returns how many conversations were retriggered.
	Retrigger(ctx context.Context, tenantID *uuid.UUID, queueID *uuid.UUID, limit int, backoff time.Duration) (int, error)

	// ActiveQueuesForAgent resolves the round-robin queues one AgentProfile
	// is currently eligible to serve (active Membership, active AgentProfile,
	// active+available queue_members) — used only to scope a presence-online
	// wakeup to the queues that actually just gained a candidate.
	ActiveQueuesForAgent(ctx context.Context, tenantID, agentProfileID uuid.UUID) ([]uuid.UUID, error)
}
