package adapters

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/tool/connectors"
)

func authedRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	return req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: uuid.New()}))
}

// PRODUCT.6-B core invariant: repeated requests against the canonical
// (connector-less) runtime configuration must never reveal any state —
// there must be nothing to leak, because nothing is ever created.
func TestCRMHandlersRepeatedGetDoesNotRevealFakeState(t *testing.T) {
	h := NewCRMHandlers(nil)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.GetCurrentTicket(rec, authedRequest(http.MethodGet, "/x", ""))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("call %d: got %d, want 503", i, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "\"id\"") {
			t.Fatalf("call %d: response must never look like a ticket payload: %s", i, rec.Body.String())
		}
	}
}

// PRODUCT.6-M superseded assertion: CreateTicket's containment check used
// to be h.crm == nil (the same field GET/UPDATE/CLOSE still use below).
// Since PRODUCT.6-M, CreateTicket is gated on h.externalTicketService
// instead — a real per-tenant application service, never the legacy
// CRMConnector mock. Injecting the OLD h.crm connector must NOT bypass
// CreateTicket's containment anymore (that would silently reopen the
// PRODUCT.6-B hole this test used to guard against).
func TestCRMHandlersInjectedLegacyCRMConnectorDoesNotBypassCreateTicketContainment(t *testing.T) {
	h := NewCRMHandlers(nil)
	h.SetCRMConnector(connectors.NewMockCRMConnector())

	rec := httptest.NewRecorder()
	req := authedRequest(http.MethodPost, "/api/v1/tenants/"+uuid.NewString()+"/conversations/"+uuid.NewString()+"/ticket", `{}`)
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy h.crm injection must not activate CreateTicket, want 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

// GetCurrentTicket/GetTicket/UpdateTicket/CloseTicket remain gated on the
// legacy h.crm field (PRODUCT.6-B containment, unchanged by PRODUCT.6-M —
// no proven real path exists for them yet). Proven here with the mock only
// as a test utility (SetCRMConnector is explicitly documented as test-only;
// canonical runtime composition in server.go must never call it).
func TestCRMHandlersGetCurrentTicketFunctionsWithAnInjectedConnector(t *testing.T) {
	h := NewCRMHandlers(nil)
	h.SetCRMConnector(connectors.NewMockCRMConnector())

	rec := httptest.NewRecorder()
	req := authedRequest(http.MethodGet, "/api/v1/tenants/"+uuid.NewString()+"/conversations/"+uuid.NewString()+"/ticket", "")
	h.GetCurrentTicket(rec, req)
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("an injected connector must bypass GetCurrentTicket's containment check, got 503: %s", rec.Body.String())
	}
}

// server.go must never wire a fake connector into production/pilot runtime
// composition. This is asserted by repository convention (documented in the
// PRODUCT.6-B human gate) rather than re-parsed here; NewCRMHandlers itself
// is the enforcement point — proven by the fact that a fresh handler has no
// connector until a caller explicitly injects one.
func TestNewCRMHandlersHasNoConnectorByDefault(t *testing.T) {
	h := NewCRMHandlers(nil)
	if h.crm != nil {
		t.Fatalf("NewCRMHandlers must not install any ticketing connector by default")
	}
}
