package ports

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// GatewayErrorKind is how an adapter classifies a failure, because the module must act differently on each (ADR-0013's lesson: a timeout is not a failure).
type GatewayErrorKind string

const (
	// GatewayRejected: the ERP DEFINITIVELY refused (validation, permission, unknown customer). Nothing was created; a retry may be tried.
	GatewayRejected GatewayErrorKind = "rejected"
	// GatewayNotFound: the ERP has no such record.
	GatewayNotFound GatewayErrorKind = "not_found"
	// GatewayUnknown: a timeout, a dropped connection or a 5xx. The ERP may or may not have processed the request: NEVER treated as a failure.
	GatewayUnknown GatewayErrorKind = "unknown"
	// GatewayNotSupported: the adapter does not do this operation.
	GatewayNotSupported GatewayErrorKind = "not_supported"
)

// GatewayError carries the kind and a short, closed class (a token, never provider text) for the history.
type GatewayError struct {
	Kind  GatewayErrorKind
	Class string
	Err   error
}

func (e *GatewayError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("gateway %s (%s): %v", e.Kind, e.Class, e.Err)
	}
	return fmt.Sprintf("gateway %s (%s)", e.Kind, e.Class)
}
func (e *GatewayError) Unwrap() error { return e.Err }

// KindOf classifies any error from a gateway call; one that is not a GatewayError is treated as UNKNOWN (the safe reading).
func KindOf(err error) (GatewayErrorKind, string) {
	var ge *GatewayError
	if errors.As(err, &ge) {
		return ge.Kind, ge.Class
	}
	return GatewayUnknown, "transport"
}

// CreateOrderRequest is what the ERP is asked to create. CorrelationKey is a stable reference made by OMNIRA: an ERP that can keep it lets the record be found
// again when the answer was lost.
type CreateOrderRequest struct {
	CorrelationKey string
	Title          string
	Description    string
	// CustomerRef is the ERP's own id of the customer when the session has one linked for this integration ("" otherwise).
	CustomerRef string
}

// ServiceOrderGateway is the ONLY door to an ERP/CRM for this module. The domain knows no provider: an adapter implements it per provider. It never retries a write.
type ServiceOrderGateway interface {
	System() string
	Get(ctx context.Context, externalType, externalID string) (*domain.Snapshot, error)
	CreateServiceOrder(ctx context.Context, req CreateOrderRequest) (*domain.Snapshot, error)
}

// CorrelationLookup is an OPTIONAL capability: finding again a record created with a correlation key. An ERP without it (K3G offers none, ADR-0013) can only be
// reconciled by hand.
type CorrelationLookup interface {
	FindByCorrelation(ctx context.Context, correlationKey string) (*domain.Snapshot, error)
}

// GatewayResolver returns the gateway of a company's integration, or domain.ErrNotConfigured. It never returns a fake one. The application has already proven that
// the integration belongs to the company; an implementation must still resolve the credentials by BOTH ids (the integration AND the company), never by the integration
// alone, so an adapter can never be reached with another company's credentials.
type GatewayResolver interface {
	Resolve(ctx context.Context, tenantID, integrationID uuid.UUID) (ServiceOrderGateway, error)
}

// NotConfigured is the resolver used when no adapter is installed: every integration answers ErrNotConfigured.
type NotConfigured struct{}

func (NotConfigured) Resolve(context.Context, uuid.UUID, uuid.UUID) (ServiceOrderGateway, error) {
	return nil, domain.ErrNotConfigured
}

// ---- repository ----

type RecordFilter struct {
	IntegrationID *uuid.UUID
	ExternalType  string
	ExternalID    string
	StatusClass   domain.StatusClass
	SessionID     *uuid.UUID
	CaseID        *uuid.UUID
	TaskID        *uuid.UUID
	Cursor        string
	Limit         int
}

type RequestFilter struct {
	State     domain.RequestState
	SessionID *uuid.UUID
	Cursor    string
	Limit     int
}

type ExternalRepository interface {
	// IntegrationSystem returns the provider of an integration of this company (visible under RLS), "" and false when it is not one.
	IntegrationSystem(ctx context.Context, integrationID uuid.UUID) (string, bool, error)
	GetRecord(ctx context.Context, id uuid.UUID) (*domain.ExternalRecord, error)
	LockRecord(ctx context.Context, id uuid.UUID) (*domain.ExternalRecord, error)
	FindRecord(ctx context.Context, integrationID uuid.UUID, externalType, externalID string) (*domain.ExternalRecord, error)
	// InsertRecord creates the record, or returns the one that already stood (unique by integration + type + id) with created=false.
	InsertRecord(ctx context.Context, r *domain.ExternalRecord) (rec *domain.ExternalRecord, created bool, err error)
	// ApplySnapshot writes the snapshot when it is not OLDER than the one applied (by the provider's update time). It reports whether it was applied and whether the
	// status class or label changed. An older snapshot is ignored without error (out-of-order delivery).
	ApplySnapshot(ctx context.Context, id uuid.UUID, snap domain.Snapshot, now time.Time) (applied, statusChanged bool, rec *domain.ExternalRecord, err error)
	MarkSync(ctx context.Context, id uuid.UUID, state string, now time.Time) error
	ListRecords(ctx context.Context, f RecordFilter) ([]domain.ExternalRecord, string, error)

	InsertLink(ctx context.Context, l *domain.ExternalLink) (created bool, existing *domain.ExternalLink, err error)
	GetLink(ctx context.Context, id uuid.UUID) (*domain.ExternalLink, error)
	ActiveLinks(ctx context.Context, recordID uuid.UUID) ([]domain.ExternalLink, error)
	Unlink(ctx context.Context, id uuid.UUID, by *uuid.UUID, at time.Time) (bool, error)

	AcquireRequest(ctx context.Context, r *domain.ExternalRequest) (acquired *domain.ExternalRequest, existing *domain.ExternalRequest, blocker *domain.ExternalRequest, err error)
	GetRequest(ctx context.Context, id uuid.UUID) (*domain.ExternalRequest, error)
	LockRequest(ctx context.Context, id uuid.UUID) (*domain.ExternalRequest, error)
	ListRequests(ctx context.Context, f RequestFilter) ([]domain.ExternalRequest, string, error)
	UpdateRequest(ctx context.Context, r *domain.ExternalRequest, expectedVersion int) error

	// InNewTransaction runs fn in its OWN transaction for the same company and the same actor, committed when fn returns: the durable attempt must not depend on the
	// request's transaction (a crash or a rollback after the ERP answered must not make the request look as if it never happened).
	InNewTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}
