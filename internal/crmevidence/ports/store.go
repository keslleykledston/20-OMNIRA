// Package ports declares the seam the PRODUCT.7B2B evidence write depends
// on. This is deliberately the smallest possible surface: one write,
// idempotent, best-effort from the caller's point of view.
package ports

import (
	"context"

	"github.com/google/uuid"
)

// RecordTicketSelectionInput carries only server-authoritative facts.
// Every field here comes from a trusted source at the call site — never
// browser input except ExternalCompanyID, which arrives already validated
// by PRODUCT.6-K2's CompanyDirectory check (the caller passes through the
// SAME value CreateExternalTicket already accepted, never re-derives or
// re-trusts it independently).
type RecordTicketSelectionInput struct {
	TenantID             uuid.UUID
	ContactID            uuid.UUID
	ConnectionID         uuid.UUID
	ExternalCompanyID    string
	ActorUserID          uuid.UUID
	OriginTicketID       uuid.UUID
	OriginConversationID uuid.UUID
}

// EvidenceStore records durable Contact<->Company evidence (PRODUCT.7B2B).
// It stores NEITHER a CRM contact identity (external_contact_id) NOR any
// provider directory metadata (name/CNPJ/active) — only the fact that an
// authorized, validated ticket_selection observed this contact associated
// with this external company, through this exact tenant connection.
//
// Implementations MUST be idempotent: observing the same
// (tenant, contact, connection, company) fact again while it is still
// active must never create a duplicate row — it only advances
// last_verified_at.
type EvidenceStore interface {
	RecordTicketSelection(ctx context.Context, input RecordTicketSelectionInput) error
}
