package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
)

// PRODUCT.7B1B (security correction): CreateActivity is TEMPORARILY
// CONTAINED. A security review found CompanyDirectory membership
// insufficient authorization for an external write: it proves a company
// exists and is active for the tenant, never that it is the company
// associated with THIS conversation's CRM contact — and no authoritative
// Contact/Conversation→Company relationship, nor K3G read contract able to
// prove one, exists anywhere today. Every test here proves the route
// authenticates, authorizes, and checks the conversation's CRM contact —
// then ALWAYS fails closed before any provider interaction, regardless of
// what the browser sends. There is no writer/resolver dependency left in
// this handler at all: nothing exists that could make a provider call.

type fakeActivityConversations struct {
	byID map[uuid.UUID]*activityConversation
}

func (f *fakeActivityConversations) LoadForActivity(ctx context.Context, conversationID uuid.UUID) (*activityConversation, bool, error) {
	c, ok := f.byID[conversationID]
	if !ok {
		return nil, false, nil
	}
	cp := *c
	return &cp, true, nil
}

type fakeActivityPermissions struct{ granted map[string]bool }

func (f *fakeActivityPermissions) HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error) {
	return f.granted[permission], nil
}

type activityHarness struct {
	tenantID, convID, actorID uuid.UUID
	conversations             *fakeActivityConversations
	permissions               *fakeActivityPermissions
	handler                   *CRMHandlers
}

func newActivityHarness(t *testing.T) *activityHarness {
	t.Helper()
	tenantID, convID, actorID := uuid.New(), uuid.New(), uuid.New()
	crmContact := uuid.New()
	h := &activityHarness{tenantID: tenantID, convID: convID, actorID: actorID}
	h.conversations = &fakeActivityConversations{byID: map[uuid.UUID]*activityConversation{
		convID: {AssignedToUserID: &actorID, CRMContactID: &crmContact},
	}}
	h.permissions = &fakeActivityPermissions{granted: map[string]bool{}}
	handler := NewCRMHandlers(nil)
	handler.SetActivityConversationReader(h.conversations)
	handler.SetActivityPermissionChecker(h.permissions)
	h.handler = handler
	return h
}

func (h *activityHarness) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity", h.handler.CreateActivity)
	return mux
}

func (h *activityHarness) path() string {
	return "/api/v1/tenants/" + h.tenantID.String() + "/conversations/" + h.convID.String() + "/crm/activity"
}

func (h *activityHarness) request(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, h.path(), strings.NewReader(body))
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: h.actorID})
	tc, err := tenancydomain.NewTenantContext(h.tenantID, h.actorID, tenancydomain.AccessSourceDirect)
	if err != nil {
		panic(err)
	}
	ctx = tenancydomain.WithTenantContext(ctx, tc)
	return req.WithContext(ctx)
}

const containedBody = "CRM company context for this conversation is not yet authoritative"

// G. an otherwise fully valid, authorized request — assigned actor,
// subject present, conversation has a linked CRM contact — still fails
// closed: there is no authoritative company linkage to approve a
// provider write against.
func TestCreateActivityHTTPContainedEvenWhenFullyAuthorized(t *testing.T) {
	h := newActivityHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"subject":"Ligação de retorno"}`))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), containedBody) {
		t.Fatalf("got %d %q, want 409 containing %q", rec.Code, rec.Body.String(), containedBody)
	}
}

// A/B/C/E. browser-supplied contact_id/company_id — real, foreign,
// fabricated, anything — can never influence the outcome: neither field
// exists in the request contract anymore, so both are always ignored, and
// the result is the identical containment response regardless of what a
// (possibly malicious) client sends, including a real-looking foreign
// tenant's company id.
func TestCreateActivityHTTPBrowserIdentityFieldsNeverInfluenceOutcome(t *testing.T) {
	h := newActivityHarness(t)
	cases := []string{
		`{"subject":"s"}`,
		`{"subject":"s","company_id":"a-real-active-company-in-this-tenant"}`,
		`{"subject":"s","company_id":"some-other-tenants-company"}`,
		`{"subject":"s","contact_id":"` + uuid.NewString() + `","company_id":"whatever"}`,
	}
	for _, body := range cases {
		rec := httptest.NewRecorder()
		h.mux().ServeHTTP(rec, h.request(body))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), containedBody) {
			t.Fatalf("body %q: got %d %q, want 409 containing %q", body, rec.Code, rec.Body.String(), containedBody)
		}
	}
}

// D/J. no success shape is ever produced. There is no writer/resolver
// wired into this handler at all anymore — structurally nothing exists
// that could perform a provider call, live or otherwise.
func TestCreateActivityHTTPNeverReturnsSuccess(t *testing.T) {
	h := newActivityHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"subject":"s"}`))
	if rec.Code == http.StatusCreated {
		t.Fatalf("must never report success while contained, got 201: %s", rec.Body.String())
	}
}

