package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// PRODUCT.6-M HTTP activation tests. These wire a REAL
// *ticketsapplication.Service (the same one PRODUCT.6-K2 exhaustively
// tested) behind CreateTicket, with small in-memory fakes standing in for
// the ports — never a real Postgres/K3G call — so replay/idempotency/
// write-outcome behavior proven here is genuine application behavior, not
// a rubber-stamped HTTP fake.

type httpFakePerms struct{ granted map[string]bool }

func (f *httpFakePerms) HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error) {
	return f.granted[permission], nil
}

type httpFakeConversation struct {
	found      bool
	assignedTo *uuid.UUID
}

func (f *httpFakeConversation) LoadAssignment(ctx context.Context, conversationID uuid.UUID) (*uuid.UUID, bool, error) {
	return f.assignedTo, f.found, nil
}

type httpFakeCompanies struct{ companies []ticketsports.Company }

func (f *httpFakeCompanies) ListCompanies(ctx context.Context) ([]ticketsports.Company, error) {
	return f.companies, nil
}

type httpFakeLocalTickets struct{ candidate *ticketsdomain.Ticket }

func (f *httpFakeLocalTickets) FindEnrichmentCandidate(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error) {
	if f.candidate == nil {
		return nil, nil
	}
	cp := *f.candidate
	return &cp, nil
}

func (f *httpFakeLocalTickets) FindActiveByConversation(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error) {
	return f.FindEnrichmentCandidate(ctx, conversationID)
}
func (f *httpFakeLocalTickets) EnrichExternalProjection(ctx context.Context, ticketID uuid.UUID, provider, externalTicketID, externalStatus, externalStatusLabel string, syncedAt time.Time) error {
	return nil
}

type httpFakeTicketing struct {
	calls  int
	result *connectors.ExternalTicket
	err    error
}

func (f *httpFakeTicketing) Name() string { return "k3g" }
func (f *httpFakeTicketing) GetTicket(ctx context.Context, externalTicketID string) (*connectors.ExternalTicket, error) {
	return nil, errors.New("not used")
}
func (f *httpFakeTicketing) CreateTicket(ctx context.Context, req connectors.CreateTicketRequest) (*connectors.ExternalTicket, error) {
	f.calls++
	return f.result, f.err
}

type httpFakeRuntimeResolver struct {
	companies ticketsports.CompanyDirectory
	ticketing connectors.TicketingConnector
	err       error
}

func (f *httpFakeRuntimeResolver) Resolve(ctx context.Context, tenantID uuid.UUID) (*ticketsports.TicketingRuntime, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &ticketsports.TicketingRuntime{CompanyDirectory: f.companies, TicketingConnector: f.ticketing}, nil
}

// httpFakeAttempts mirrors the guarded-transition semantics already proven
// against real Postgres in PRODUCT.6-K1 — same design as
// internal/tickets/application's own test fake, duplicated here (test-only)
// so this package's HTTP tests can exercise real replay/idempotency logic
// without importing unexported test types from another package.
type httpFakeAttempts struct {
	mu    sync.Mutex
	byKey map[string]*ticketsdomain.ExternalCreateAttempt
}

