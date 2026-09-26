package ports

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// TicketingRuntime bundles the two provider-neutral capabilities
// CreateExternalTicket needs for one tenant (PRODUCT.6-L). Both are
// derived from the SAME configured K3G credential (PRODUCT.6-C1: one
// Bearer token authenticates both the CRM/customer namespace and the
// support/ticket namespace) — a single small struct, not two independent
// resolvers, because they always succeed or fail together for a given
// tenant and resolving them separately would just decrypt the same
// credential twice for no isolation benefit.
//
// ConnectionID (PRODUCT.7B2B) is additive integration metadata: the exact
// channel_connections row this runtime was built from. It exists so a
// caller (CreateExternalTicket's Result) can durably record WHICH tenant
// integration/workspace produced a successful operation, without ever
// re-querying channel_connections afterward and assuming it is still the
// same row. Value comes directly from the resolved connection (conn.ID) —
// never hardcoded. There is deliberately no separate Provider field here:
// channel_connections.provider is immutable after creation (see
// internal/channels/adapters/postgres.go's Update, which never touches
// it), so ConnectionID alone already durably and immutably qualifies the
// provider/workspace — a second Provider column/field would only risk
// drifting from it with nothing to enforce consistency. A caller that
// needs the provider string reads it from the referenced
// channel_connections row, never from a duplicated copy here.
type TicketingRuntime struct {
	CompanyDirectory   CompanyDirectory
	TicketingConnector connectors.TicketingConnector
	ConnectionID       uuid.UUID
}

// ResolutionErrorCode classifies why a tenant's ticketing runtime could not
// be resolved — a configuration/credential problem, never a live provider
// outage (that surfaces later as a connectors.TicketingError once a real
// call is made through the resolved TicketingConnector).
type ResolutionErrorCode string

const (
	// ResolutionNoConfiguration: the tenant has no applicable K3G
	// connection at all. Not an error condition from the tenant's
	// perspective — ticketing is simply not configured yet.
	ResolutionNoConfiguration ResolutionErrorCode = "NO_CONFIGURATION"
	// ResolutionAmbiguousConfiguration: more than one applicable
	// connection exists for this tenant. Never resolved by picking the
	// first/newest row — an operator must fix the configuration.
	ResolutionAmbiguousConfiguration ResolutionErrorCode = "AMBIGUOUS_CONFIGURATION"
	// ResolutionCredentialNotFound: the connection references a
	// secret_ref that does not exist in channel_credentials (dangling
	// reference).
	ResolutionCredentialNotFound ResolutionErrorCode = "CREDENTIAL_NOT_FOUND"
	// ResolutionCredentialInvalid: the secret_ref is malformed, the
	// stored ciphertext could not be decrypted, or the decrypted payload
	// is missing a required field (base_url/token).
	ResolutionCredentialInvalid ResolutionErrorCode = "CREDENTIAL_INVALID"
	// ResolutionUnsupportedProvider: an applicable connection exists but
	// its provider is not one this resolver knows how to construct
	// capabilities for.
	ResolutionUnsupportedProvider ResolutionErrorCode = "UNSUPPORTED_PROVIDER"
)

// ResolutionError is the error type every TicketingRuntimeResolver
// implementation returns on failure. Message/Err are for logs only —
// never include credential material (PRODUCT.6-L section 7: never log,
// print, or leak the secret; never expose it through an error message).
type ResolutionError struct {
	Code    ResolutionErrorCode
	Message string
	Err     error
}

func (e *ResolutionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("ticketing runtime: %s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("ticketing runtime: %s: %s", e.Code, e.Message)
}

func (e *ResolutionError) Unwrap() error { return e.Err }

// TicketingRuntimeResolver resolves the tenant-scoped ticketing runtime.
// Implementations MUST fail closed: never fall back to another tenant's
// connection, never use a hardcoded/default connection, and never return a
// capability built from more than one applicable connection.
type TicketingRuntimeResolver interface {
	Resolve(ctx context.Context, tenantID uuid.UUID) (*TicketingRuntime, error)
}
