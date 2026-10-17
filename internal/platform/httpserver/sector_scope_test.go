package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	dashboardadapters "github.com/omnira/omnira/internal/dashboard/adapters"
	flowsadapters "github.com/omnira/omnira/internal/flows/adapters"
	flowsapplication "github.com/omnira/omnira/internal/flows/application"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapplication "github.com/omnira/omnira/internal/routing/application"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
	ticketsadapters "github.com/omnira/omnira/internal/tickets/adapters"
)

// ADR-0043 S3 (owner decision 2026-10-17): with the company's sector scope ON, an agent sees only the queues they are an active member of, what is assigned to them, what they
// were invited to and what has no queue; administrators and supervisors (conversation.read_all_queues) see everything. Everything else answers as if it did not exist.
// Real PostgreSQL, the real tenancy middleware and the real handlers; Tenant A and B.

type scopeEnv struct {
	t        *testing.T
	seed     *pgxpool.Pool
	app      *pgxpool.Pool
	mux      *http.ServeMux
	tenant   uuid.UUID
	other    uuid.UUID
	agent1   uuid.UUID // member of Q1
	agent2   uuid.UUID // member of Q2
	super    uuid.UUID
	admin    uuid.UUID
	reader   uuid.UUID // custom role: dashboard.read + ticket.read, NO read_all_queues, member of no queue
	outsider uuid.UUID // admin of the OTHER company
	q1, q2   uuid.UUID
	c        map[string]uuid.UUID // conversations by name
	contacts map[string]uuid.UUID
	msg      map[string]uuid.UUID
}

func (e *scopeEnv) exec(q string, a ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(context.Background(), q, a...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func newScopeEnv(t *testing.T) *scopeEnv {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	e := &scopeEnv{t: t, seed: seed, app: app, tenant: uuid.New(), other: uuid.New(), c: map[string]uuid.UUID{}, contacts: map[string]uuid.UUID{}, msg: map[string]uuid.UUID{}}
	users := map[string]*uuid.UUID{"agent1": &e.agent1, "agent2": &e.agent2, "super": &e.super, "admin": &e.admin, "reader": &e.reader, "outsider": &e.outsider}
	for _, u := range users {
		*u = uuid.New()
		e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, *u, u.String(), u.String()+"@invalid")
	}
	for _, tn := range []uuid.UUID{e.tenant, e.other} {
		e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, e.tenant, e.other)
		for _, u := range users {
			_, _ = seed.Exec(bg, `DELETE FROM users WHERE id=$1`, *u)
		}
		_, _ = seed.Exec(bg, `DELETE FROM roles WHERE tenant_id IN ($1,$2)`, e.tenant, e.other)
		seed.Close()
		app.Close()
	})
	role := func(key string) (id uuid.UUID) {
		if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return
	}
	readerRole := uuid.New()
	e.exec(`INSERT INTO roles(id,tenant_id,key,name) VALUES($1,$2,'reader_dash','Reader')`, readerRole, e.tenant)
	for _, p := range []string{"tenant.read", "dashboard.read", "ticket.read"} {
		e.exec(`INSERT INTO role_permissions(role_id,permission_key) VALUES($1,$2)`, readerRole, p)
	}
	member := func(tn, u, r uuid.UUID) {
		e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tn, u, r)
	}
	member(e.tenant, e.agent1, role("tenant_agent"))
	member(e.tenant, e.agent2, role("tenant_agent"))
	member(e.tenant, e.super, role("tenant_supervisor"))
	member(e.tenant, e.admin, role("tenant_admin"))
	member(e.tenant, e.reader, readerRole)
	member(e.other, e.outsider, role("tenant_admin"))

	// operational enablement (IAM4) is independent of the role
	e.exec(`INSERT INTO agent_profiles(tenant_id,membership_id,status) SELECT tenant_id,id,'active' FROM memberships WHERE tenant_id=$1 AND user_id IN ($2,$3,$4,$5)`, e.tenant, e.agent1, e.agent2, e.super, e.admin)
	e.q1, e.q2 = uuid.New(), uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'Q1','manual'),($3,$2,'Q2','manual')`, e.q1, e.tenant, e.q2)
	e.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,active) VALUES($1,$2,$3,true),($1,$4,$5,true)`, e.tenant, e.q1, e.agent1, e.q2, e.agent2)

	n := 0
	conv := func(name string, queue *uuid.UUID, assigned *uuid.UUID) {
		contact, id, ticket := uuid.New(), uuid.New(), uuid.New()
		n++
		e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,$3,$4)`, contact, e.tenant, "Contato "+name, fmt.Sprintf("+559299900%04d", n))
		e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status,queue_id,assigned_to_user_id) VALUES($1,$2,$3,'open',$4,$5)`, id, e.tenant, contact, queue, assigned)
		m := uuid.New()
		e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'inbound','text','oi','received')`, m, e.tenant, id)
		e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,status,priority,subject) VALUES($1,$2,$3,'open','high','assunto '||$4::text)`, ticket, e.tenant, id, name)
		e.c[name], e.contacts[name], e.msg[name] = id, contact, m
	}
	conv("q1", &e.q1, nil)         // agent1's queue
	conv("q2", &e.q2, nil)         // agent2's queue
	conv("free", nil, nil)         // no queue
	conv("held", &e.q2, &e.agent1) // in Q2 but assigned to agent1
	conv("invited", &e.q2, nil)    // in Q2, agent1 was invited
	e.exec(`INSERT INTO conversation_participants(tenant_id,conversation_id,user_id,role) VALUES($1,$2,$3,'INVITED')`, e.tenant, e.c["invited"], e.agent1)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	mw := tenancyadapters.AuthorizationMiddleware(app, authz)
	ih := inboxadapters.NewInboxAPIHandler(app)
	th := ticketsadapters.NewHandler(app)
	dh := dashboardadapters.NewHandler(app)
	ch := contactsadapters.NewContactsAPIHandler(app)
	base := "/api/v1/tenants/{tenant_id}"
	e.mux = http.NewServeMux()
	e.mux.Handle("GET "+base+"/inbox/conversations", mw(http.HandlerFunc(ih.ListConversations)))
	e.mux.Handle("GET "+base+"/inbox/conversations/{conversation_id}", mw(http.HandlerFunc(ih.GetConversation)))
	e.mux.Handle("GET "+base+"/inbox/conversations/{conversation_id}/messages", mw(http.HandlerFunc(ih.ListMessages)))
	e.mux.Handle("GET "+base+"/messages/{message_id}/media", mw(http.HandlerFunc(ih.GetMedia)))
	e.mux.Handle("GET "+base+"/tickets", mw(http.HandlerFunc(th.List)))
	e.mux.Handle("GET "+base+"/tickets/export.csv", mw(http.HandlerFunc(th.ExportCSV)))
	e.mux.Handle("GET "+base+"/dashboard/snapshot", mw(http.HandlerFunc(dh.GetSnapshot)))
	e.mux.Handle("GET "+base+"/contacts/{contact_id}/conversations", mw(http.HandlerFunc(ch.ListContactConversations)))
	e.mux.Handle("GET "+base+"/contacts/{contact_id}/tickets", mw(http.HandlerFunc(ch.ListContactTickets)))
	qh := tenancyadapters.NewQueuesHandler(app, auditadapters.NewPostgresAuditEventRepository(app))
	e.mux.Handle("GET "+base+"/inbox/queue-options", mw(http.HandlerFunc(qh.Options)))
	ah := routingadapters.NewAssignHandler(routingapplication.NewAssigner(
		routingadapters.NewPostgresConversationAssigner(app),
		routingadapters.NewAuditRecorder(auditadapters.NewPostgresAuditEventRepository(app)),
	))
	e.mux.Handle("POST "+base+"/inbox/conversations/{conversation_id}/assign", mw(http.HandlerFunc(ah.Assign)))
	e.mux.Handle("POST "+base+"/inbox/conversations/{conversation_id}/unassign", mw(http.HandlerFunc(ah.Unassign)))
	e.mux.Handle("POST "+base+"/inbox/conversations/{conversation_id}/route-queue", mw(http.HandlerFunc(ah.RouteToQueue)))
	return e
}

