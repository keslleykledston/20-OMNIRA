package adapters_test

// ADR-0040 phase 03 (pilot) on a real PostgreSQL, with the application's own role: the data layer answers the delegated context for READING
// conversations, messages, media and the contact card. What must hold:
//   - each table answers only the domain it belongs to (conversation / media / contact), only for the instances the grant covers, only in the
//     delegated context; nothing can be WRITTEN through it;
//   - one hub never reads what another hub's agents may, one instance never reads another's, and a forged acting setting changes nothing;
//   - a person who is a member AND a delegate gets, in each context, only what that context gives (the member predicates are false while acting for a hub);
//   - through the REAL handlers and middleware: the five routes work, a route that was not marked delegable is a uniform 404 even for a
//     member+delegate, a missing key is a 403, revocation refuses the next request, and the media file is served by the path variant.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	mediaadapters "github.com/omnira/omnira/internal/media/adapters"
	mediaports "github.com/omnira/omnira/internal/media/ports"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

// mediaOf seeds one clean inbound media row (with a transcript) for the first message of a conversation (and returns the message and media ids).
func (w *world) mediaOf(tenantKey string, convKey string) (message, media uuid.UUID) {
	w.t.Helper()
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM messages WHERE tenant_id = $1 AND conversation_id = $2 LIMIT 1`, w.tenant[tenantKey], w.conv[convKey]).Scan(&message))
	media = uuid.New()
	w.exec(`INSERT INTO message_media(id, tenant_id, message_id, status, kind, mime, size_bytes, sha256) VALUES($1,$2,$3,'clean','image','image/png',7,'abc')`,
		media, w.tenant[tenantKey], message)
	w.exec(`INSERT INTO message_media_analysis(tenant_id, message_id, kind, status, body) VALUES($1,$2,'transcript','done','hello')`, w.tenant[tenantKey], message)
	return message, media
}

func (w *world) outboundMediaOf(tenantKey, convKey string) {
	w.t.Helper()
	var message uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM messages WHERE tenant_id = $1 AND conversation_id = $2 LIMIT 1`, w.tenant[tenantKey], w.conv[convKey]).Scan(&message))
	uploader := w.user("uploader")
	w.exec(`INSERT INTO message_outbound_media(tenant_id, conversation_id, uploaded_by, kind, mime, size_bytes, sha256, file_name, message_id)
	        VALUES($1,$2,$3,'image','image/png',7,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','a.png',$4)`, w.tenant[tenantKey], w.conv[convKey], uploader, message)
}

func (w *world) queueScope(tenantKey string, queues ...uuid.UUID) {
	ids := make([]string, len(queues))
	for i, q := range queues {
		ids[i] = q.String()
	}
	raw, _ := json.Marshal(map[string]any{"queue_ids": ids})
	w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, w.contract[tenantKey], string(raw))
}

// seen counts, as the user, what each table shows for ONE instance, optionally after entering the delegated context for another/the same one.
type seen struct{ conversations, messages, contacts, media, outbound, analysis int }

func (w *world) seenIn(user uuid.UUID, actInstance string, count string) seen {
	w.t.Helper()
	var s seen
	w.inSession(user, func(ctx context.Context, q platformdb.Querier) {
		if actInstance != "" && !w.enter(ctx, q, user, actInstance) {
			w.t.Fatalf("could not enter the delegated context for %s", actInstance)
		}
		one := func(table string) int {
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, w.tenant[count]).Scan(&n))
			return n
		}
		s = seen{one("conversations"), one("messages"), one("contacts"), one("message_media"), one("message_outbound_media"), one("message_media_analysis")}
	})
	return s
}