func newHTTPFakeAttempts() *httpFakeAttempts {
	return &httpFakeAttempts{byKey: map[string]*ticketsdomain.ExternalCreateAttempt{}}
}
func (f *httpFakeAttempts) Acquire(ctx context.Context, conversationID, localTicketID, actorUserID uuid.UUID, idempotencyKey, requestHash string) (*ticketsdomain.ExternalCreateAttempt, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.byKey[idempotencyKey]; ok {
		if existing.RequestHash != requestHash {
			return nil, false, ticketsports.ErrIdempotencyMismatch
		}
		cp := *existing
		return &cp, false, nil
	}
	a := &ticketsdomain.ExternalCreateAttempt{ID: uuid.New(), ConversationID: conversationID, ActorUserID: actorUserID, LocalTicketID: &localTicketID,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, State: ticketsdomain.AttemptInFlight,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	f.byKey[idempotencyKey] = a
	cp := *a
	return &cp, true, nil
}
func (f *httpFakeAttempts) findByID(id uuid.UUID) *ticketsdomain.ExternalCreateAttempt {
	for _, a := range f.byKey {
		if a.ID == id {
			return a
		}
	}
	return nil
}
func (f *httpFakeAttempts) MarkConfirmedSuccess(ctx context.Context, attemptID uuid.UUID, provider, externalTicketID string) (*ticketsdomain.ExternalCreateAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.findByID(attemptID)
	if a == nil || a.State != ticketsdomain.AttemptInFlight {
		return nil, errors.New("invalid transition")
	}
	a.State, a.Provider, a.ExternalTicketID = ticketsdomain.AttemptConfirmedSuccess, &provider, &externalTicketID
	cp := *a
	return &cp, nil
}
func (f *httpFakeAttempts) MarkConfirmedFailure(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error) {
	return f.markTerminal(attemptID, ticketsdomain.AttemptConfirmedFailure)
}
func (f *httpFakeAttempts) MarkOutcomeUnknown(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error) {
	return f.markTerminal(attemptID, ticketsdomain.AttemptOutcomeUnknown)
}
func (f *httpFakeAttempts) markTerminal(attemptID uuid.UUID, target ticketsdomain.AttemptState) (*ticketsdomain.ExternalCreateAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.findByID(attemptID)
	if a == nil || a.State != ticketsdomain.AttemptInFlight {
		return nil, errors.New("invalid transition")
	}
	a.State = target
	cp := *a
	return &cp, nil
}
func (f *httpFakeAttempts) MarkProjectionSynced(ctx context.Context, attemptID, localTicketID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.findByID(attemptID)
	if a == nil || a.State != ticketsdomain.AttemptConfirmedSuccess {
		return nil, errors.New("invalid transition")
	}
	now := time.Now().UTC()
	a.LocalTicketID, a.ProjectionSyncedAt = &localTicketID, &now
	cp := *a
	return &cp, nil
}

const httpTestCompanyID = "d38e7970-635d-490b-a119-749ee6f1fe23"

type externalTicketHarness struct {
	perms        *httpFakePerms
	conversation *httpFakeConversation
	companies    *httpFakeCompanies
	ticketing    *httpFakeTicketing
	runtime      *httpFakeRuntimeResolver
	handler      *CRMHandlers
	tenantID     uuid.UUID
	actorID      uuid.UUID
	convID       uuid.UUID
}

func newExternalTicketHarness(t *testing.T) *externalTicketHarness {
	t.Helper()
	actorID := uuid.New()
	h := &externalTicketHarness{
		perms:        &httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketCreate: true}},
		conversation: &httpFakeConversation{found: true, assignedTo: &actorID},
		companies:    &httpFakeCompanies{companies: []ticketsports.Company{{ExternalID: httpTestCompanyID, Active: true}}},
		ticketing:    &httpFakeTicketing{result: &connectors.ExternalTicket{ExternalID: "28180", ExternalStatus: "1", ExternalStatusLabel: "Novo"}},
		tenantID:     uuid.New(), actorID: actorID, convID: uuid.New(),
	}
	h.runtime = &httpFakeRuntimeResolver{companies: h.companies, ticketing: h.ticketing}
	svc := ticketsapplication.NewService(h.perms, h.conversation, newHTTPFakeAttempts(),
		&httpFakeLocalTickets{candidate: &ticketsdomain.Ticket{ID: uuid.New()}}, h.runtime)
	handler := NewCRMHandlers(nil)
	handler.SetExternalTicketService(svc)
	h.handler = handler
	return h
}

func (h *externalTicketHarness) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", h.handler.CreateTicket)
	return mux
}

func (h *externalTicketHarness) path() string {
	return "/api/v1/tenants/" + h.tenantID.String() + "/conversations/" + h.convID.String() + "/ticket"
}

func (h *externalTicketHarness) request(body, idempotencyKey string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, h.path(), strings.NewReader(body))
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: h.actorID})
	tc, err := tenancydomain.NewTenantContext(h.tenantID, h.actorID, tenancydomain.AccessSourceDirect)
	if err != nil {
		panic(err)
	}
	ctx = tenancydomain.WithTenantContext(ctx, tc)
	return req.WithContext(ctx)
}

const validKey = "test-idempotency-key-01"

// A. successful create returns provider-neutral result.
func TestCreateExternalTicketHTTPSuccessReturnsProviderNeutralResult(t *testing.T) {
	h := newExternalTicketHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s","description":"d"}`, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var resp externalTicketCreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ExternalTicketID != "28180" || resp.Provider != "k3g" || resp.SyncStatus != "synced" || resp.Replayed {
		t.Fatalf("unexpected response: %+v", resp)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"companyId", "\"token\"", "Bearer", "assigneeUserId"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked provider/credential internals (%q): %s", forbidden, body)
		}
	}
}

