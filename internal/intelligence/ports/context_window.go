package ports

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/contextwindow"
)

// ContextWindow is one row of context_analysis_windows (CONTEXT.2A): a batch of consecutive inbound messages of one
// conversation, accumulating until the flush policy says it is ready for grouped semantic analysis. Generation is
// the fencing/ordering token per conversation (1, 2, 3, ...; never two open windows at once).
type ContextWindow struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	Generation     int
	State          string
	Trigger        contextwindow.Trigger
	FirstMessageAt time.Time
	FirstMessageID uuid.UUID
	LastMessageAt  time.Time
	LastMessageID  uuid.UUID
	MessageCount   int
	DueAt          time.Time
	Attempt        int
}

// ResolvedMessage is what the consumer needs from a persisted inbound conversation message to call Touch — read
// from the stored row, never trusted from the event payload (ADR-0017 Wave 4 convention).
type ResolvedMessage struct {
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	CreatedAt      time.Time
}

// ContextWindowStore is the durable scheduling queue behind grouped context analysis (CONTEXT.2A). It never decides
// WHETHER a window is ready — that is contextwindow.Policy, a pure function — only persists the decision.
type ContextWindowStore interface {
	// ResolveMessage reads the tenant and conversation of an inbound conversation message by id (cross-tenant lookup,
	// system session — this is exactly how the consumer discovers the tenant, never from the event). found=false
	// when the message does not exist (deleted before the consumer saw it: nothing to do, not a failure) or is not
	// an inbound conversation message (e.g. outbound, or a group message out of this feature's scope).
	ResolveMessage(ctx context.Context, messageID uuid.UUID) (msg ResolvedMessage, found bool, err error)
	// Touch records one more distinct inbound message against the conversation's current open window (creating a new
	// one, generation = previous + 1, if none is open — never reopening a window that already left 'open'), then
	// recomputes due_at from the policy with TriggerNone. Idempotent per (tenant_id, window_id, message_id): a
	// redelivered event changes nothing. The tenant is resolved by the caller from the stored message, never trusted
	// from an event payload.
	Touch(ctx context.Context, policy contextwindow.Policy, tenantID, conversationID, messageID uuid.UUID, messageAt time.Time) error
	// Force closes the conversation's current open window immediately (due_at = now, state = 'due', the given
	// trigger recorded) for the manual/handoff/claim/finalize/priority triggers, which must not wait for
	// inactivity. A no-op (false, nil) when there is no open window — there is nothing to analyze yet.
	Force(ctx context.Context, tenantID, conversationID uuid.UUID, trigger contextwindow.Trigger) (forced bool, err error)
	// Sweep transitions up to limit windows from 'open' to 'due' whose due_at has passed (the idle/max-age cases —
	// max-messages is already handled synchronously by Touch). System-wide, across every tenant.
	Sweep(ctx context.Context, now time.Time, limit int) (int, error)
	// Claim leases up to limit due windows (state 'due', or 'running' with an expired lease = a crashed worker) with
	// SKIP LOCKED, ordered oldest due_at first.
	Claim(ctx context.Context, limit int, lease time.Duration) ([]ContextWindow, error)
	// MessageIDs returns the window's membership, oldest first — the deterministic cursor and the
	// analyzed_message_ids source for CONTEXT.2B.
	MessageIDs(ctx context.Context, w ContextWindow) ([]uuid.UUID, error)
	Complete(ctx context.Context, w ContextWindow) (bool, error)
	Retry(ctx context.Context, w ContextWindow, errorClass string, delay time.Duration) (bool, error)
	Dead(ctx context.Context, w ContextWindow, errorClass string) (bool, error)
}