// Conversations and messages are readable through the Hub's own policies (000095: any live grant, the contract's queue scope applied). The new
// policies of 000109 cover what was never readable through a Hub: the contact card and the files, each by its own domain.
func TestDelegatedContactAndMediaReadsFollowTheirDomain(t *testing.T) {
	w := newWorld(t)
	w.mediaOf("A", "A")
	w.mediaOf("A", "A2")
	w.outboundMediaOf("A", "A")
	w.mediaOf("B", "B")
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read", "conversation.reply", "media.read", "contact.read", "contact.classify")
	const cm = 3 // conversations and messages of A, through the legacy grant policies, whatever the keys are
	for _, c := range []struct {
		name string
		keys []string
		want seen
	}{
		{"nothing granted", nil, seen{conversations: cm, messages: cm}},
		{"conversation.read", []string{"conversation.read"}, seen{conversations: cm, messages: cm}},
		{"media.read", []string{"media.read"}, seen{cm, cm, 0, 2, 1, 2}},
		{"contact.read", []string{"contact.read"}, seen{cm, cm, 1, 0, 0, 0}},
		{"contact.classify (write implies read)", []string{"contact.classify"}, seen{cm, cm, 1, 0, 0, 0}},
		{"all", []string{"conversation.read", "media.read", "contact.read"}, seen{cm, cm, 1, 2, 1, 2}},
	} {
		w.grantKeys(g, c.keys...)
		if len(c.keys) == 0 {
			// nothing delegated: the door itself refuses, there is no delegated context to read in
			var entered bool
			w.inSession(agent, func(ctx context.Context, q platformdb.Querier) { entered = w.enter(ctx, q, agent, "A") })
			if entered {
				t.Errorf("%s: the delegated context must not open", c.name)
			}
			continue
		}
		if got := w.seenIn(agent, "A", "A"); got != c.want {
			t.Errorf("%s: instance A shows %+v, want %+v", c.name, got, c.want)
		}
		// never another instance, whatever the grant holds
		if got := w.seenIn(agent, "A", "B"); got != (seen{}) {
			t.Errorf("%s: instance B leaked %+v", c.name, got)
		}
		// not acting for a hub: contacts and files stay invisible (they are delegated-context only)
		if got := w.seenIn(agent, "", "A"); got.contacts != 0 || got.media != 0 || got.outbound != 0 || got.analysis != 0 {
			t.Errorf("%s: outside the delegated context the agent saw contacts/files: %+v", c.name, got)
		}
	}
}

