package domain

import (
	"time"

	"github.com/google/uuid"
)

// AttemptState is the durable state of an external (ERP) ticket create
// attempt (PRODUCT.6-K1). It exists because CreateTicket is a mutating,
// non-idempotent, non-retryable provider write (K3G offers no
// Idempotency-Key/externalReference/correlationId — PRODUCT.6-H1/6-I) and
// its outcome must survive process restarts so a future application
// service never sends a second POST for the same logical intent.
type AttemptState string

const (
	// AttemptInFlight: this idempotency key owns a create attempt and a
	// provider POST may already have been sent. NOT safe to treat as
	// retry-ready merely because it is old — see AttemptOutcomeUnknown and
	// the package doc on the crash window.
	AttemptInFlight AttemptState = "in_flight"
	// AttemptConfirmedSuccess: provider success was observed;
	// ExternalTicketID is known and must never be replaced by another one.
	AttemptConfirmedSuccess AttemptState = "confirmed_success"
	// AttemptConfirmedFailure: the provider definitively rejected the
	// create request (e.g. 400/401/403); no external ticket exists.
	AttemptConfirmedFailure AttemptState = "confirmed_failure"
	// AttemptOutcomeUnknown: a write may have occurred but OMNIRA cannot
	// prove success or failure (transport failure, 5xx, malformed 2xx,
	// 2xx missing a usable ticket id). Never automatically becomes
	// AttemptInFlight again — that would permit an automatic second POST.
	AttemptOutcomeUnknown AttemptState = "outcome_unknown"
)

// ExternalCreateAttempt is the durable record backing idempotent,
// crash-safe external ticket creation. Provider success
// (Provider/ExternalTicketID set, State=AttemptConfirmedSuccess) and local
// ticket projection (LocalTicketID/ProjectionSyncedAt) are deliberately
// separate facts: a row can be AttemptConfirmedSuccess with
// ProjectionSyncedAt still nil if the local enrichment write failed after a
// successful provider create. That evidence must remain durable and never
// trigger a second POST.
type ExternalCreateAttempt struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	ConversationID     uuid.UUID
	ActorUserID        uuid.UUID
	IdempotencyKey     string
	RequestHash        string
	State              AttemptState
	Provider           *string
	ExternalTicketID   *string
	LocalTicketID      *uuid.UUID
	ProjectionSyncedAt *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}
