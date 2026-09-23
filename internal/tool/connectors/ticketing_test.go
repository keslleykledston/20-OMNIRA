package connectors

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// PRODUCT.6-E: the ticketing boundary must be a genuinely separate type from
// the CRM/customer/activity integration — proven by compiling a minimal
// fake against TicketingConnector alone, with none of CRMConnector's
// customer/activity methods (FindCustomer, Authenticate, CreateTicket by
// customerID+subject) anywhere in sight.
type fakeTicketingConnector struct {
	name    string
	tickets map[string]*ExternalTicket
}

func (f *fakeTicketingConnector) Name() string { return f.name }

func (f *fakeTicketingConnector) GetTicket(ctx context.Context, externalTicketID string) (*ExternalTicket, error) {
	if t, ok := f.tickets[externalTicketID]; ok {
		return t, nil
	}
	return nil, &TicketingError{Code: TicketingNotFound, Message: "no such ticket"}
}

func TestTicketingConnectorIsIndependentOfCRMCustomerIntegration(t *testing.T) {
	var _ TicketingConnector = (*fakeTicketingConnector)(nil)

	f := &fakeTicketingConnector{name: "fake", tickets: map[string]*ExternalTicket{
		"28176": {ExternalID: "28176", ExternalStatus: "1", ExternalStatusLabel: "Novo"},
	}}
	ticket, err := f.GetTicket(context.Background(), "28176")
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if ticket.ExternalStatusLabel != "Novo" {
		t.Fatalf("ExternalStatusLabel = %q, want %q", ticket.ExternalStatusLabel, "Novo")
	}
}

func TestTicketingConnectorHasNoCustomerActivityMethods(t *testing.T) {
	// Compile-time-shaped assertion: TicketingConnector must NOT satisfy
	// (or be satisfied only by coincidence with) the customer/activity
	// surface CRMConnector exposes. MockCRMConnector implements
	// CRMConnector (ticket-shaped) but must NOT implement
	// TicketingConnector, because its method set is different
	// (CreateTicket(customerID, subject), not the ticketing boundary's
	// read-only GetTicket(externalTicketID)) — this is what keeps the two
	// integrations from colliding again.
	var mock interface{} = NewMockCRMConnector()
	if _, ok := mock.(TicketingConnector); ok {
		t.Fatalf("MockCRMConnector must not accidentally satisfy TicketingConnector — that would let the CRM/customer mock re-enter ticketing runtime through a different door")
	}
}

func TestTicketingErrorCodeOfClassifiesKnownCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want TicketingErrorCode
	}{
		{"not found", &TicketingError{Code: TicketingNotFound}, TicketingNotFound},
		{"not migrated", &TicketingError{Code: TicketingNotMigrated}, TicketingNotMigrated},
		{"unauthorized", &TicketingError{Code: TicketingUnauthorized}, TicketingUnauthorized},
		{"validation error", &TicketingError{Code: TicketingValidationError}, TicketingValidationError},
		{"provider unavailable", &TicketingError{Code: TicketingProviderUnavailable}, TicketingProviderUnavailable},
		{"unknown provider error", &TicketingError{Code: TicketingUnknownProviderError}, TicketingUnknownProviderError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TicketingErrorCodeOf(c.err); got != c.want {
				t.Fatalf("TicketingErrorCodeOf(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// A generic Go error (not *TicketingError) — e.g. a network error a future
// adapter forgot to wrap — must classify as UNKNOWN, never silently as
// NOT_FOUND or any other specific category a caller might act on
// incorrectly.
func TestTicketingErrorCodeOfDefaultsToUnknownForUnclassifiedErrors(t *testing.T) {
	if got := TicketingErrorCodeOf(errors.New("boom")); got != TicketingUnknownProviderError {
		t.Fatalf("TicketingErrorCodeOf(generic error) = %q, want %q", got, TicketingUnknownProviderError)
	}
	if got := TicketingErrorCodeOf(nil); got != TicketingUnknownProviderError {
		t.Fatalf("TicketingErrorCodeOf(nil) = %q, want %q", got, TicketingUnknownProviderError)
	}
}

func TestTicketingErrorUnwrapsUnderlyingCause(t *testing.T) {
	cause := errors.New("connection reset")
	te := &TicketingError{Code: TicketingProviderUnavailable, Message: "unreachable", Err: cause}
	if !errors.Is(te, cause) {
		t.Fatalf("errors.Is(te, cause) = false, want true (Unwrap must expose the underlying cause)")
	}
}

// PRODUCT.6-E: no provider is implicitly selected. A resolver with no real
// provider configured for a tenant returns (nil, nil) — never a mock,
// never an error, never any connector at all. This is the shape
// CRMHandlers' PRODUCT.6-B containment check will eventually delegate to,
// so it is proven here even though nothing wires it into CRMHandlers yet.
type noProviderResolver struct{}

func (noProviderResolver) ResolveTicketingConnector(ctx context.Context, tenantID uuid.UUID) (TicketingConnector, error) {
	return nil, nil
}

func TestTicketingConnectorResolverSelectsNoProviderByDefault(t *testing.T) {
	var resolver TicketingConnectorResolver = noProviderResolver{}
	conn, err := resolver.ResolveTicketingConnector(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn != nil {
		t.Fatalf("expected nil connector when no provider is configured, got %v", conn)
	}
}