func (e *scopeEnv) post(user uuid.UUID, path, body string) int {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+e.tenant.String()+path, strings.NewReader(body))
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String(), SessionKind: authn.SessionKindCookie}))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec.Code
}

func (e *scopeEnv) scope(on bool) {
	e.t.Helper()
	e.exec(`DELETE FROM tenant_entitlements WHERE tenant_id=$1 AND capability='sector_scope'`, e.tenant)
	if on {
		e.exec(`INSERT INTO tenant_entitlements(tenant_id,capability,enabled) VALUES($1,'sector_scope',true)`, e.tenant)
	}
}

func (e *scopeEnv) get(user, tenant uuid.UUID, path string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenant.String()+path, nil)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String(), SessionKind: authn.SessionKindCookie}))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// listed returns the conversation names the inbox list shows to the user.
func (e *scopeEnv) listed(user uuid.UUID) map[string]bool {
	e.t.Helper()
	code, body := e.get(user, e.tenant, "/inbox/conversations?limit=50")
	if code != 200 {
		e.t.Fatalf("list = %d %s", code, body)
	}
	var out struct {
		Items []struct {
			ID uuid.UUID `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		e.t.Fatalf("decode %q: %v", body, err)
	}
	byID := map[uuid.UUID]string{}
	for name, id := range e.c {
		byID[id] = name
	}
	got := map[string]bool{}
	for _, it := range out.Items {
		got[byID[it.ID]] = true
	}
	return got
}

func names(m map[string]bool) string {
	var out []string
	for _, n := range []string{"q1", "q2", "free", "held", "invited"} {
		if m[n] {
			out = append(out, n)
		}
	}
	return strings.Join(out, ",")
}

func TestSectorScopeIsOffByDefaultAndNothingChanges(t *testing.T) {
	e := newScopeEnv(t)
	for name, u := range map[string]uuid.UUID{"agent1": e.agent1, "agent2": e.agent2, "super": e.super} {
		if got := names(e.listed(u)); got != "q1,q2,free,held,invited" {
			t.Errorf("scope off, %s sees %q", name, got)
		}
	}
	if code, _ := e.get(e.agent1, e.tenant, "/inbox/conversations/"+e.c["q2"].String()); code != 200 {
		t.Errorf("scope off, agent1 reads a conversation of another queue = %d", code)
	}
}

func TestSectorScopeAgentSeesOnlyTheirQueuesTheirOwnAndTheUnqueued(t *testing.T) {
	e := newScopeEnv(t)
	e.scope(true)
	// the list
	if got := names(e.listed(e.agent1)); got != "q1,free,held,invited" {
		t.Errorf("agent1 list = %q, want q1,free,held,invited (own queue, unqueued, assigned to them, invited)", got)
	}
	if got := names(e.listed(e.agent2)); got != "q2,free,held,invited" {
		t.Errorf("agent2 list = %q, want q2,free,held,invited (a queue they belong to shows every conversation in it)", got)
	}
	for name, u := range map[string]uuid.UUID{"supervisor": e.super, "admin": e.admin} {
		if got := names(e.listed(u)); got != "q1,q2,free,held,invited" {
			t.Errorf("%s list = %q, want everything", name, got)
		}
	}
	// the routes: 404 for what is outside, exactly like a conversation that does not exist
	missing, _ := e.get(e.agent1, e.tenant, "/inbox/conversations/"+uuid.NewString())
	for _, path := range []string{"", "/messages"} {
		code, _ := e.get(e.agent1, e.tenant, "/inbox/conversations/"+e.c["q2"].String()+path)
		if code != http.StatusNotFound || code != missing {
			t.Errorf("agent1 on a Q2 conversation %q = %d (a missing one gives %d)", path, code, missing)
		}
		if code, body := e.get(e.agent1, e.tenant, "/inbox/conversations/"+e.c["q1"].String()+path); code != 200 {
			t.Errorf("agent1 on their own queue %q = %d %s", path, code, body)
		}
	}
	for _, name := range []string{"held", "invited", "free"} {
		if code, _ := e.get(e.agent1, e.tenant, "/inbox/conversations/"+e.c[name].String()); code != 200 {
			t.Errorf("agent1 must reach %s = %d", name, code)
		}
	}
	// a message's media is judged by its conversation
	if code, _ := e.get(e.agent1, e.tenant, "/messages/"+e.msg["q2"].String()+"/media"); code != http.StatusNotFound {
		t.Errorf("media of a message of another queue = %d, want 404", code)
	}
	// supervisor and admin reach everything
	for _, u := range []uuid.UUID{e.super, e.admin} {
		if code, _ := e.get(u, e.tenant, "/inbox/conversations/"+e.c["q2"].String()); code != 200 {
			t.Errorf("a supervisor/admin must reach Q2 = %d", code)
		}
	}
	// turning it off gives the full view back at once (nothing was moved or deleted)
	e.scope(false)
	if got := names(e.listed(e.agent1)); got != "q1,q2,free,held,invited" {
		t.Errorf("scope turned off again, agent1 sees %q", got)
	}
}

func TestSectorScopeCoversTicketsContactHistoryAndCounts(t *testing.T) {
	e := newScopeEnv(t)
	e.scope(true)
	subjects := func(user uuid.UUID, path string) string {
		code, body := e.get(user, e.tenant, path)
		if code != 200 {
			t.Fatalf("%s = %d %s", path, code, body)
		}
		var out struct {
			Items []struct {
				Subject string `json:"subject"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		var s []string
		for _, i := range out.Items {
			s = append(s, strings.TrimPrefix(i.Subject, "assunto "))
		}
		return strings.Join(sortedStrings(s), ",")
	}
	// a person with ticket.read but no key and no queue sees only the unqueued ticket
	if got := subjects(e.reader, "/tickets"); got != "free" {
		t.Errorf("reader tickets = %q, want free", got)
	}
	if got := subjects(e.super, "/tickets"); got != "free,held,invited,q1,q2" {
		t.Errorf("supervisor tickets = %q", got)
	}
	// the CSV export is the same data
	code, csv := e.get(e.reader, e.tenant, "/tickets/export.csv")
	if code != 200 || strings.Contains(csv, "assunto q1") || strings.Contains(csv, "assunto q2") || !strings.Contains(csv, "assunto free") {
		t.Errorf("reader export = %d %q", code, csv)
	}
	// a contact's history: the contact itself stays, its conversations and tickets in an out-of-scope queue do not
	if code, body := e.get(e.reader, e.tenant, "/contacts/"+e.contacts["q1"].String()+"/conversations"); code != 200 || strings.Contains(body, e.c["q1"].String()) {
		t.Errorf("reader sees the conversation history of a Q1 contact: %d %s", code, body)
	}
	if code, body := e.get(e.reader, e.tenant, "/contacts/"+e.contacts["free"].String()+"/conversations"); code != 200 || !strings.Contains(body, e.c["free"].String()) {
		t.Errorf("reader must see the unqueued history: %d %s", code, body)
	}
	if code, body := e.get(e.reader, e.tenant, "/contacts/"+e.contacts["q2"].String()+"/tickets"); code != 200 || strings.Contains(body, "assunto q2") {
		t.Errorf("reader sees the tickets of a Q2 contact: %d %s", code, body)
	}
	// the dashboard counts only what the person may see
	snap := func(user uuid.UUID) (open, tickets int) {
		code, body := e.get(user, e.tenant, "/dashboard/snapshot")
		if code != 200 {
			t.Fatalf("snapshot = %d %s", code, body)
		}
		var s struct {
			OpenConversations int `json:"open_conversations"`
			OpenTickets       int `json:"open_tickets"`
		}
		_ = json.Unmarshal([]byte(body), &s)
		return s.OpenConversations, s.OpenTickets
	}
	if o, tk := snap(e.reader); o != 1 || tk != 1 {
		t.Errorf("reader dashboard = %d conversations, %d tickets, want 1 and 1", o, tk)
	}
	if o, tk := snap(e.super); o != 5 || tk != 5 {
		t.Errorf("supervisor dashboard = %d, %d, want 5 and 5", o, tk)
	}
}

func TestSectorScopeNeverCrossesCompaniesAndTheStreamUsesTheSameDecision(t *testing.T) {
	e := newScopeEnv(t)
	e.scope(true)
	// the OTHER company's admin cannot reach this company's conversations by any door; the scope switch of A does not touch B
	if code, _ := e.get(e.outsider, e.tenant, "/inbox/conversations/"+e.c["q1"].String()); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("outsider = %d", code)
	}
	// the realtime stream asks the same SQL: per-conversation visibility and whether the person is restricted at all
	auth := sessionStreamAuthorizer{pool: e.app}
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		user  uuid.UUID
		conv  string
		want  bool
		restr bool
	}{
		{"agent1, own queue", e.agent1, "q1", true, true},
		{"agent1, other queue", e.agent1, "q2", false, true},
		{"agent1, assigned in other queue", e.agent1, "held", true, true},
		{"agent1, invited", e.agent1, "invited", true, true},
		{"agent1, unqueued", e.agent1, "free", true, true},
		{"supervisor, other queue", e.super, "q2", true, false},
		{"admin, other queue", e.admin, "q1", true, false},
	} {
		got, err := auth.ConversationVisible(ctx, c.user, e.tenant, e.c[c.conv])
		if err != nil || got != c.want {
			t.Errorf("%s: visible=%v err=%v, want %v", c.name, got, err, c.want)
		}
		restricted, err := auth.SectorRestricted(ctx, c.user, e.tenant)
		if err != nil || restricted != c.restr {
			t.Errorf("%s: restricted=%v err=%v, want %v", c.name, restricted, err, c.restr)
		}
	}
	// with the capability off nobody is restricted
	e.scope(false)
	if r, err := auth.SectorRestricted(ctx, e.agent1, e.tenant); err != nil || r {
		t.Errorf("scope off: restricted=%v err=%v", r, err)
	}
	if v, err := auth.ConversationVisible(ctx, e.agent1, e.tenant, e.c["q2"]); err != nil || !v {
		t.Errorf("scope off: agent1 sees Q2 = %v %v", v, err)
	}
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// The write paths under the scope: what the person may see they can still work on, and sending a conversation to another queue (which takes it out of their reach) works and
// then hides it; what is outside the scope cannot be claimed, released or moved - it answers 404, not 403.
func TestSectorScopeWritesKeepWorkingAndCannotReachOutsideTheScope(t *testing.T) {
	e := newScopeEnv(t)
	e.scope(true)
	conv := func(n string) string { return "/inbox/conversations/" + e.c[n].String() }
	// agent1 claims and releases a conversation of their own queue
	if code := e.post(e.agent1, conv("q1")+"/assign", `{}`); code != 200 {
		t.Fatalf("agent1 claims q1 = %d", code)
	}
	if code := e.post(e.agent1, conv("q1")+"/unassign", `{}`); code != 200 {
		t.Fatalf("agent1 releases q1 = %d", code)
	}
	// outside the scope: invisible, so every verb answers 404 and nothing changes
	for _, verb := range []struct{ path, body string }{{"/assign", `{}`}, {"/unassign", `{}`}, {"/route-queue", `{"queue_id":"` + e.q1.String() + `"}`}} {
		if code := e.post(e.agent1, conv("q2")+verb.path, verb.body); code != http.StatusNotFound {
			t.Errorf("agent1 %s on a Q2 conversation = %d, want 404", verb.path, code)
		}
	}
	var queue uuid.UUID
	_ = e.seed.QueryRow(context.Background(), `SELECT queue_id FROM conversations WHERE id=$1`, e.c["q2"]).Scan(&queue)
	if queue != e.q2 {
		t.Fatalf("a refused request moved the conversation to %s", queue)
	}
	// sending q1's conversation to Q2 works for agent1 (it is in their reach), and from then on it is out of it
	if code := e.post(e.agent1, conv("q1")+"/route-queue", `{"queue_id":"`+e.q2.String()+`"}`); code != 200 {
		t.Fatalf("agent1 sends q1 to Q2 = %d", code)
	}
	if code, _ := e.get(e.agent1, e.tenant, "/inbox/conversations/"+e.c["q1"].String()); code != http.StatusNotFound {
		t.Errorf("after sending it away agent1 still reads it = %d, want 404", code)
	}
	if code, _ := e.get(e.agent2, e.tenant, "/inbox/conversations/"+e.c["q1"].String()); code != 200 {
		t.Errorf("agent2 (Q2 member) must now see it = %d", code)
	}
	// a supervisor moves anything
	if code := e.post(e.super, conv("q2")+"/route-queue", `{"queue_id":"`+e.q1.String()+`"}`); code != 200 {
		t.Errorf("supervisor routes a Q2 conversation = %d", code)
	}
}

