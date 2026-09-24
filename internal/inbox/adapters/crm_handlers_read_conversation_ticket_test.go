package adapters

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// PRODUCT.6-O1 HTTP tests. Real *ticketsapplication.ReadConversationTicketService
// behind fakes implementing the tickets/ports interfaces — never a real
// Postgres/K3G call.

type readTicketHarness struct {
	conversation *httpFakeConversation
	localTickets *httpFakeLocalTickets
	handler      *CRMHandlers
	tenantID     uuid.UUID
	actorID      uuid.UUID
	convID       uuid.UUID
}

func newReadTicketHarness(t *testing.T) *readTicketHarness {
	t.Helper()
	actorID := uuid.New()
	h := &readTicketHarness{
		conversation: &httpFakeConversation{found: true, assignedTo: &actorID},
		localTickets: &httpFakeLocalTickets{},
		tenantID:     uuid.New(), actorID: actorID, convID: uuid.New(),
	}
	svc := ticketsapplication.NewReadConversationTicketService(&httpFakePerms{granted: map[string]bool{}}, h.conversation, h.localTickets)
	handler := NewCRMHandlers(nil)
	handler.SetReadTicketService(svc)
	h.handler = handler
	return h
}

func (h *readTicketHarness) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", h.handler.GetCurrentTicket)
	return mux
}

func (h *readTicketHarness) request() *http.Request {
	path := "/api/v1/tenants/" + h.tenantID.String() + "/conversations/" + h.convID.String() + "/ticket"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: h.actorID})
	tc, err := tenancydomain.NewTenantContext(h.tenantID, h.actorID, tenancydomain.AccessSourceDirect)
	if err != nil {
		panic(err)
	}
	ctx = tenancydomain.WithTenantContext(ctx, tc)
	return req.WithContext(ctx)
}

// A. linked projection returns provider-neutral JSON.
func TestReadConversationTicketHTTPLinkedReturnsProviderNeutralJSON(t *testing.T) {
	h := newReadTicketHarness(t)
	provider, externalID, status, label, sync := "k3g", "28182", "1", "Novo", "synced"
	ticketID := uuid.New()
	h.localTickets.candidate = &ticketsdomain.Ticket{
		ID: ticketID, Provider: &provider, ExternalTicketID: &externalID,
		ExternalStatus: &status, ExternalStatusLabel: &label, SyncStatus: &sync,
	}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp conversationTicketResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Linked || resp.LocalTicketID != ticketID || resp.Provider != provider || resp.ExternalTicketID != externalID ||
		resp.ExternalStatus != status || resp.ExternalStatusLabel != label || resp.SyncStatus != sync {
		t.Fatalf("unexpected response: %+v", resp)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"companyId", "\"token\"", "Bearer", "assigneeUserId", "acceptanceState"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked provider/credential internals (%q): %s", forbidden, body)
		}
	}
}

// B. unlinked local ticket returns 200 + linked=false.
func TestReadConversationTicketHTTPUnlinkedReturns200(t *testing.T) {
	h := newReadTicketHarness(t)
	ticketID := uuid.New()
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: ticketID}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp conversationTicketResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Linked || resp.LocalTicketID != ticketID {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// C. no active ticket returns 404.
func TestReadConversationTicketHTTPNoActiveTicketReturns404(t *testing.T) {
	h := newReadTicketHarness(t)
	h.localTickets.candidate = nil
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// D. unauthorized conversation rejected.
func TestReadConversationTicketHTTPUnauthorizedConversationRejected(t *testing.T) {
	h := newReadTicketHarness(t)
	other := uuid.New()
	h.conversation.assignedTo = &other
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New()}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// E. tenant/actor come from authenticated context, not request input —
// structural proof: GetCurrentTicket's request has no body at all (GET),
// and the command is built exclusively from r.PathValue + tenancydomain
// TenantContext (see the handler); this test proves a path tenant_id that
// disagrees with the session's own tenant is rejected, the same invariant
// already proven for CreateTicket (PRODUCT.6-M).
func TestReadConversationTicketHTTPPathTenantMismatchWithSessionRejected(t *testing.T) {
	h := newReadTicketHarness(t)
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New()}
	otherTenantInPath := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+otherTenantInPath.String()+"/conversations/"+h.convID.String()+"/ticket", nil)
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: h.actorID})
	tc, err := tenancydomain.NewTenantContext(h.tenantID, h.actorID, tenancydomain.AccessSourceDirect)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(tenancydomain.WithTenantContext(ctx, tc))
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("a path tenant_id disagreeing with the session's own tenant must never succeed, got 200: %s", rec.Body.String())
	}
}

// F. K3G outage/config absence has no effect on local GET — structural
// proof: readTicketHarness never constructs a TicketingRuntimeResolver or
// TicketingConnector at all, and the successful-linked test above (A)
// already passes without one configured.
func TestReadConversationTicketHTTPHasNoProviderDependency(t *testing.T) {
	h := newReadTicketHarness(t)
	provider, externalID := "k3g", "28182"
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New(), Provider: &provider, ExternalTicketID: &externalID}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with no provider/runtime wired: %s", rec.Code, rec.Body.String())
	}
}

// G. legacy CRMConnector injection does not bypass/newly control this route.
func TestReadConversationTicketHTTPLegacyCRMConnectorDoesNotControlRoute(t *testing.T) {
	h := newReadTicketHarness(t)
	h.localTickets.candidate = nil // no active ticket -> should still 404
	h.handler.SetCRMConnector(connectors.NewMockCRMConnector())
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("legacy h.crm injection must not change GetCurrentTicket's real behavior, got %d: %s", rec.Code, rec.Body.String())
	}
}

// H. GetCurrentTicket without a wired read service remains contained (503) —
// proves nil readTicketService, not the legacy h.crm, gates this route.
func TestReadConversationTicketHTTPUnwiredServiceIsContained(t *testing.T) {
	h := NewCRMHandlers(nil)
	rec := httptest.NewRecorder()
	req := authedRequest(http.MethodGet, "/api/v1/tenants/"+uuid.NewString()+"/conversations/"+uuid.NewString()+"/ticket", "")
	h.GetCurrentTicket(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}
