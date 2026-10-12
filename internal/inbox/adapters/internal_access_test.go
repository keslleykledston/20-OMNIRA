package adapters_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// internalConversation seeds a contact declared internal (with its subtype), an open conversation and one inbound message.
func (e *kindEnv) internalConversation(tenant uuid.UUID, name, phone string) (contact, conversation, message uuid.UUID) {
	contact, conversation, message = uuid.New(), uuid.New(), uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,$3,$4)`, contact, tenant, name, phone)
	e.exec(`UPDATE contacts SET kind='internal', internal_role='partner' WHERE id=$1`, contact)
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conversation, tenant, contact)
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'inbound','text','combinado de parceria','received')`, message, tenant, conversation)
	return contact, conversation, message
}

// ADR-0018 addendum: the conversations of contacts declared internal are for people who hold conversation.read_internal
// (administrators, supervisors). An agent gets the same answer as for a conversation that does not exist, on every route that
// names the conversation, one of its messages, or the conversation list of the contact; the contact itself stays reachable so the
// classification can be reverted. The route middleware is the single place that decides.
func TestAgentsCannotReachConversationsOfInternalContacts(t *testing.T) {
	e := newKindEnv(t)
	tenant := e.tenant()
	agent := e.member(tenant, "tenant_agent")
	supervisor := e.member(tenant, "tenant_supervisor")
	admin := e.member(tenant, "tenant_admin")
	partnerContact, partner, partnerMsg := e.internalConversation(tenant, "Parceiro Y", "+5592955550001")
	customer := e.conversation(tenant, "Cliente Z", "other", "+5592955550002")
	var customerContact uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT contact_id FROM conversations WHERE id=$1`, customer).Scan(&customerContact); err != nil {
		t.Fatal(err)
	}
	// another company's internal conversation must not matter to this one's answers
	otherTenant := e.tenant()
	e.internalConversation(otherTenant, "Parceiro de B", "+5592955550009")

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	h := inboxadapters.NewInboxAPIHandler(e.app)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := tenancyadapters.AuthorizationMiddleware(e.app, authz)
	mux := http.NewServeMux()
	base := "/api/v1/tenants/{tenant_id}"
	mux.Handle("GET "+base+"/inbox/conversations", mw(http.HandlerFunc(h.ListConversations)))
	mux.Handle("GET "+base+"/inbox/conversations/{conversation_id}", mw(http.HandlerFunc(h.GetConversation)))
	mux.Handle("GET "+base+"/inbox/conversations/{conversation_id}/messages", mw(http.HandlerFunc(h.ListMessages)))
	mux.Handle("POST "+base+"/inbox/conversations/{conversation_id}/messages", mw(ok))
	mux.Handle("POST "+base+"/conversations/{conversation_id}/ticket", mw(ok))
	mux.Handle("GET "+base+"/messages/{message_id}/media", mw(ok))
	mux.Handle("GET "+base+"/contacts/{contact_id}", mw(ok))
	mux.Handle("PUT "+base+"/contacts/{contact_id}/classification", mw(ok))
	mux.Handle("GET "+base+"/contacts/{contact_id}/conversations", mw(ok))
	mux.Handle("GET "+base+"/contacts/{contact_id}/tickets", mw(ok))

	call := func(user uuid.UUID, method, path string) (int, []byte) {
		req := httptest.NewRequest(method, "/api/v1/tenants/"+tenant.String()+path, nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	ids := func(user uuid.UUID, query string) map[string]bool {
		code, body := call(user, http.MethodGet, "/inbox/conversations"+query)
		if code != http.StatusOK {
			t.Fatalf("list %s as %s = %d %s", query, user, code, body)
		}
		var page struct{ Items []struct{ ID string } }
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, it := range page.Items {
			out[it.ID] = true
		}
		return out
	}

	// Administrators and supervisors see and open it.
	for name, user := range map[string]uuid.UUID{"admin": admin, "supervisor": supervisor} {
		if l := ids(user, ""); !l[partner.String()] || !l[customer.String()] {
			t.Errorf("%s: the default list must hold both: %v", name, l)
		}
		if l := ids(user, "?conversation_kind=internal"); !l[partner.String()] {
			t.Errorf("%s: the Internas view must hold the partner: %v", name, l)
		}
		for _, path := range []string{"/inbox/conversations/" + partner.String(), "/inbox/conversations/" + partner.String() + "/messages", "/messages/" + partnerMsg.String() + "/media", "/contacts/" + partnerContact.String() + "/conversations"} {
			if code, body := call(user, http.MethodGet, path); code != http.StatusOK {
				t.Errorf("%s GET %s = %d %s", name, path, code, body)
			}
		}
	}

	// An agent: not in any list, 404 everywhere the conversation is named.
	if l := ids(agent, ""); l[partner.String()] || !l[customer.String()] {
		t.Errorf("agent default list = %v (must hide the partner and keep the customer)", l)
	}
	if l := ids(agent, "?conversation_kind=internal"); len(l) != 0 {
		t.Errorf("agent Internas view = %v, must be empty", l)
	}
	if l := ids(agent, "?kind=internal"); len(l) != 0 {
		t.Errorf("agent kind=internal = %v, must be empty", l)
	}
	if l := ids(agent, "?status=all&q=Parceiro"); len(l) != 0 {
		t.Errorf("agent search finds the partner: %v", l)
	}
	refused := []struct{ method, path string }{
		{http.MethodGet, "/inbox/conversations/" + partner.String()},
		{http.MethodGet, "/inbox/conversations/" + partner.String() + "/messages"},
		{http.MethodPost, "/inbox/conversations/" + partner.String() + "/messages"},
		{http.MethodPost, "/conversations/" + partner.String() + "/ticket"},
		{http.MethodGet, "/messages/" + partnerMsg.String() + "/media"},
		{http.MethodGet, "/contacts/" + partnerContact.String() + "/conversations"},
		{http.MethodGet, "/contacts/" + partnerContact.String() + "/tickets"},
	}
	for _, c := range refused {
		if code, body := call(agent, c.method, c.path); code != http.StatusNotFound {
			t.Errorf("agent %s %s = %d %s, want 404 (same as a missing conversation)", c.method, c.path, code, body)
		}
	}
	// ...the same answer as for a conversation that does not exist, so its existence is not revealed.
	_, missing := call(agent, http.MethodGet, "/inbox/conversations/"+uuid.NewString())
	_, hidden := call(agent, http.MethodGet, "/inbox/conversations/"+partner.String())
	if string(missing) != string(hidden) {
		t.Errorf("hidden = %q, missing = %q: the bodies must be identical", hidden, missing)
	}
	// The contact itself and its classification stay reachable (to see who it is and to revert), and customers are untouched.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/contacts/" + partnerContact.String()},
		{http.MethodPut, "/contacts/" + partnerContact.String() + "/classification"},
		{http.MethodGet, "/inbox/conversations/" + customer.String()},
		{http.MethodGet, "/contacts/" + customerContact.String() + "/conversations"},
	} {
		if code, body := call(agent, c.method, c.path); code != http.StatusOK {
			t.Errorf("agent %s %s = %d %s, want 200", c.method, c.path, code, body)
		}
	}

	// Reverting the contact gives the agent the conversation back (nothing was moved or deleted).
	e.exec(`UPDATE contacts SET kind='other', internal_role=NULL WHERE id=$1`, partnerContact)
	if code, body := call(agent, http.MethodGet, "/inbox/conversations/"+partner.String()); code != http.StatusOK {
		t.Errorf("after reverting to other, agent GET = %d %s", code, body)
	}
}

// A contact declared internal never gets an automatic queue (like spam): it waits for someone who may read it, who claims it by hand.
func TestRouteNewDoesNotQueueInternalContacts(t *testing.T) {
	e := newKindEnv(t)
	tenant := e.tenant()
	user := e.member(tenant, "tenant_agent")
	queue := uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Default','round_robin',true)`, queue, tenant)
	_, internal, _ := e.internalConversation(tenant, "Fornecedor", "+5592955550003")
	normal := e.conversation(tenant, "Normal", "other", "+5592955550004")
	store := inboxadapters.NewPostgresInboundStore(e.app)
	for _, conv := range []uuid.UUID{internal, normal} {
		err := platformdb.WithTenantSession(e.ctx, e.app, user, false, func(ctx context.Context) error {
			tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
			if err != nil {
				return err
			}
			return store.RouteNew(tenancydomain.WithTenantContext(ctx, tc), conv)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	queueOf := func(conv uuid.UUID) *uuid.UUID {
		var q *uuid.UUID
		if err := e.seed.QueryRow(e.ctx, `SELECT queue_id FROM conversations WHERE id=$1`, conv).Scan(&q); err != nil {
			t.Fatal(err)
		}
		return q
	}
	if q := queueOf(internal); q != nil {
		t.Errorf("an internal contact's conversation was queued (%s)", *q)
	}
	if q := queueOf(normal); q == nil || *q != queue {
		t.Errorf("a normal conversation must still be routed, got %v", q)
	}
	var jobs int
	if err := e.seed.QueryRow(e.ctx, `SELECT count(*) FROM outbox_events WHERE event_type='job.routing.assign.v1' AND aggregate_id=$1`, internal.String()).Scan(&jobs); err != nil || jobs != 0 {
		t.Errorf("assignment jobs for the internal conversation = %d (%v), want 0", jobs, err)
	}
}
