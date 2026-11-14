package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotPlatformOperator is returned by a write to the feature flag (CONTEXT.2 operator kill switch,
// same shape as ADR-0058's groupsapp.ErrNotPlatformOperator) made by anyone who is not an active
// platform operator — the same uniform denial the HTTP handler maps to 404, never a 403 (no
// enumeration oracle, same convention as internal/platformrelease/updatemgr).
var ErrNotPlatformOperator = errors.New("intelligence: caller is not an active platform operator")

// ContextIntelligenceV2FeatureFlagRepository is the platform operator's runtime pause/resume for the
// CONTEXT.2A/2B window analysis runner — a SECOND gate, layered on top of (never replacing)
// OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED, which stays the host-level switch deciding whether the
// subsystem is wired up at all. ProcessOnce checks this on EVERY tick, never only once at worker
// startup: an operator flipping it off takes effect on the very next tick, no restart needed.
type ContextIntelligenceV2FeatureFlagRepository interface {
	Enabled(ctx context.Context) (bool, error)
}

// ContextIntelligenceV2FeatureFlagState is the admin console's view of the switch: who last touched
// it, and when.
type ContextIntelligenceV2FeatureFlagState struct {
	Enabled   bool       `json:"enabled"`
	UpdatedBy *uuid.UUID `json:"updated_by,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}