// A contract that serves only some queues (service_scope.queue_ids) never reaches the contacts or the files of the others.
func TestDelegatedContactAndMediaInheritTheContractsQueueScope(t *testing.T) {
	w := newWorld(t)
	w.mediaOf("A", "A")  // queue 1
	w.mediaOf("A", "A2") // queue 2
	w.outboundMediaOf("A", "A")
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "media.read", "contact.read")
	w.grantKeys(g, "media.read", "contact.read")
	for _, c := range []struct {
		name  string
		setup func()
		want  seen
	}{
		{"no scope: everything", func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}' WHERE id = $1`, w.contract["A"])
		}, seen{3, 3, 1, 2, 1, 2}},
		{"queue 1 only", func() { w.queueScope("A", w.queue1["A"]) }, seen{1, 1, 1, 1, 1, 1}},
		{"queue 2 only", func() { w.queueScope("A", w.queue2["A"]) }, seen{1, 1, 1, 1, 0, 1}},
		{"an empty allow-list", func() { w.queueScope("A") }, seen{}},
		{"a malformed allow-list", func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{"queue_ids":"x"}' WHERE id = $1`, w.contract["A"])
		}, seen{}},
	} {
		c.setup()
		if got := w.seenIn(agent, "A", "A"); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestNothingCanBeWrittenThroughTheDelegatedReadPolicies(t *testing.T) {
	w := newWorld(t)
	w.mediaOf("A", "A")
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	// Only READ keys (and reply, which writes through the Hub's own service, not through these tables): the read policies must write nothing.
	// contact.classify is deliberately not here: since 000110 it DOES allow updating a contact (serving_contacts_integration_test.go proves exactly
	// what it allows and what it does not).
	w.ceiling("A", "conversation.read", "conversation.reply", "media.read", "contact.read", "contact.classify")
	w.grantKeys(g, "conversation.read", "conversation.reply", "media.read", "contact.read")
	statements := map[string]string{
		"UPDATE conversations": `UPDATE conversations SET status = 'closed' WHERE tenant_id = $1`,
		"DELETE conversations": `DELETE FROM conversations WHERE tenant_id = $1`,
		"UPDATE messages":      `UPDATE messages SET body = 'x' WHERE tenant_id = $1`,
		"DELETE messages":      `DELETE FROM messages WHERE tenant_id = $1`,
		"UPDATE contacts":      `UPDATE contacts SET display_name = 'x' WHERE tenant_id = $1`,
		"DELETE contacts":      `DELETE FROM contacts WHERE tenant_id = $1`,
		"UPDATE message_media": `UPDATE message_media SET status = 'pending' WHERE tenant_id = $1`,
		"DELETE message_media": `DELETE FROM message_media WHERE tenant_id = $1`,
		"UPDATE analysis":      `UPDATE message_media_analysis SET body = 'x' WHERE tenant_id = $1`,
	}
	attempt := func(stmt string, args ...any) (int64, error) {
		var affected int64
		err := platformdb.WithTenantSession(w.ctx, w.app, agent, false, func(c context.Context) error {
			q := platformdb.QuerierFromContext(c, w.app)
			var ok *bool
			if err := q.QueryRow(c, `SELECT lock_served_tenant($1,$2,$3)`, w.tenant["A"], agent, w.hub).Scan(&ok); err != nil || ok == nil || !*ok {
				t.Fatal("enter")
			}
			tag, err := q.Exec(c, stmt, args...)
			affected = tag.RowsAffected()
			return err
		})
		return affected, err
	}
	for name, stmt := range statements {
		if n, err := attempt(stmt, w.tenant["A"]); err == nil && n != 0 {
			t.Errorf("%s changed %d row(s) through the delegated read policies", name, n)
		}
	}
	if _, err := attempt(`INSERT INTO messages(tenant_id, conversation_id, direction) VALUES($1,$2,'outbound')`, w.tenant["A"], w.conv["A"]); err == nil {
		t.Error("a delegated agent must not insert a message through the data layer")
	}
	if _, err := attempt(`INSERT INTO contacts(tenant_id, display_name, phone_e164) VALUES($1,'x','+5511999990000')`, w.tenant["A"]); err == nil {
		t.Error("a delegated agent must not insert a contact")
	}
	if n := w.count(`SELECT count(*) FROM messages WHERE tenant_id = $1 AND body = 'x'`, w.tenant["A"]); n != 0 {
		t.Errorf("%d messages were rewritten", n)
	}
	if n := w.count(`SELECT count(*) FROM contacts WHERE tenant_id = $1 AND display_name = 'x'`, w.tenant["A"]); n != 0 {
		t.Errorf("%d contacts were rewritten", n)
	}
}

func TestOneHubNeverReadsWhatAnotherHubsAgentsMay(t *testing.T) {
	w := newWorld(t)
	w.mediaOf("A", "A")
	// a SECOND hub with its own contract on instance A and its own agent, who holds contact.read only
	hub2 := uuid.New()
	w.exec(`INSERT INTO service_hubs(id, name) VALUES($1, $2)`, hub2, "Other "+hub2.String()[:8])
	contract2 := uuid.New()
	w.exec(`INSERT INTO hub_tenant_service_contracts(id, hub_id, tenant_id, valid_from, delegable_permissions) VALUES($1,$2,$3, now() - interval '1 day', ARRAY['contact.read'])`, contract2, hub2, w.tenant["A"])
	agent2 := w.user("agent2")
	w.exec(`INSERT INTO hub_memberships(hub_id, user_id, role_id) VALUES($1,$2,$3)`, hub2, agent2, w.roleHubAgent)
	w.exec(`INSERT INTO effective_access_grants(hub_id, user_id, tenant_id, service_contract_id, valid_from, permissions) VALUES($1,$2,$3,$4, now() - interval '1 day', ARRAY['contact.read','media.read'])`, hub2, agent2, w.tenant["A"], contract2)
	// agent (hub 1) holds media.read only (and the ceiling of hub 1's contract allows both)
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "media.read", "contact.read")
	w.grantKeys(g, "media.read")

	enterVia := func(ctx context.Context, q platformdb.Querier, user, hub uuid.UUID) bool {
		var ok *bool
		w.must(q.QueryRow(ctx, `SELECT lock_served_tenant($1,$2,$3)`, w.tenant["A"], user, hub).Scan(&ok))
		return ok != nil && *ok
	}
	count := func(ctx context.Context, q platformdb.Querier, table string) int {
		var n int
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id=$1`, w.tenant["A"]).Scan(&n))
		return n
	}
	// agent2 acts for HIS hub: contact yes; media NO (his grant holds media.read but hub2's ceiling does not delegate it)
	w.inSession(agent2, func(ctx context.Context, q platformdb.Querier) {
		if !enterVia(ctx, q, agent2, hub2) {
			t.Fatal("agent2 must enter through hub2")
		}
		if c, m := count(ctx, q, "contacts"), count(ctx, q, "message_media"); c != 1 || m != 0 {
			t.Errorf("agent2 via hub2: contacts=%d media=%d, want 1 and 0 (the ceiling of HIS contract decides)", c, m)
		}
	})
	// agent acts for hub 1: media yes (his grant and hub 1's ceiling), contacts no
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if !enterVia(ctx, q, agent, w.hub) {
			t.Fatal("agent must enter through hub1")
		}
		if c, m := count(ctx, q, "contacts"), count(ctx, q, "message_media"); c != 0 || m != 1 {
			t.Errorf("agent via hub1: contacts=%d media=%d, want 0 and 1", c, m)
		}
	})
	// agent cannot enter through hub2, and forging the setting for hub2 yields nothing (he has no grant through hub2)
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if enterVia(ctx, q, agent, hub2) {
			t.Error("agent must not enter through a hub that did not grant him")
		}
		_, err := q.Exec(ctx, `SELECT set_config('app.acting_hub', $1, true)`, hub2.String())
		w.must(err)
		if c, m := count(ctx, q, "contacts"), count(ctx, q, "message_media"); c != 0 || m != 0 {
			t.Errorf("a forged acting hub revealed contacts=%d media=%d", c, m)
		}
	})
	// agent2 forging hub 1: no grant through hub 1, so nothing
	w.inSession(agent2, func(ctx context.Context, q platformdb.Querier) {
		_, err := q.Exec(ctx, `SELECT set_config('app.acting_hub', $1, true)`, w.hub.String())
		w.must(err)
		if c, m := count(ctx, q, "contacts"), count(ctx, q, "message_media"); c != 0 || m != 0 {
			t.Errorf("agent2 forging hub1 revealed contacts=%d media=%d", c, m)
		}
	})
}

func TestAMemberWhoIsAlsoADelegateGetsExactlyTheContextTheyActIn(t *testing.T) {
	w := newWorld(t)
	w.mediaOf("A", "A")
	both := w.hubAgent("both")
	w.directMember(both, "A")
	g := w.grant(both, "A")
	w.ceiling("A", "conversation.read", "contact.read", "media.read")
	w.grantKeys(g, "conversation.read") // delegated: no contact, no media
	// as a member: the contact and the files (their role), as always
	if got := w.seenIn(both, "", "A"); got.contacts == 0 || got.media == 0 {
		t.Fatalf("as a member the contacts and files are readable: %+v", got)
	}
	// acting for the hub: the grant gives neither; the membership contributes nothing, not even to the policies that name membership
	if got := w.seenIn(both, "A", "A"); got.contacts != 0 || got.media != 0 || got.outbound != 0 || got.analysis != 0 {
		t.Errorf("acting for the hub the person must see only what the grant gives (no contact, no file), got %+v", got)
	}
	// the member predicates themselves
	w.inSession(both, func(ctx context.Context, q platformdb.Querier) {
		var asMember, asAdmin bool
		w.must(q.QueryRow(ctx, `SELECT has_active_membership($1,$2)`, w.tenant["A"], both).Scan(&asMember))
		if !asMember {
			t.Error("has_active_membership must stay true outside the delegated context")
		}
		if !w.enter(ctx, q, both, "A") {
			t.Fatal("enter")
		}
		w.must(q.QueryRow(ctx, `SELECT has_active_membership($1,$2)`, w.tenant["A"], both).Scan(&asMember))
		w.must(q.QueryRow(ctx, `SELECT has_active_admin_membership($1,$2)`, w.tenant["A"], both).Scan(&asAdmin))
		if asMember || asAdmin {
			t.Errorf("while acting for a hub the member predicates must be false (member=%v admin=%v)", asMember, asAdmin)
		}
	})
}

// --- through the real handlers

type readsAPI struct {
	w     *world
	h     http.Handler
	store *mediaadapters.FileStore
}

func (w *world) readsAPI(t *testing.T) *readsAPI {
	t.Helper()
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	store, err := mediaadapters.OpenFileStore(t.TempDir())
	w.must(err)
	inbox := inboxadapters.NewInboxAPIHandler(w.app).WithMediaReader(mediaadapters.NewReader(w.app, store))
	contacts := contactsadapters.NewContactsAPIHandler(w.app)
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	session := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	who := func(next http.Handler) http.Handler { // stands in for authentication: the test user comes from a header
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
				r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u}))
			}
			next.ServeHTTP(rw, r)
		})
	}
	const t0 = "/api/v1/tenants/{tenant_id}"
	mux := http.NewServeMux()
	mux.Handle("GET "+t0+"/inbox/conversations", tenancyadapters.Delegable("conversation.read", who(session(http.HandlerFunc(inbox.ListConversations)))))
	mux.Handle("GET "+t0+"/inbox/conversations/{conversation_id}", tenancyadapters.Delegable("conversation.read", who(session(http.HandlerFunc(inbox.GetConversation)))))
	mux.Handle("GET "+t0+"/inbox/conversations/{conversation_id}/messages", tenancyadapters.Delegable("conversation.read", who(session(http.HandlerFunc(inbox.ListMessages)))))
	mux.Handle("GET "+t0+"/contacts/{contact_id}", tenancyadapters.Delegable("contact.read", who(session(http.HandlerFunc(contacts.GetContact)))))
	mux.Handle("GET "+t0+"/messages/{message_id}/media", tenancyadapters.Delegable("media.read", who(session(http.HandlerFunc(inbox.GetMedia)))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/serve/{tenant_id}/messages/{message_id}/media", tenancyadapters.Delegable("media.read", who(tenancyadapters.DelegatedPath(w.app)(http.HandlerFunc(inbox.GetMedia)))))
	// a route that was NOT marked delegable, with its own (membership-based) authorization like the unmigrated modules have
	mux.Handle("GET "+t0+"/tickets-not-migrated", who(session(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusOK) }))))
	return &readsAPI{w: w, h: mux, store: store}
}

func (a *readsAPI) get(user uuid.UUID, path, acting string) (int, string, http.Header) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Test-User", user.String())
	if acting != "" {
		req.Header.Set("X-Omnira-Acting-As", acting)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestDelegatedPilotRoutesThroughTheRealHandlers(t *testing.T) {
	w := newWorld(t)
	api := w.readsAPI(t)
	message, media := w.mediaOf("A", "A")
	var contactA uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM contacts WHERE tenant_id = $1`, w.tenant["A"]).Scan(&contactA))
	var contactB uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM contacts WHERE tenant_id = $1`, w.tenant["B"]).Scan(&contactB))
	// the media file really exists in the clean area (the reader opens only what the database row names)
	work := mediaports.Work{ID: media, TenantID: w.tenant["A"], MessageID: message}
	w.must(api.store.PutQuarantine(work, []byte("PNGDATA")))
	w.must(api.store.Promote(work))

	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read", "media.read", "contact.read")
	w.grantKeys(g, "conversation.read", "media.read", "contact.read")
	hub := "hub:" + w.hub.String()
	base := "/api/v1/tenants/" + w.tenant["A"].String()

	t.Run("the five routes answer, with the instance's data and only it", func(t *testing.T) {
		code, body, _ := api.get(agent, base+"/inbox/conversations?status=all", hub)
		if code != 200 || strings.Count(body, `"id"`) < 3 {
			t.Fatalf("list: %d %s", code, body)
		}
		if strings.Contains(body, w.conv["B"].String()) {
			t.Error("the list leaked another instance's conversation")
		}
		if code, body, _ = api.get(agent, base+"/inbox/conversations/"+w.conv["A"].String(), hub); code != 200 || !strings.Contains(body, w.conv["A"].String()) {
			t.Errorf("get: %d %s", code, body)
		}
		if code, body, _ = api.get(agent, base+"/inbox/conversations/"+w.conv["A"].String()+"/messages", hub); code != 200 || !strings.Contains(body, "media_status") {
			t.Errorf("messages with media status: %d %s", code, body)
		}
		if code, body, _ = api.get(agent, base+"/contacts/"+contactA.String(), hub); code != 200 || !strings.Contains(body, contactA.String()) {
			t.Errorf("contact: %d %s", code, body)
		}
		code, body, hdr := api.get(agent, base+"/messages/"+message.String()+"/media", hub)
		if code != 200 || body != "PNGDATA" || hdr.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("media: %d %q %v", code, body, hdr)
		}
	})

	t.Run("a contract limited to other queues hides the contact and the file of this conversation", func(t *testing.T) {
		w.queueScope("A", w.queue2["A"]) // the conversation of this contact and file is in queue 1
		if code, _, _ := api.get(agent, base+"/contacts/"+contactA.String(), hub); code != 200 {
			t.Errorf("the contact also has a conversation in queue 2 (A2): %d, want 200", code)
		}
		if code, _, _ := api.get(agent, base+"/messages/"+message.String()+"/media", hub); code != 404 {
			t.Errorf("the file of a conversation outside the contract's queues: %d, want 404", code)
		}
		w.queueScope("A") // an empty allow-list: nothing at all
		if code, _, _ := api.get(agent, base+"/contacts/"+contactA.String(), hub); code != 404 {
			t.Errorf("empty allow-list, contact: %d, want 404", code)
		}
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}' WHERE id = $1`, w.contract["A"])
	})

	t.Run("the media path variant (an <img> cannot send the header) is the same door", func(t *testing.T) {
		path := "/api/v1/hubs/" + w.hub.String() + "/serve/" + w.tenant["A"].String() + "/messages/" + message.String() + "/media"
		if code, body, _ := api.get(agent, path, ""); code != 200 || body != "PNGDATA" {
			t.Errorf("media by path: %d %q", code, body)
		}
		// the hub in the path is a target, not authority
		if code, _, _ := api.get(agent, "/api/v1/hubs/"+uuid.NewString()+"/serve/"+w.tenant["A"].String()+"/messages/"+message.String()+"/media", ""); code != 404 {
			t.Errorf("another hub in the path: %d, want 404", code)
		}
		if code, _, _ := api.get(agent, "/api/v1/hubs/"+w.hub.String()+"/serve/"+w.tenant["B"].String()+"/messages/"+message.String()+"/media", ""); code != 404 {
			t.Errorf("another instance in the path: %d, want 404", code)
		}
		if code, _, _ := api.get(agent, "/api/v1/hubs/not-a-uuid/serve/"+w.tenant["A"].String()+"/messages/"+message.String()+"/media", ""); code != 400 {
			t.Errorf("malformed hub in the path: %d, want 400", code)
		}
	})

	t.Run("another instance's rows are not reachable through this instance's URL, nor through the other's", func(t *testing.T) {
		// B's conversation asked through A's URL: the tenant context says A, RLS gives nothing of B
		if code, _, _ := api.get(agent, base+"/inbox/conversations/"+w.conv["B"].String(), hub); code != 404 {
			t.Errorf("B's conversation through A: %d, want 404", code)
		}
		if code, _, _ := api.get(agent, base+"/contacts/"+contactB.String(), hub); code != 404 {
			t.Errorf("B's contact through A: %d, want 404", code)
		}
		// B's own URL: no grant there at all
		if code, _, _ := api.get(agent, "/api/v1/tenants/"+w.tenant["B"].String()+"/inbox/conversations", hub); code != 404 {
			t.Errorf("an instance with no grant: %d, want 404", code)
		}
	})

	t.Run("a missing key is a 403 and a route that is not delegable is a 404, even for a member who is also a delegate", func(t *testing.T) {
		w.grantKeys(g, "conversation.read") // no contact.read, no media.read
		if code, _, _ := api.get(agent, base+"/contacts/"+contactA.String(), hub); code != 403 {
			t.Errorf("contact without contact.read: %d, want 403", code)
		}
		if code, _, _ := api.get(agent, base+"/messages/"+message.String()+"/media", hub); code != 403 {
			t.Errorf("media without media.read: %d, want 403", code)
		}
		w.grantKeys(g, "conversation.read", "media.read", "contact.read")
		both := w.hubAgent("both")
		w.directMember(both, "A")
		gb := w.grant(both, "A")
		w.grantKeys(gb, "conversation.read")
		if code, _, _ := api.get(both, base+"/tickets-not-migrated", ""); code != 200 {
			t.Fatalf("as a member the unmigrated route works as before: %d", code)
		}
		if code, _, _ := api.get(both, base+"/tickets-not-migrated", hub); code != 404 {
			t.Errorf("acting for the hub, an unmigrated route must be a 404 (it would otherwise answer through the membership): %d", code)
		}
		// and the member context of the same person is exactly what it was
		if code, _, _ := api.get(both, base+"/contacts/"+contactA.String(), "member"); code != 200 {
			t.Errorf("member context, contact: %d", code)
		}
		if code, _, _ := api.get(both, base+"/contacts/"+contactA.String(), hub); code != 403 {
			t.Errorf("hub context with only conversation.read must not read the contact (the membership gives nothing here): %d, want 403", code)
		}
	})

	t.Run("revocation refuses the very next request, on every route", func(t *testing.T) {
		paths := []string{base + "/inbox/conversations", base + "/contacts/" + contactA.String(), base + "/messages/" + message.String() + "/media"}
		for _, p := range paths {
			if code, _, _ := api.get(agent, p, hub); code != 200 {
				t.Fatalf("before revoking %s: %d", p, code)
			}
		}
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		for _, p := range paths {
			if code, _, _ := api.get(agent, p, hub); code != 404 {
				t.Errorf("after revoking %s: %d, want 404", p, code)
			}
		}
		if code, _, _ := api.get(agent, "/api/v1/hubs/"+w.hub.String()+"/serve/"+w.tenant["A"].String()+"/messages/"+message.String()+"/media", ""); code != 404 {
			t.Errorf("the media path variant after revoking: %d, want 404", code)
		}
	})
}