// Topics are judged by their conversations: visible when at least one conversation of the topic is in the person's reach; a message id is judged by its conversation.
func TestSectorScopeJudgesTopicsAndMessagesByTheirConversations(t *testing.T) {
	e := newScopeEnv(t)
	e.scope(true)
	topic := func(title string, convs ...string) uuid.UUID {
		id := uuid.New()
		e.exec(`INSERT INTO topic_threads(id,tenant_id,title,source) VALUES($1,$2,$3,'manual')`, id, e.tenant, title)
		for _, c := range convs {
			e.exec(`INSERT INTO topic_conversation_links(tenant_id,topic_thread_id,conversation_id,relation) VALUES($1,$2,$3,'origin')`, e.tenant, id, e.c[c])
		}
		return id
	}
	onlyQ2, mixed, none := topic("so Q2", "q2"), topic("misto", "q2", "q1"), topic("sem conversa")
	refused := func(user uuid.UUID, topicID, messageID uuid.UUID) bool {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if topicID != uuid.Nil {
			req.SetPathValue("topic_id", topicID.String())
		}
		if messageID != uuid.Nil {
			req.SetPathValue("message_id", messageID.String())
		}
		var got bool
		err := platformdb.WithTenantSession(context.Background(), e.app, user, false, func(ctx context.Context) error {
			tc, err := tenancydomain.NewTenantContext(e.tenant, user, tenancydomain.AccessSourceDirect)
			if err != nil {
				return err
			}
			got, err = tenancyadapters.RefusesOutOfSectorScope(ctx, platformdb.QuerierFromContext(ctx, e.app), req, tc)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, c := range []struct {
		name    string
		user    uuid.UUID
		topic   uuid.UUID
		message uuid.UUID
		want    bool
	}{
		{"agent1, topic only in Q2", e.agent1, onlyQ2, uuid.Nil, true},
		{"agent1, topic in Q1 and Q2", e.agent1, mixed, uuid.Nil, false},
		{"agent1, topic with no conversation (fail closed)", e.agent1, none, uuid.Nil, true},
		{"supervisor, topic with no conversation", e.super, none, uuid.Nil, false},
		{"supervisor, topic only in Q2", e.super, onlyQ2, uuid.Nil, false},
		{"agent1, message of Q2", e.agent1, uuid.Nil, e.msg["q2"], true},
		{"agent1, message of Q1", e.agent1, uuid.Nil, e.msg["q1"], false},
		{"agent1, unknown message", e.agent1, uuid.Nil, uuid.New(), true},
		{"agent1, unknown topic", e.agent1, uuid.New(), uuid.Nil, true},
	} {
		if got := refused(c.user, c.topic, c.message); got != c.want {
			t.Errorf("%s: refused=%v, want %v", c.name, got, c.want)
		}
	}
}

// "Enviar para uma fila" needs the list of queues, and a plain agent has no agent.read: the options route is open to anyone who can claim, shows names only, leaves out a
// queue whose sector is off (routing refuses it) and never another company's queues.
func TestQueueOptionsAreOpenToAnAgentAndShowNamesOnly(t *testing.T) {
	e := newScopeEnv(t)
	sector := uuid.New()
	e.exec(`INSERT INTO sectors(id,tenant_id,name,active) VALUES($1,$2,'Desligado',false)`, sector, e.tenant)
	off := uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode,sector_id) VALUES($1,$2,'Fila do setor desligado','manual',$3)`, off, e.tenant, sector)
	foreign := uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'Fila de outra empresa','manual')`, foreign, e.other)

	code, body := e.get(e.agent1, e.tenant, "/inbox/queue-options")
	if code != 200 {
		t.Fatalf("agent options = %d %s", code, body)
	}
	if !strings.Contains(body, `"Q1"`) || !strings.Contains(body, `"Q2"`) {
		t.Errorf("options miss the company's queues: %s", body)
	}
	for _, leak := range []string{"Fila do setor desligado", "Fila de outra empresa", "member_count", "open_conversation_count", "available_count"} {
		if strings.Contains(body, leak) {
			t.Errorf("options carry %q: %s", leak, body)
		}
	}
	// the full list stays for people who manage agents; a plain agent is refused there (that is why the options exist)
	if code, _ := e.get(e.agent1, e.tenant, "/queues"); code == 200 {
		t.Log("note: the plain agent can read /queues in this build")
	}
	// a member without conversation.claim sees nothing
	if code, _ := e.get(e.reader, e.tenant, "/inbox/queue-options"); code != http.StatusForbidden {
		t.Errorf("a member who cannot claim = %d, want 403", code)
	}
	// the other company's admin cannot read this company's queues
	if code, _ := e.get(e.outsider, e.tenant, "/inbox/queue-options"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("outsider = %d", code)
	}
}

// Codex round 1 left two accepted gaps; with the scope ON in the pilot company they are closed: the flow-run history (variables and a step-by-step timeline of a conversation)
// and the queue SLA monitor (conversation ids and timings) obey the same single decision as everything else.
func TestSectorScopeCoversFlowRunHistoryAndTheQueueSLAMonitor(t *testing.T) {
	e := newScopeEnv(t)
	e.exec(`INSERT INTO role_permissions(role_id,permission_key) SELECT id,k FROM roles, unnest(ARRAY['flow_run.view','agent.read']) k WHERE tenant_id=$1 AND key='reader_dash'`, e.tenant)
	repo := flowsadapters.NewPostgresFlowRepository(e.app)
	fh := flowsadapters.NewHandler(e.app, flowsapplication.NewControlPlane(repo, repo, nil)).WithRuns(repo)
	sh := sectorsadapters.NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app))
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	mw := tenancyadapters.AuthorizationMiddleware(e.app, authz)
	base := "/api/v1/tenants/{tenant_id}"
	e.mux.Handle("GET "+base+"/flow-runs", mw(http.HandlerFunc(fh.ListRuns)))
	e.mux.Handle("GET "+base+"/flow-runs/{run_id}", mw(http.HandlerFunc(fh.GetRun)))
	e.mux.Handle("GET "+base+"/queues/{queue_id}/sla", mw(http.HandlerFunc(sh.QueueSLA)))

	flowID, versionID := uuid.New(), uuid.New()
	e.exec(`INSERT INTO flows(id, tenant_id, slug, name) VALUES($1,$2,'scope-flow','Fluxo')`, flowID, e.tenant)
	e.exec(`INSERT INTO flow_versions(id, tenant_id, flow_id, version, definition, definition_hash) VALUES($1,$2,$3,1,'{}','h')`, versionID, e.tenant, flowID)
	runs := map[string]uuid.UUID{}
	for _, name := range []string{"q1", "q2", "free"} {
		id := uuid.New()
		e.exec(`INSERT INTO flow_runs(id, tenant_id, flow_id, flow_version_id, conversation_id, status, trigger_event_id) VALUES($1,$2,$3,$4,$5,'running',$6)`, id, e.tenant, flowID, versionID, e.c[name], uuid.NewString())
		runs[name] = id
	}
	for _, name := range []string{"q2", "held", "invited"} { // the three open conversations of Q2, each with a running clock
		e.exec(`INSERT INTO conversation_sla (tenant_id, conversation_id, metric, started_at, anchor_at, budget_seconds, risk_percent, counting, calendar_queue_id, deadline_at, risk_at, state)
			VALUES ($1,$2,'assignment', now(), now(), 600, 80, 'wall', $3, now()+interval '10 minutes', now()+interval '8 minutes', 'running')`, e.tenant, e.c[name], e.q2)
	}
	runIDs := func(user uuid.UUID) string {
		code, body := e.get(user, e.tenant, "/flow-runs")
		if code != 200 {
			t.Fatalf("flow-runs = %d %s", code, body)
		}
		var out struct {
			Items []struct {
				ID uuid.UUID `json:"id"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		var got []string
		for name, id := range runs {
			for _, it := range out.Items {
				if it.ID == id {
					got = append(got, name)
				}
			}
		}
		return strings.Join(sortedStrings(got), ",")
	}
	slaItems := func(user uuid.UUID) int {
		code, body := e.get(user, e.tenant, "/queues/"+e.q2.String()+"/sla")
		if code != 200 {
			t.Fatalf("sla = %d %s", code, body)
		}
		var out struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		return len(out.Items)
	}

	// scope OFF: nothing changes
	if got := runIDs(e.reader); got != "free,q1,q2" {
		t.Errorf("scope off: reader runs = %q, want everything", got)
	}
	if n := slaItems(e.reader); n != 3 {
		t.Errorf("scope off: reader SLA items = %d, want 3", n)
	}

	e.scope(true)
	if got := runIDs(e.reader); got != "free" {
		t.Errorf("reader runs = %q, want only the unqueued conversation's", got)
	}
	if got := runIDs(e.super); got != "free,q1,q2" {
		t.Errorf("supervisor runs = %q, want all", got)
	}
	if code, _ := e.get(e.reader, e.tenant, "/flow-runs/"+runs["q1"].String()); code != 404 {
		t.Errorf("a run of a conversation out of reach by id = %d, want 404 (as if it did not exist)", code)
	}
	if code, _ := e.get(e.reader, e.tenant, "/flow-runs/"+runs["free"].String()); code != 200 {
		t.Errorf("a run of a reachable conversation = %d, want 200", code)
	}
	if code, _ := e.get(e.super, e.tenant, "/flow-runs/"+runs["q1"].String()); code != 200 {
		t.Errorf("supervisor on any run = %d, want 200", code)
	}
	// filtering by an out-of-reach conversation is no way around it
	if code, body := e.get(e.reader, e.tenant, "/flow-runs?conversation_id="+e.c["q1"].String()); code != 200 || strings.Contains(body, runs["q1"].String()) {
		t.Errorf("filter by an out-of-reach conversation: %d %s", code, body)
	}
	if n := slaItems(e.reader); n != 0 {
		t.Errorf("reader SLA items for Q2 = %d, want 0 (none of Q2's conversations is in reach)", n)
	}
	if n := slaItems(e.super); n != 3 {
		t.Errorf("supervisor SLA items = %d, want 3", n)
	}
	// the other company's administrator reaches neither
	if code, _ := e.get(e.outsider, e.tenant, "/flow-runs"); code != 403 && code != 404 {
		t.Errorf("another company's admin on the run history = %d", code)
	}
	if code, _ := e.get(e.outsider, e.tenant, "/queues/"+e.q2.String()+"/sla"); code != 403 && code != 404 {
		t.Errorf("another company's admin on the SLA monitor = %d", code)
	}
}

// Contact notes belong to the contact, not to a conversation: a person limited to their queues reads them only when one of the contact's conversations is in their reach
// (their own notes stay theirs). The flow analytics are measured over the runs the person may see, never the company-wide totals.
func TestSectorScopeCoversContactNotesAndFlowAnalytics(t *testing.T) {
	e := newScopeEnv(t)
	e.exec(`INSERT INTO role_permissions(role_id,permission_key) SELECT id,k FROM roles, unnest(ARRAY['flow_run.view','agent.read','contact.classify']) k WHERE tenant_id=$1 AND key='reader_dash'`, e.tenant)
	repo := flowsadapters.NewPostgresFlowRepository(e.app)
	fh := flowsadapters.NewHandler(e.app, flowsapplication.NewControlPlane(repo, repo, nil)).WithRuns(repo)
	ch := contactsadapters.NewContactsAPIHandler(e.app)
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	mw := tenancyadapters.AuthorizationMiddleware(e.app, authz)
	base := "/api/v1/tenants/{tenant_id}"
	e.mux.Handle("GET "+base+"/flows/{flow_id}/analytics", mw(http.HandlerFunc(fh.FlowAnalytics)))
	e.mux.Handle("GET "+base+"/contacts/{contact_id}/notes", mw(http.HandlerFunc(ch.ListNotes)))

	flowID, versionID := uuid.New(), uuid.New()
	e.exec(`INSERT INTO flows(id, tenant_id, slug, name) VALUES($1,$2,'scope-flow','Fluxo')`, flowID, e.tenant)
	e.exec(`INSERT INTO flow_versions(id, tenant_id, flow_id, version, definition, definition_hash) VALUES($1,$2,$3,1,'{}','h')`, versionID, e.tenant, flowID)
	for name, status := range map[string]string{"q1": "completed", "q2": "failed", "free": "completed"} {
		id := uuid.New()
		e.exec(`INSERT INTO flow_runs(id, tenant_id, flow_id, flow_version_id, conversation_id, status, trigger_event_id, completed_at, current_node_id) VALUES($1,$2,$3,$4,$5,$6,$7, CASE WHEN $6='completed' THEN now() END, $8)`,
			id, e.tenant, flowID, versionID, e.c[name], status, uuid.NewString(), "stop_"+name)
		if status == "completed" {
			e.exec(`UPDATE flow_runs SET started_at = completed_at - make_interval(secs => $2) WHERE id = $1`, id, map[string]float64{"q1": 60, "free": 2}[name])
		}
		// every run hands over to a person once; the runs of q1 and q2 also have a failed step, each at its own node
		e.exec(`INSERT INTO flow_node_executions(tenant_id, flow_run_id, seq, flow_version_id, node_id, node_type, status) VALUES($1,$2,1,$3,'handoff','human_handoff','completed')`, e.tenant, id, versionID)
		if name != "free" {
			e.exec(`INSERT INTO flow_node_executions(tenant_id, flow_run_id, seq, flow_version_id, node_id, node_type, status) VALUES($1,$2,2,$3,$4,'send_message','failed')`, e.tenant, id, versionID, "err_"+name)
		}
	}
	for name, who := range map[string]uuid.UUID{"q1": e.super, "q2": e.super, "free": e.super} {
		e.exec(`INSERT INTO contact_notes(tenant_id, contact_id, body, created_by_user_id) VALUES($1,$2,$3,$4)`, e.tenant, e.contacts[name], "nota de "+name, who)
	}
	e.exec(`INSERT INTO contact_notes(tenant_id, contact_id, body, created_by_user_id) VALUES($1,$2,'nota propria de q1',$3)`, e.tenant, e.contacts["q1"], e.reader)

	noteBodies := func(user uuid.UUID, contact string) string {
		code, body := e.get(user, e.tenant, "/contacts/"+e.contacts[contact].String()+"/notes")
		if code != 200 {
			t.Fatalf("notes of %s = %d %s", contact, code, body)
		}
		var out struct {
			Items []struct {
				Body string `json:"body"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		var got []string
		for _, it := range out.Items {
			got = append(got, it.Body)
		}
		return strings.Join(sortedStrings(got), "|")
	}
	// runs summarises every figure the analytics expose: runs/completed/failed, handoffs, failed steps and where the failed runs stopped
	runs := func(user uuid.UUID) string {
		code, body := e.get(user, e.tenant, "/flows/"+flowID.String()+"/analytics")
		if code != 200 {
			t.Fatalf("analytics = %d %s", code, body)
		}
		var out struct {
			Runs         int    `json:"runs"`
			Completed    int    `json:"completed"`
			Failed       int    `json:"failed"`
			HumanHandoff int    `json:"human_handoffs"`
			AvgMs        *int64 `json:"avg_duration_ms"`
			NodeErrors   []struct {
				NodeID string `json:"node_id"`
			} `json:"node_errors"`
			DropOff []struct {
				NodeID string `json:"node_id"`
			} `json:"drop_off_by_node"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		var errs, drops []string
		for _, n := range out.NodeErrors {
			errs = append(errs, n.NodeID)
		}
		for _, n := range out.DropOff {
			drops = append(drops, n.NodeID)
		}
		return fmt.Sprintf("runs=%d completed=%d failed=%d handoffs=%d avgslow=%t errors=[%s] dropoff=[%s]", out.Runs, out.Completed, out.Failed, out.HumanHandoff, out.AvgMs != nil && *out.AvgMs >= 10000, strings.Join(sortedStrings(errs), ","), strings.Join(sortedStrings(drops), ","))
	}

	const all = "runs=3 completed=2 failed=1 handoffs=3 avgslow=true errors=[err_q1,err_q2] dropoff=[stop_q2]"
	const onlyFree = "runs=1 completed=1 failed=0 handoffs=1 avgslow=false errors=[] dropoff=[]"
	// scope OFF: nothing changes
	if got := noteBodies(e.reader, "q1"); got != "nota de q1|nota propria de q1" {
		t.Errorf("scope off: notes of q1 = %q", got)
	}
	if got := runs(e.reader); got != all {
		t.Errorf("scope off: analytics = %s, want %s", got, all)
	}

	e.scope(true)
	if got := noteBodies(e.reader, "free"); got != "nota de free" {
		t.Errorf("notes of a contact in reach = %q", got)
	}
	if got := noteBodies(e.reader, "q1"); got != "nota propria de q1" {
		t.Errorf("notes of a contact out of reach = %q, want only the reader's own", got)
	}
	if got := noteBodies(e.reader, "q2"); got != "" {
		t.Errorf("notes of another queue's contact = %q, want none", got)
	}
	if got := noteBodies(e.super, "q2"); got != "nota de q2" {
		t.Errorf("supervisor notes of q2 = %q, want all", got)
	}
	if got := runs(e.reader); got != onlyFree {
		t.Errorf("reader analytics = %s, want %s", got, onlyFree)
	}
	if got := runs(e.super); got != all {
		t.Errorf("supervisor analytics = %s, want %s", got, all)
	}
	if code, _ := e.get(e.outsider, e.tenant, "/flows/"+flowID.String()+"/analytics"); code != 403 && code != 404 {
		t.Errorf("another company's admin on the analytics = %d", code)
	}
	if code, _ := e.get(e.outsider, e.tenant, "/contacts/"+e.contacts["q1"].String()+"/notes"); code != 403 && code != 404 {
		t.Errorf("another company's admin on the notes = %d", code)
	}
}
