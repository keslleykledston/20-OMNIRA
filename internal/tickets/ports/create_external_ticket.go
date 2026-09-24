// Package ports declares the seams application.CreateExternalTicket depends
// on (PRODUCT.6-K2). Every dependency is an interface so the application
// service never imports a concrete adapter, a connector wire type, or SQL —
// mirrors internal/messages/ports' separation between application and
// adapters.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// ErrIdempotencyMismatch is the canonical sentinel AttemptStore
// implementations return when an idempotency key is reused with a
// different request_hash. Declared here (not in a concrete adapter) so
// internal/tickets/application can recognize it via errors.Is without
// importing internal/tickets/adapters.
var ErrIdempotencyMismatch = errors.New("tickets: Idempotency-Key was already used with a different request")

// PermissionChecker resolves role permissions of a user in the
// TenantContext tenant. Structurally identical to (and satisfied without
// change by) internal/channels/adapters.PostgresPermissionChecker and
// internal/messages/ports.PermissionChecker — REUSE, not a new
// implementation.
type PermissionChecker interface {
	HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error)
}

// ConversationAuthorizer answers the single question CreateExternalTicket
// needs about a conversation: who, if anyone, it is assigned to. found=false
// means the conversation does not exist (or is not visible under RLS) —
// distinct from found=true with assignedTo=nil (exists, unassigned).
type ConversationAuthorizer interface {
	LoadAssignment(ctx context.Context, conversationID uuid.UUID) (assignedTo *uuid.UUID, found bool, err error)
}

// Company is the provider-neutral shape CreateExternalTicket validates a
// SelectedCustomerExternalID against — deliberately minimal (PRODUCT.6-K0):
// only what "does this ID correspond to a real, active tenant company"
// requires.
type Company struct {
	ExternalID string
	Active     bool
}

// CompanyDirectory is the trusted, server-side company source
// (PRODUCT.6-K0: VALIDATED_USER_SELECTION). Backed by the tenant's real
// CRM/company integration (K3GCRMClient.ListCompanies today) — never the
// browser's own claim about which company is selected.
type CompanyDirectory interface {
	ListCompanies(ctx context.Context) ([]Company, error)
}

// LocalTicketStore is the smallest seam onto the existing tickets table
// CreateExternalTicket needs: find the conversation's canonical open
// ticket (PRODUCT.6-K2 section 1 — the exact rule already used by
// internal/inbox/adapters.PostgresInboundStore.FindOpenByConversation, not
// invented here) and enrich it with the PRODUCT.6-D projection columns.
// Never creates a ticket — CreateExternalTicket only enriches an existing
// one.
type LocalTicketStore interface {
	// FindEnrichmentCandidate returns the conversation's canonical
	// non-closed/non-resolved ticket, or nil if none exists.
	FindEnrichmentCandidate(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error)
	// EnrichExternalProjection sets the PRODUCT.6-D projection columns on
	// an existing ticket. Idempotent: repeating it with the same values is
	// a no-op. Must reject (return an error) if the ticket already carries
	// a DIFFERENT external_ticket_id than the one being written — a local
	// ticket is enriched by exactly one external create attempt in V1.
	EnrichExternalProjection(ctx context.Context, ticketID uuid.UUID, provider, externalTicketID, externalStatus, externalStatusLabel string, syncedAt time.Time) error
}

// AttemptStore is the durable safety foundation from PRODUCT.6-K1
// (internal/tickets/adapters.AttemptStore satisfies this without
// modification — same method set).
type AttemptStore interface {
	Acquire(ctx context.Context, conversationID, actorUserID uuid.UUID, idempotencyKey, requestHash string) (*ticketsdomain.ExternalCreateAttempt, bool, error)
	MarkConfirmedSuccess(ctx context.Context, attemptID uuid.UUID, provider, externalTicketID string) (*ticketsdomain.ExternalCreateAttempt, error)
	MarkConfirmedFailure(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error)
	MarkOutcomeUnknown(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error)
	MarkProjectionSynced(ctx context.Context, attemptID, localTicketID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error)
}