// F. missing crm_contact_id fails closed with its own specific reason,
// checked (and returned) BEFORE the company containment message.
func TestCreateActivityHTTPMissingCRMContactIDFailsClosed(t *testing.T) {
	h := newActivityHarness(t)
	h.conversations.byID[h.convID].CRMContactID = nil
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"subject":"s"}`))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no linked CRM contact") {
		t.Fatalf("got %d %q, want 409 containing %q", rec.Code, rec.Body.String(), "no linked CRM contact")
	}
}

// H. authorization is still checked, and still runs BEFORE containment —
// an unauthorized actor gets its own 409/403, never the containment
// message (which would otherwise confirm a linkable conversation exists).
func TestCreateActivityHTTPUnassignedConversationRejectedBeforeContainment(t *testing.T) {
	h := newActivityHarness(t)
	h.conversations.byID[h.convID].AssignedToUserID = nil
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"subject":"s"}`))
	if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), containedBody) {
		t.Fatalf("got %d %q, want 409 for unassigned (not the containment message)", rec.Code, rec.Body.String())
	}
}

func TestCreateActivityHTTPOtherAssigneeWithoutManageForbidden(t *testing.T) {
	h := newActivityHarness(t)
	otherActor := uuid.New()
	h.conversations.byID[h.convID].AssignedToUserID = &otherActor
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"subject":"s"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// conversation.manage grants authorization for someone other than the
// assignee — but even then, the request still ends in containment, never
// success.
func TestCreateActivityHTTPConversationManageAllowsNonAssigneeButStillContained(t *testing.T) {
	h := newActivityHarness(t)
	otherActor := uuid.New()
	h.conversations.byID[h.convID].AssignedToUserID = &otherActor
	h.permissions.granted[ticketsapplication.PermissionConversationManage] = true
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"subject":"s"}`))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), containedBody) {
		t.Fatalf("got %d %q, want 409 containment even for a manager", rec.Code, rec.Body.String())
	}
}

func TestCreateActivityHTTPUnknownConversationRejected(t *testing.T) {
	h := newActivityHarness(t)
	otherConv := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+h.tenantID.String()+"/conversations/"+otherConv.String()+"/crm/activity",
		strings.NewReader(`{"subject":"s"}`))
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: h.actorID})
	tc, err := tenancydomain.NewTenantContext(h.tenantID, h.actorID, tenancydomain.AccessSourceDirect)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(tenancydomain.WithTenantContext(ctx, tc))
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateActivityHTTPMissingSubjectRejected(t *testing.T) {
	h := newActivityHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// Dependencies not wired (misconfigured deployment) fails closed.
func TestCreateActivityHTTPDependenciesNotWiredFailsClosed(t *testing.T) {
	handler := NewCRMHandlers(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity", handler.CreateActivity)
	tenantID, convID, actorID := uuid.New(), uuid.New(), uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenantID.String()+"/conversations/"+convID.String()+"/crm/activity", strings.NewReader(`{}`))
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: actorID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// I/J. unauthenticated request is denied before any DB/dependency call —
// and, as with every case above, no live provider call happens anywhere
// in this test file: no writer, no resolver, nothing capable of one.
func TestCreateActivityHTTPUnauthenticatedDenied(t *testing.T) {
	h := newActivityHarness(t)
	req := httptest.NewRequest(http.MethodPost, h.path(), strings.NewReader(`{"subject":"s"}`))
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}
