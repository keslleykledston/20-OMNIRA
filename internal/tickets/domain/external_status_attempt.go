package domain

import (
	"time"

	"github.com/google/uuid"
)

// ExternalStatusAttempt is the durable safety record for external (ERP)
// ticket STATUS MUTATION (PRODUCT.6-O2B1). It reuses AttemptState
// (PRODUCT.6-K1) — the same four-state vocabulary applies — but is a
// deliberately SEPARATE table/type from ExternalCreateAttempt: status
// mutation has different lifecycle safety semantics (see the package doc
// on AttemptState and migration 000050's own doc comment). In particular,
// AttemptConfirmedSuccess and AttemptConfirmedFailure are BOTH
// non-blocking here — status 2 -> later status 4 -> later status 5 are
// valid, distinct, sequential operations against the same local ticket —
// whereas for ExternalCreateAttempt, AttemptConfirmedSuccess permanently
// blocks another creation (there can only ever be one create).
//
// LocalTicketID, Provider, ExternalTicketID and TargetStatus are all known
// and durable from the moment an attempt is acquired (unlike
// ExternalCreateAttempt, where Provider/ExternalTicketID are only known
// once the provider responds): a status mutation attempt only ever targets
// an ALREADY LINKED ticket, so its identity preconditions are checked
// before acquisition, exactly like CreateExternalTicket's existing-link
// guard (PRODUCT.6-M5).
//
// ConfirmedExternalStatus/ConfirmedExternalStatusLabel capture the
// RECONCILED provider snapshot (either the mutation's own response, or a
// single read-back GetTicket, PRODUCT.6-O2A3 section 9) — kept separate
// from TargetStatus (what was requested) so a reconciliation mismatch
// remains visible rather than silently overwriting the original intent.
type ExternalStatusAttempt struct {
	ID                           uuid.UUID
	TenantID                     uuid.UUID
	LocalTicketID                uuid.UUID
	ConversationID               uuid.UUID
	ActorUserID                  uuid.UUID
	IdempotencyKey               string
	RequestHash                  string
	Provider                     string
	ExternalTicketID             string
	TargetStatus                 string
	State                        AttemptState
	ConfirmedExternalStatus      *string
	ConfirmedExternalStatusLabel *string
	ProjectionSyncedAt           *time.Time
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}
