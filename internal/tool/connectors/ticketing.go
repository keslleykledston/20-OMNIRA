package connectors

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// TicketingConnector is the provider-neutral ticketing boundary (ADR-0013,
// PRODUCT.6-E). It exists because CRMConnector (above, in crm.go) and
// inboxapp.CRMConnector (internal/inbox/application) are both named "CRM"
// but model two unrelated things:
//
//   - CRM/customer/activity integration (K3GCRMClient/K3GCRMConnector):
//     companies, contacts, activities — relationship data, no ticket
//     concept at all (see k3gcrm.go's own comment).
//   - Ticket/service-desk integration (this file): an external ERP's
//     ticket lifecycle. CRMConnector already has the right method shape
//     for this, but its name collides with the customer/activity one —
//     that collision is exactly what let MockCRMConnector masquerade as
//     ticket-runtime authority before PRODUCT.6-B contained it.
//
// TicketingConnector does not replace CRMConnector yet and is not wired
// into any handler or server composition in this slice — it is the target
// shape a real per-tenant adapter (K3G or otherwise) will satisfy once its
// write contract is validated (PRODUCT.6-C3, not yet run).
//
// PRODUCT.6-C1 validated GET /api/support/tickets/{id} against the real K3G
// API; PRODUCT.6-H1 went further and directly validated CREATE with one
// authorized, controlled production POST (ticket.id 28180, HTTP 201,
// source="crm"). UpdateTicket/CloseTicket remain deferred — no write
// payload for status/priority/assignee mutation has been validated yet,
// and inventing one now would repeat the exact mistake IXCConnector made
// (a contract built from assumption, never exercised against a real
// environment).
type TicketingConnector interface {
	// Name identifies the provider (e.g. "k3g", "ixc") for logging/audit —
	// never used for behavior branching in caller code.
	Name() string

	// GetTicket reads an external ticket by the provider's own identifier
	// (the same value a local projection row would store as
	// external_ticket_id, PRODUCT.6-D). Returns a *TicketingError with a
	// TicketingErrorCode the caller can act on, never a bare error the
	// caller has to string-match.
	GetTicket(ctx context.Context, externalTicketID string) (*ExternalTicket, error)

	// CreateTicket opens a new external ticket. Implementations MUST NOT
	// retry the underlying write on their own — PRODUCT.6-H1 confirmed K3G
	// offers no Idempotency-Key/externalReference/correlationId, so a
	// blind retry after a timeout or ambiguous transport error could create
	// a duplicate ticket with no way to detect it. Exactly one write
	// attempt per call; application-level idempotency/reconciliation is a
	// future slice's responsibility, not this connector's.
	CreateTicket(ctx context.Context, req CreateTicketRequest) (*ExternalTicket, error)

	// UpdateTicketStatus mutates an EXISTING external ticket's lifecycle
	// status (PRODUCT.6-O2A3/O2B2 — confirmed real against K3G:
	// PUT /api/support/tickets/{id}/status, {"status":<int>}). Same
	// no-automatic-retry rule as CreateTicket, for the same reason (no
	// provider idempotency mechanism): exactly one write attempt per call.
	// Unlike CreateTicket, a same-target resubmission is confirmed safe by
	// the provider (PRODUCT.6-O2A3 real evidence, ticket 9115) — but that
	// fact belongs to application-level orchestration (O2B3), never a
	// reason for this method to add its own pre-GET or retry logic.
	UpdateTicketStatus(ctx context.Context, externalID string, target ExternalStatusTarget) (*ExternalTicket, error)
}

// ExternalStatusTarget is the provider-neutral status mutation command
// (PRODUCT.6-O2A3 section 5, PRODUCT.6-O2B2). Code is an OPAQUE string to
// OMNIRA/HTTP/frontend — never a K3G field name ("status"/"statusId") and
// never an OMNIRA lifecycle-intent enum (start/pend/resolve/close): K3G's
// own OpenAPI still ships this route as untyped ("pendente tipagem Zod",
// PRODUCT.6-O2A1), so freezing an intent vocabulary now would repeat the
// exact mistake IXCConnector already made. Each provider's own adapter
// owns validating/mapping Code into its real wire request — for K3G, the
// official vocabulary "1".."6" (PRODUCT.6-O2A2/O2A3).
type ExternalStatusTarget struct {
	Code string
}

// CreateTicketRequest is the provider-neutral V1 create command — OMNIRA
// business intent, never a provider's wire shape. Deliberately minimal:
// PRODUCT.6-H1's controlled production create proved a company reference +
// subject + description is sufficient for a real, successful K3G ticket
// (HTTP 201, ticket.id 28180). Requester, category, service, priority,
// urgency, assignee and status are all out of V1 scope — none of them were
// sent in the validated call, and the provider applied sane defaults
// (priority/urgency both defaulted to 3) without them.
//
// CustomerExternalID names the provider's own identifier for "which
// customer/company this ticket belongs to" (K3G's companyId) — not an
// OMNIRA domain concept, and deliberately not named CompanyID: OMNIRA has
// no company entity of its own, and a future non-K3G provider may key this
// by something other than a company (e.g. a subscriber/contract ID).
type CreateTicketRequest struct {
	CustomerExternalID string
	Subject            string
	Description        string
}

