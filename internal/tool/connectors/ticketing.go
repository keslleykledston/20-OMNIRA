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
// V1 is deliberately read-only: PRODUCT.6-C1 validated GET
// /api/support/tickets/{id} against the real K3G API, but no write payload
// (POST create, PUT status/priority/assignee) — inventing those shapes now
// would repeat the exact mistake IXCConnector already made (a contract
// built from assumption, never exercised against a real environment).
// CreateTicket/UpdateTicket/CloseTicket are deferred until PRODUCT.6-C3
// validates real request/response payloads.
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