// An ADMINISTRATOR of the instance who attends through a Hub is, while acting for the hub, not an administrator: the delegated context never
// inherits administrative privileges (ADR-0040 section 3.2).
func TestAnInstanceAdminActingForAHubIsNoAdmin(t *testing.T) {
	w := newWorld(t)
	admin := w.hubAgent("admin-and-agent")
	w.exec(`INSERT INTO memberships (tenant_id, user_id, role_id) VALUES ($1, $2, $3)`, w.tenant["A"], admin, w.role("tenant_admin"))
	g := w.grant(admin, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")
	w.inSession(admin, func(ctx context.Context, q platformdb.Querier) {
		var asAdmin bool
		w.must(q.QueryRow(ctx, `SELECT has_active_admin_membership($1,$2)`, w.tenant["A"], admin).Scan(&asAdmin))
		if !asAdmin {
			t.Fatal("as a member the administrator is an administrator (the test would be vacuous otherwise)")
		}
		if !w.enter(ctx, q, admin, "A") {
			t.Fatal("enter")
		}
		w.must(q.QueryRow(ctx, `SELECT has_active_admin_membership($1,$2)`, w.tenant["A"], admin).Scan(&asAdmin))
		if asAdmin {
			t.Error("acting for a hub, the instance administrator must not keep the administrator privileges")
		}
		// and a policy that only an administrator passes (the credentials of the channels) stays shut
		var n int
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM tenant_ai_integrations WHERE tenant_id = $1`, w.tenant["A"]).Scan(&n))
		if n != 0 {
			t.Errorf("an administrator-only table answered %d row(s) in the delegated context", n)
		}
	})
}

// The Hub inbox advertises, per company, whether the full workspace may be opened (display only: every request is decided again).
func TestTheHubInboxAdvertisesTheFullContextOnlyWhereItIsReal(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	agent := w.hubAgent("agent")
	gA := w.grant(agent, "A")
	w.grant(agent, "B") // a grant with no delegated keys
	w.ceiling("A", "conversation.read", "media.read")
	w.grantKeys(gA, "conversation.read")
	full := func() map[string]bool {
		code, body, _ := api.do("GET", "/api/v1/hubs/"+w.hub.String()+"/inbox", agent, nil)
		if code != 200 {
			t.Fatalf("inbox: %d %s", code, body)
		}
		var out struct {
			Companies []struct {
				ID          uuid.UUID `json:"id"`
				FullContext bool      `json:"full_context"`
			} `json:"companies"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, c := range out.Companies {
			for k, id := range w.tenant {
				if id == c.ID {
					m[k] = c.FullContext
				}
			}
		}
		return m
	}
	tenancyadapters.EnableDelegatedServing(false)
	if got := full(); got["A"] || got["B"] {
		t.Errorf("serving off: nothing may advertise the full context: %v", got)
	}
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	if got := full(); !got["A"] || got["B"] {
		t.Errorf("serving on: A has conversation.read (true), B has no keys (false): %v", got)
	}
	w.grantKeys(gA, "media.read") // no conversation.read: the workspace cannot open
	if got := full(); got["A"] {
		t.Errorf("without conversation.read the full context must not be advertised: %v", got)
	}
	w.grantKeys(gA, "conversation.read")
	w.ceiling("A") // the contract withdraws the ceiling
	if got := full(); got["A"] {
		t.Errorf("a withdrawn ceiling must withdraw the advertisement: %v", got)
	}
}