// ExternalTicket is the provider-neutral read shape returned by
// TicketingConnector.GetTicket. Field names intentionally mirror the local
// projection columns added in PRODUCT.6-D (external_status,
// external_status_label) so a future connector's output maps onto that
// schema without translation. No K3G-specific fields (no statusId as int,
// no urgency, no crmCompany) — those stay inside whatever adapter
// eventually implements this interface for a specific provider.
type ExternalTicket struct {
	ExternalID          string
	ExternalStatus      string
	ExternalStatusLabel string
}

// TicketingErrorCode is a provider-neutral error category every future
// TicketingConnector implementation must classify its failures into, so
// callers (CRMHandlers, or whatever replaces it once a real connector is
// wired) can react to the category, never to a provider's raw message
// string or HTTP status.
type TicketingErrorCode string

const (
	// TicketingNotFound: the external ticket genuinely does not exist for
	// this provider/identifier.
	TicketingNotFound TicketingErrorCode = "NOT_FOUND"
	// TicketingNotMigrated: the ticket exists in the provider's legacy
	// system but is not yet available through the path this connector
	// uses — confirmed real K3G behavior (PRODUCT.6-C1: 404 with
	// code:"CRM_TICKET_NOT_MIGRATED"). Must never be collapsed into
	// TicketingNotFound: a future read through a different path, or after
	// the provider finishes its own migration, may reveal the ticket.
	TicketingNotMigrated TicketingErrorCode = "NOT_MIGRATED"
	// TicketingUnauthorized: the configured credential was rejected.
	TicketingUnauthorized TicketingErrorCode = "UNAUTHORIZED"
	// TicketingValidationError: the request was rejected as malformed by
	// the provider (this connector's caller sent something the provider's
	// API does not accept).
	TicketingValidationError TicketingErrorCode = "VALIDATION_ERROR"
	// TicketingProviderUnavailable: the provider could not be reached, or
	// responded with a transient server error — retry may succeed.
	TicketingProviderUnavailable TicketingErrorCode = "PROVIDER_UNAVAILABLE"
	// TicketingUnknownProviderError: the provider responded with something
	// this connector does not recognize. Never silently treated as any of
	// the categories above.
	TicketingUnknownProviderError TicketingErrorCode = "UNKNOWN_PROVIDER_ERROR"
	// TicketingWriteOutcomeUnknown (PRODUCT.6-K1, generalized to status
	// mutation in PRODUCT.6-O2B2): a mutating call (CreateTicket or
	// UpdateTicketStatus) may or may not have been committed by the
	// provider — a transport failure, a 5xx/429 response, a malformed/
	// incomplete 2xx (no usable ticket id), or — for UpdateTicketStatus
	// specifically — a syntactically valid 2xx whose returned status does
	// not match the requested target, all leave OMNIRA unable to prove
	// success or failure. K3G offers no Idempotency-Key/externalReference/
	// correlationId (PRODUCT.6-H1/6-I), so none of these outcomes may ever
	// be collapsed into TicketingProviderUnavailable (which implies "safe
	// to consider not-written") nor trigger an automatic retry anywhere in
	// this connector or its callers. A caller that sees this code MUST
	// treat the write as possibly-succeeded and route to durable
	// reconciliation, never a second CreateTicket/UpdateTicketStatus call
	// for the same intent (PRODUCT.6-O2A3: at most one safe reconciliation
	// GetTicket read is allowed at the APPLICATION layer, never inside this
	// connector). GetTicket itself is a read and keeps using
	// TicketingProviderUnavailable for its own transport/5xx failures,
	// since a failed read has no write-duplication risk.
	TicketingWriteOutcomeUnknown TicketingErrorCode = "WRITE_OUTCOME_UNKNOWN"
)

// TicketingError is the error type every TicketingConnector method returns
// on failure. Code is always one of the TicketingErrorCode constants above;
// Err optionally wraps the underlying cause (network error, decode error)
// for logs, never for caller branching.
type TicketingError struct {
	Code    TicketingErrorCode
	Message string
	Err     error
}

func (e *TicketingError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("ticketing: %s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("ticketing: %s: %s", e.Code, e.Message)
}

func (e *TicketingError) Unwrap() error { return e.Err }

// TicketingErrorCodeOf extracts the TicketingErrorCode from err, or
// TicketingUnknownProviderError if err is nil, not a *TicketingError, or
// otherwise unclassified — callers should always be able to switch on a
// code without a type assertion that might panic or silently fall through.
func TicketingErrorCodeOf(err error) TicketingErrorCode {
	var te *TicketingError
	if errors.As(err, &te) {
		return te.Code
	}
	return TicketingUnknownProviderError
}

// TicketingConnectorResolver is the smallest provider-neutral seam between
// "a tenant" and "the TicketingConnector configured for it", if any. No
// implementation exists yet in this slice: no registry, no new
// integration table, no K3G hardcode, no global default provider. A future
// slice implements this against the existing channel_connections/
// channel_credentials infrastructure (the same one K3G CRM and WAHA
// already use) — this interface only fixes the shape callers will depend
// on, so CRMHandlers' containment check (PRODUCT.6-B: "no connector
// configured" => 503) can eventually become "resolver returns nil,
// nil" => 503, without a second wiring path ever needing to exist.
type TicketingConnectorResolver interface {
	// ResolveTicketingConnector returns the connector configured for
	// tenantID, or (nil, nil) if no real ticketing provider is configured
	// — never a fake/mock connector, never an error for the "not
	// configured" case (that is the expected, common state today).
	ResolveTicketingConnector(ctx context.Context, tenantID uuid.UUID) (TicketingConnector, error)
}
