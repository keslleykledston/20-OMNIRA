package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrPresenceUnavailable is distinct from application.ErrNoEligibleAgent:
// it means the presence backend itself could not be consulted (Valkey down,
// timeout, or not configured for this process) — never converted into "no
// eligible agent". Left unwrapped by the caller so it falls through to the
// worker consumer's existing NakWithDelay/MaxDeliver retry (IAM4.2-B0
// guarantees a future retrigger even past MaxDeliver).
var ErrPresenceUnavailable = errors.New("routing: presence backend unavailable")

// PresenceChecker is IAM4.2-B1: presence-aware automated routing. It checks
// a bounded batch (chunk) of agent_profile_ids against Valkey's online set
// in one round trip — never one call per candidate, never a keyspace scan.
// internal/presence/adapters.Store already implements this method exactly
// (structural typing — no adapter glue needed).
type PresenceChecker interface {
	OnlineMembers(ctx context.Context, tenantID uuid.UUID, agentProfileIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}
