package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// PRODUCT.6-O1R HTTP tests. A REAL *ticketsapplication.RefreshTicketProjectionService
// behind fakes implementing the tickets/ports interfaces — never a real
// Postgres/K3G call.

const (
	refreshHTTPProvider   = "k3g"
	refreshHTTPExternalID = "28182"
)

// refreshHTTPFakeTicketing supports a configurable Name() (for provider
// mismatch tests) and GetTicket — httpFakeTicketing (used by the CreateTicket
// HTTP tests) hardcodes Name()="k3g" and has no GetTicket support, so this
// slice defines its own narrow fake rather than widening that one's scope.
type refreshHTTPFakeTicketing struct {
	name        string
	getCalls    int
	getResult   *connectors.ExternalTicket
	getErr      error
	createCalls int
}

func (f *refreshHTTPFakeTicketing) Name() string {
	if f.name == "" {
		return refreshHTTPProvider
	}
	return f.name
}
func (f *refreshHTTPFakeTicketing) GetTicket(ctx context.Context, externalTicketID string) (*connectors.ExternalTicket, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResult, nil
}
func (f *refreshHTTPFakeTicketing) CreateTicket(ctx context.Context, req connectors.CreateTicketRequest) (*connectors.ExternalTicket, error) {
	f.createCalls++
	return nil, nil
}
func (f *refreshHTTPFakeTicketing) UpdateTicketStatus(ctx context.Context, externalID string, target connectors.ExternalStatusTarget) (*connectors.ExternalTicket, error) {
	return nil, errors.New("not used")
}

type refreshTicketHarness struct {
	conversation *httpFakeConversation
	localTickets *httpFakeLocalTickets
	ticketing    *refreshHTTPFakeTicketing
	runtime      *httpFakeRuntimeResolver
	handler      *CRMHandlers
	tenantID     uuid.UUID
	actorID      uuid.UUID
	convID       uuid.UUID
}

func newRefreshTicketHarness(t *testing.T) *refreshTicketHarness {
	t.Helper()
	actorID := uuid.New()
	linkedTicket := &ticketsdomain.Ticket{ID: uuid.New()}
	provider, externalID := refreshHTTPProvider, refreshHTTPExternalID
	linkedTicket.Provider, linkedTicket.ExternalTicketID = &provider, &externalID

	h := &refreshTicketHarness{
		conversation: &httpFakeConversation{found: true, assignedTo: &actorID},
		localTickets: &httpFakeLocalTickets{candidate: linkedTicket},
		ticketing: &refreshHTTPFakeTicketing{
			getResult: &connectors.ExternalTicket{ExternalID: refreshHTTPExternalID, ExternalStatus: "2", ExternalStatusLabel: "Em atendimento"},
		},
		tenantID: uuid.New(), actorID: actorID, convID: uuid.New(),
	}
	h.runtime = &httpFakeRuntimeResolver{ticketing: h.ticketing}
	svc := ticketsapplication.NewRefreshTicketProjectionService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketCreate: true}},
		h.conversation, h.localTickets, h.runtime,
	)
	handler := NewCRMHandlers(nil)
	handler.SetRefreshTicketService(svc)
	h.handler = handler
	return h
}

func (h *refreshTicketHarness) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/refresh", h.handler.RefreshTicket)
	return mux
}

func (h *refreshTicketHarness) request() *http.Request {
	path := "/api/v1/tenants/" + h.tenantID.String() + "/conversations/" + h.convID.String() + "/ticket/refresh"
	req := httptest.NewRequest(http.MethodPost, path, nil)
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: h.actorID})
	tc, err := tenancydomain.NewTenantContext(h.tenantID, h.actorID, tenancydomain.AccessSourceDirect)
	if err != nil {
		panic(err)
	}
	ctx = tenancydomain.WithTenantContext(ctx, tc)
	return req.WithContext(ctx)
}

// successful refresh response.
func TestRefreshTicketHTTPSuccessReturnsProviderNeutralJSON(t *testing.T) {
	h := newRefreshTicketHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp conversationTicketResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Linked || resp.Provider != refreshHTTPProvider || resp.ExternalTicketID != refreshHTTPExternalID ||
		resp.ExternalStatus != "2" || resp.ExternalStatusLabel != "Em atendimento" || resp.SyncStatus != "synced" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if h.ticketing.getCalls != 1 {
		t.Fatalf("GetTicket called %d times, want exactly 1", h.ticketing.getCalls)
	}
	if h.ticketing.createCalls != 0 {
		t.Fatalf("CreateTicket called %d times, want 0", h.ticketing.createCalls)
	}
}

