package ports

import (
	"context"

	"github.com/google/uuid"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// AcquireOutcome is the discriminated result of
// StatusMutationAttemptStore.Acquire (PRODUCT.6-O2B1 section 8). Unlike
// AttemptStore.Acquire (bool + sentinel error, PRODUCT.6-K1), status
// mutation acquisition is modeled as an explicit four-way outcome so a
// caller never has to infer "cross-key blocked" vs "idempotency mismatch"
// from error identity alone.
type AcquireOutcome string

const (
	// AcquireAcquired: a new attempt was created in state in_flight. Only
	// the caller that receives this outcome owns the right to perform the
	// provider PUT.
	AcquireAcquired AcquireOutcome = "acquired"
	// AcquireExistingSameIntent: the same (tenant, idempotency key) was
	// already used with the SAME request fingerprint. The returned attempt
	// is the existing durable row, in whatever state it is currently in —
	// the caller must act on that state (replay confirmed_success, surface
	// confirmed_failure, or report reconciliation-required for
	// in_flight/outcome_unknown), never re-acquire or re-PUT.
	AcquireExistingSameIntent AcquireOutcome = "existing_same_intent"
	// AcquireIdempotencyMismatch: the same (tenant, idempotency key) was
	// already used with a DIFFERENT request fingerprint. No attempt is
	// returned.
	AcquireIdempotencyMismatch AcquireOutcome = "idempotency_mismatch"
	// AcquireBlockedByUnresolvedOperation: a DIFFERENT idempotency key
	// already owns an unresolved (in_flight or outcome_unknown) attempt for
	// this exact local ticket. The returned attempt is the BLOCKER's, not a
	// row owned by this call's key. confirmed_success/confirmed_failure
	// attempts never block (PRODUCT.6-O2B1 section 5) — only a genuinely
	// unresolved operation does.
	AcquireBlockedByUnresolvedOperation AcquireOutcome = "blocked_by_unresolved_operation"
)

// AcquireStatusMutationAttemptCommand carries everything
// StatusMutationAttemptStore.Acquire needs to either create a new durable
// attempt or recognize an existing one. TenantID is deliberately absent —
// it comes from the caller's TenantContext (ctx), exactly like
// AttemptStore/LocalTicketStore, never as an explicit parameter a caller
// could mismatch against its own session.
type AcquireStatusMutationAttemptCommand struct {
	LocalTicketID    uuid.UUID
	ConversationID   uuid.UUID
	ActorUserID      uuid.UUID
	IdempotencyKey   string
	RequestHash      string
	Provider         string
	ExternalTicketID string
	TargetStatus     string
}

// StatusMutationAttemptStore is the durable safety foundation for external
// ticket STATUS MUTATION (PRODUCT.6-O2B1) — the status-mutation analog of
// AttemptStore (PRODUCT.6-K1), but with different blocking/non-blocking
// semantics per state (see ticketsdomain.ExternalStatusAttempt's doc
// comment). No application service exists yet that calls this interface —
// PRODUCT.6-O2B1 only lands the store; PRODUCT.6-O2B3 wires it into an
// application service.
type StatusMutationAttemptStore interface {
	Acquire(ctx context.Context, cmd AcquireStatusMutationAttemptCommand) (*ticketsdomain.ExternalStatusAttempt, AcquireOutcome, error)
	// MarkConfirmedSuccess transitions an in_flight attempt to
	// confirmed_success, durably recording the RECONCILED provider
	// snapshot (confirmedExternalStatus/confirmedExternalStatusLabel).
	// Provider/ExternalTicketID/TargetStatus are already fixed at
	// acquisition time and are never touched by this transition — a
	// status-mutation attempt's identity never needs re-establishing the
	// way a create attempt's does.
	MarkConfirmedSuccess(ctx context.Context, attemptID uuid.UUID, confirmedExternalStatus, confirmedExternalStatusLabel string) (*ticketsdomain.ExternalStatusAttempt, error)
	// MarkConfirmedFailure transitions an in_flight attempt to
	// confirmed_failure — the provider definitively rejected the mutation.
	// Non-blocking: releases the unresolved-operation barrier for this
	// local ticket immediately.
	MarkConfirmedFailure(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalStatusAttempt, error)
	// MarkOutcomeUnknown transitions an in_flight attempt to
	// outcome_unknown. Remains BLOCKING — never automatically becomes
	// in_flight again (no automatic retry), and a different key must not
	// acquire a new mutation for the same local ticket until this
	// ambiguity is explicitly reconciled.
	MarkOutcomeUnknown(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalStatusAttempt, error)
	// MarkProjectionSynced records that the local ticket's external
	// projection fields were updated from this confirmed_success attempt's
	// reconciled snapshot. Idempotent: a no-op if already synced.
	MarkProjectionSynced(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalStatusAttempt, error)
}
