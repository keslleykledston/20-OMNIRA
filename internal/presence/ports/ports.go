package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
	presencedomain "github.com/omnira/omnira/internal/presence/domain"
)

// AgentRef names one agent profile inside one tenant.
type AgentRef struct {
	TenantID       uuid.UUID
	AgentProfileID uuid.UUID
}

// Store is the realtime presence backend (Valkey per ADR-0010). It is the
// sole source of truth for online/offline; Postgres is never read to answer
// this question.
type Store interface {
	// Touch registers or refreshes one session/tab's heartbeat and reports
	// whether this heartbeat caused the agent to transition offline->online
	// (i.e. this was the first live session for the agent). A second tab
	// heartbeating while the agent is already online must return false.
	Touch(ctx context.Context, tenantID, agentProfileID uuid.UUID, sessionID string, ttl time.Duration) (becameOnline bool, err error)

	// ExpireBatch retires up to limit sessions whose TTL elapsed as of now,
	// and reports which agents transitioned online->offline as a result
	// (only agents whose last live session just expired). Safe to call
	// concurrently from multiple reaper processes: each expired session is
	// retired by exactly one caller, so no duplicate transition is ever
	// reported. processed is the number of expired entries inspected in this
	// call, used by the caller to decide whether to drain further before the
	// next tick.
	ExpireBatch(ctx context.Context, now time.Time, limit int) (offline []AgentRef, processed int, err error)

	// Snapshot lists agents currently online in a tenant, as observed by the
	// realtime store (never Postgres).
	Snapshot(ctx context.Context, tenantID uuid.UUID) ([]uuid.UUID, error)
}

// TransitionPublisher publishes only aggregated state transitions
// (offline->online, online->offline), never individual heartbeats.
type TransitionPublisher interface {
	PublishTransition(ctx context.Context, tenantID, agentProfileID uuid.UUID, status presencedomain.Status) error
}

// LastSeenWriter persists a coalesced last_seen_at checkpoint to Postgres.
// MarkSeen only records intent in memory; it must never trigger a write by
// itself. FlushNow performs the batched write and is called by a periodic
// loop, not per heartbeat.
type LastSeenWriter interface {
	MarkSeen(tenantID, agentProfileID uuid.UUID, at time.Time)
	FlushNow(ctx context.Context) error
}