// unauthorized conversation.
func TestRefreshTicketHTTPUnauthorizedConversationRejected(t *testing.T) {
	h := newRefreshTicketHarness(t)
	other := uuid.New()
	h.conversation.assignedTo = &other
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket must not be called, got %d", h.ticketing.getCalls)
	}
}

// linked=false -> conflict, unavailable for refresh.
func TestRefreshTicketHTTPUnlinkedReturnsConflict(t *testing.T) {
	h := newRefreshTicketHarness(t)
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New()}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket must not be called, got %d", h.ticketing.getCalls)
	}
}

// no active local ticket -> 404.
func TestRefreshTicketHTTPNoActiveTicketReturns404(t *testing.T) {
	h := newRefreshTicketHarness(t)
	h.localTickets.candidate = nil
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// provider mismatch -> reconciliation-required (409), no GetTicket.
func TestRefreshTicketHTTPProviderMismatchReturnsConflict(t *testing.T) {
	h := newRefreshTicketHarness(t)
	h.ticketing.name = "a-different-provider"
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket must not be called, got %d", h.ticketing.getCalls)
	}
}

// provider unavailable -> 503.
func TestRefreshTicketHTTPProviderUnavailableReturns503(t *testing.T) {
	h := newRefreshTicketHarness(t)
	h.ticketing.getErr = &connectors.TicketingError{Code: connectors.TicketingProviderUnavailable, Message: "down"}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// NOT_FOUND -> reconciliation-required, link preserved (no CREATE — proven
// by createCalls staying 0).
func TestRefreshTicketHTTPExternalNotFoundReturnsConflict(t *testing.T) {
	h := newRefreshTicketHarness(t)
	h.ticketing.getErr = &connectors.TicketingError{Code: connectors.TicketingNotFound, Message: "not found"}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.createCalls != 0 {
		t.Fatalf("CreateTicket called %d times, want 0", h.ticketing.createCalls)
	}
}

// external ID mismatch -> reconciliation-required.
func TestRefreshTicketHTTPExternalIDMismatchReturnsConflict(t *testing.T) {
	h := newRefreshTicketHarness(t)
	h.ticketing.getResult = &connectors.ExternalTicket{ExternalID: "99999", ExternalStatus: "2", ExternalStatusLabel: "Em atendimento"}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// inconsistent local linkage -> reconciliation-required.
func TestRefreshTicketHTTPInconsistentLinkageReturnsConflict(t *testing.T) {
	h := newRefreshTicketHarness(t)
	externalID := refreshHTTPExternalID
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New(), ExternalTicketID: &externalID}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket must not be called, got %d", h.ticketing.getCalls)
	}
}

// runtime resolution failure (no/ambiguous tenant ticketing configuration)
// maps to the same 503 convention as CreateTicket.
func TestRefreshTicketHTTPRuntimeResolutionFailureReturns503(t *testing.T) {
	h := newRefreshTicketHarness(t)
	h.runtime.err = &ticketsports.ResolutionError{Code: ticketsports.ResolutionNoConfiguration, Message: "no K3G connection configured"}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// unwired service remains contained (503) — proves nil refreshTicketService,
// not the legacy h.crm, gates this route.
func TestRefreshTicketHTTPUnwiredServiceIsContained(t *testing.T) {
	h := NewCRMHandlers(nil)
	rec := httptest.NewRecorder()
	req := authedRequest(http.MethodPost, "/api/v1/tenants/"+uuid.NewString()+"/conversations/"+uuid.NewString()+"/ticket/refresh", "")
	h.RefreshTicket(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// legacy CRMConnector injection does not bypass/newly control this route.
func TestRefreshTicketHTTPLegacyCRMConnectorDoesNotControlRoute(t *testing.T) {
	h := NewCRMHandlers(nil)
	h.SetCRMConnector(connectors.NewMockCRMConnector())
	rec := httptest.NewRecorder()
	req := authedRequest(http.MethodPost, "/api/v1/tenants/"+uuid.NewString()+"/conversations/"+uuid.NewString()+"/ticket/refresh", "")
	h.RefreshTicket(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy h.crm injection must not activate RefreshTicket, got %d: %s", rec.Code, rec.Body.String())
	}
}

// never sends provider credential material in the response.
func TestRefreshTicketHTTPNeverLeaksCredentials(t *testing.T) {
	h := newRefreshTicketHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request())
	body := rec.Body.String()
	for _, forbidden := range []string{"\"token\"", "Bearer", "companyId", "assigneeUserId"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked provider/credential internals (%q): %s", forbidden, body)
		}
	}
}