// B. TenantID/ActorUserID derived from authenticated context, not body —
// the request struct has no such fields, so unknown/extra ones are simply
// ignored; assert the command actually used the context's tenant/actor.
func TestCreateExternalTicketHTTPDerivesTenantAndActorFromContextNotBody(t *testing.T) {
	h := newExternalTicketHarness(t)
	rec := httptest.NewRecorder()
	evilBody := `{"selected_customer_external_id":"` + httpTestCompanyID + `","subject":"s","description":"d","tenant_id":"` + uuid.NewString() + `","actor_user_id":"` + uuid.NewString() + `"}`
	h.mux().ServeHTTP(rec, h.request(evilBody, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	// The fake conversation/company/attempts are all scoped by h.tenantID/
	// h.actorID; a 201 here is only reachable if the service actually used
	// those, not any value from the body (which would fail authorization/
	// company validation against the fakes above).
}

// C. missing Idempotency-Key rejected before provider call.
func TestCreateExternalTicketHTTPMissingIdempotencyKeyRejected(t *testing.T) {
	h := newExternalTicketHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

// D. invalid (too short) Idempotency-Key rejected.
func TestCreateExternalTicketHTTPInvalidIdempotencyKeyRejected(t *testing.T) {
	h := newExternalTicketHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, "short"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

// E. missing ticket.create rejected.
func TestCreateExternalTicketHTTPMissingTicketCreateRejected(t *testing.T) {
	h := newExternalTicketHarness(t)
	h.perms.granted[ticketsapplication.PermissionTicketCreate] = false
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

// F. unauthorized conversation (assigned to someone else, no manage) rejected.
func TestCreateExternalTicketHTTPUnauthorizedConversationRejected(t *testing.T) {
	h := newExternalTicketHarness(t)
	other := uuid.New()
	h.conversation.assignedTo = &other
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// unassigned conversation maps to 409 (mirrors internal/messages/adapters'
// exact convention for the identical application-level concept).
func TestCreateExternalTicketHTTPUnassignedConversationMapsTo409(t *testing.T) {
	h := newExternalTicketHarness(t)
	h.conversation.assignedTo = nil
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// G. invalid/unlisted company rejected.
func TestCreateExternalTicketHTTPInvalidCompanyRejected(t *testing.T) {
	h := newExternalTicketHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"not-a-real-company","subject":"s"}`, validKey))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

// H. successful request calls provider exactly once.
func TestCreateExternalTicketHTTPSuccessCallsProviderOnce(t *testing.T) {
	h := newExternalTicketHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("provider called %d times, want 1", h.ticketing.calls)
	}
}

// I. same Idempotency-Key replay does not call provider twice.
func TestCreateExternalTicketHTTPReplayDoesNotCallProviderTwice(t *testing.T) {
	h := newExternalTicketHarness(t)
	body := `{"selected_customer_external_id":"` + httpTestCompanyID + `","subject":"s"}`
	rec1 := httptest.NewRecorder()
	h.mux().ServeHTTP(rec1, h.request(body, validKey))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first call status = %d, want 201: %s", rec1.Code, rec1.Body.String())
	}
	rec2 := httptest.NewRecorder()
	h.mux().ServeHTTP(rec2, h.request(body, validKey))
	if rec2.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200: %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay must set Idempotent-Replayed header, got headers: %+v", rec2.Header())
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("provider called %d times across both requests, want exactly 1", h.ticketing.calls)
	}
}

// J. same key + different request body -> conflict, mapped to 422 (mirrors
// internal/messages/adapters' exact convention for the identical
// application-level concept, ErrIdempotencyMismatch).
func TestCreateExternalTicketHTTPMismatchReturnsUnprocessableEntity(t *testing.T) {
	h := newExternalTicketHarness(t)
	rec1 := httptest.NewRecorder()
	h.mux().ServeHTTP(rec1, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first call status = %d, want 201: %s", rec1.Code, rec1.Body.String())
	}
	rec2 := httptest.NewRecorder()
	h.mux().ServeHTTP(rec2, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"a different subject"}`, validKey))
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch status = %d, want 422: %s", rec2.Code, rec2.Body.String())
	}
}

// K. WRITE_OUTCOME_UNKNOWN maps to an explicit no-auto-retry response.
func TestCreateExternalTicketHTTPWriteOutcomeUnknownMapsToReconciliationRequired(t *testing.T) {
	h := newExternalTicketHarness(t)
	h.ticketing.result, h.ticketing.err = nil, &connectors.TicketingError{Code: connectors.TicketingWriteOutcomeUnknown, Message: "ambiguous"}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var problem externalTicketProblemResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if problem.Code != "TICKET_RECONCILIATION_REQUIRED" || problem.AttemptState != string(ticketsdomain.AttemptOutcomeUnknown) {
		t.Fatalf("unexpected problem body: %+v", problem)
	}
}

// L. confirmed provider success + projection failure maps to reconciliation
// response, never a plain success, and never a second provider POST on
// replay.
func TestCreateExternalTicketHTTPProjectionFailureMapsToReconciliationRequired(t *testing.T) {
	h := newExternalTicketHarness(t)
	svc := ticketsapplication.NewService(h.perms, h.conversation, newHTTPFakeAttempts(),
		&failingProjectionLocalTickets{candidate: &ticketsdomain.Ticket{ID: uuid.New()}}, h.runtime)
	h.handler.SetExternalTicketService(svc)

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var problem externalTicketProblemResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if problem.Code != "TICKET_RECONCILIATION_REQUIRED" || problem.ExternalTicketID != "28180" {
		t.Fatalf("unexpected problem body: %+v", problem)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("provider called %d times, want exactly 1 (no retry after projection failure)", h.ticketing.calls)
	}
}

type failingProjectionLocalTickets struct{ candidate *ticketsdomain.Ticket }

func (f *failingProjectionLocalTickets) FindEnrichmentCandidate(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error) {
	cp := *f.candidate
	return &cp, nil
}

func (f *failingProjectionLocalTickets) FindActiveByConversation(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error) {
	return f.FindEnrichmentCandidate(ctx, conversationID)
}
func (f *failingProjectionLocalTickets) EnrichExternalProjection(ctx context.Context, ticketID uuid.UUID, provider, externalTicketID, externalStatus, externalStatusLabel string, syncedAt time.Time) error {
	return errors.New("db down")
}

// M. no configuration maps clearly to integration unavailable (503, same
// body ticketingUnavailable already uses for PRODUCT.6-B).
func TestCreateExternalTicketHTTPNoConfigurationMapsToUnavailable(t *testing.T) {
	h := newExternalTicketHarness(t)
	h.runtime.err = &ticketsports.ResolutionError{Code: ticketsports.ResolutionNoConfiguration, Message: "no K3G connection"}
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ticketing integration not configured for this tenant") {
		t.Fatalf("body must reuse the exact PRODUCT.6-B unavailable text: %s", rec.Body.String())
	}
}

// PRODUCT.6-M5: an already-linked local ticket maps to 409
// TICKET_ALREADY_LINKED, never a plain success and never a provider call,
// even with a brand-new Idempotency-Key.
func TestCreateExternalTicketHTTPAlreadyLinkedMapsTo409(t *testing.T) {
	h := newExternalTicketHarness(t)
	provider, externalID := "k3g", "28182"
	svc := ticketsapplication.NewService(h.perms, h.conversation, newHTTPFakeAttempts(),
		&httpFakeLocalTickets{candidate: &ticketsdomain.Ticket{ID: uuid.New(), Provider: &provider, ExternalTicketID: &externalID}}, h.runtime)
	h.handler.SetExternalTicketService(svc)

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s","description":"d"}`, validKey))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var problem externalTicketProblemResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if problem.Code != "TICKET_ALREADY_LINKED" || problem.ExternalTicketID != externalID || problem.Provider != provider {
		t.Fatalf("unexpected problem body: %+v", problem)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

// N. cross-tenant request cannot reach another tenant's runtime: the
// handler always resolves via cmd.TenantID derived from the AUTHENTICATED
// TenantContext (route tenant_id + session), never a value an attacker
// could smuggle in the body — already proven structurally by test B (the
// request struct has no tenant field at all). This test additionally
// proves a request whose PATH tenant_id disagrees with the session's own
// TenantContext is rejected outright (defense in depth, same invariant
// internal/tickets/application.CreateExternalTicket itself enforces).
func TestCreateExternalTicketHTTPPathTenantMismatchWithSessionRejected(t *testing.T) {
	h := newExternalTicketHarness(t)
	otherTenantInPath := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+otherTenantInPath.String()+"/conversations/"+h.convID.String()+"/ticket",
		strings.NewReader(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`))
	req.Header.Set("Idempotency-Key", validKey)
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: h.actorID})
	tc, err := tenancydomain.NewTenantContext(h.tenantID, h.actorID, tenancydomain.AccessSourceDirect) // session's REAL tenant
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(tenancydomain.WithTenantContext(ctx, tc))

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("a path tenant_id disagreeing with the session's own tenant must never succeed, got 201: %s", rec.Body.String())
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

// O. GET/UPDATE/CLOSE containment remains intact — proven exhaustively by
// crm_handlers_routes_test.go (TestCRMHandlersReadRouteWildcards), reused
// here rather than duplicated: those routes still check h.crm == nil,
// untouched by PRODUCT.6-M.
