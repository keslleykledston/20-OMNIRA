// Package contextwindow decides when a persisted conversation's contextual
// analysis should run. Persistence, deduplication, tenancy and leases belong
// to the database/worker, never an in-memory timer.
package contextwindow

import (
    "errors"
    "time"
)

type Policy struct {
    Idle time.Duration
    MaxAge time.Duration
    MaxMessages int
}

func DefaultPolicy() Policy {
    return Policy{Idle: 30 * time.Second, MaxAge: 2 * time.Minute, MaxMessages: 12}
}

func (p Policy) Validate() error {
    if p.Idle <= 0 || p.MaxAge <= 0 || p.MaxMessages <= 0 || p.Idle > p.MaxAge {
        return errors.New("contextwindow: invalid batching policy")
    }
    return nil
}

// Snapshot represents distinct persisted messages from one tenant/conversation.
// Storage timestamps must be authoritative (not channel-provided).
type Snapshot struct {
    FirstAt time.Time
    LastAt time.Time
    Count int
}

type Trigger string

const (
    TriggerNone Trigger = ""
    TriggerManual Trigger = "manual"
    TriggerHandoff Trigger = "handoff"
    TriggerClaim Trigger = "claim"
    TriggerFinalize Trigger = "finalize"
    TriggerPriority Trigger = "priority"
)

type Reason string

const (
    ReasonPending Reason = "pending"
    ReasonIdle Reason = "idle"
    ReasonMaxAge Reason = "max_age"
    ReasonMaxMessages Reason = "max_messages"
    ReasonForced Reason = "forced"
)

type Decision struct {
    Ready bool
    DueAt time.Time
    Reason Reason
}

func (p Policy) Evaluate(s Snapshot, now time.Time, trigger Trigger) (Decision, error) {
    if err := p.Validate(); err != nil { return Decision{}, err }
    if s.Count <= 0 || s.FirstAt.IsZero() || s.LastAt.IsZero() ||
       s.LastAt.Before(s.FirstAt) || now.IsZero() || now.Before(s.FirstAt) {
        return Decision{}, errors.New("contextwindow: invalid snapshot")
    }
    idle := s.LastAt.Add(p.Idle)
    maxAge := s.FirstAt.Add(p.MaxAge)
    due := idle
    if maxAge.Before(due) { due = maxAge }
    switch trigger {
    case TriggerManual, TriggerHandoff, TriggerClaim, TriggerFinalize, TriggerPriority:
        return Decision{Ready: true, DueAt: now, Reason: ReasonForced}, nil
    case TriggerNone:
    default:
        return Decision{}, errors.New("contextwindow: unknown trigger")
    }
    if s.Count >= p.MaxMessages { return Decision{Ready: true, DueAt: now, Reason: ReasonMaxMessages}, nil }
    if !now.Before(maxAge) { return Decision{Ready: true, DueAt: maxAge, Reason: ReasonMaxAge}, nil }
    if !now.Before(idle) { return Decision{Ready: true, DueAt: idle, Reason: ReasonIdle}, nil }
    return Decision{Ready: false, DueAt: due, Reason: ReasonPending}, nil
}
