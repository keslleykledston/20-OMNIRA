package adapters_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	servicedomain "github.com/omnira/omnira/internal/servicectx/domain"
)

// ADR-0049 gate 4: the facts of Service Context & Operations reach external applications through the headless journal and its webhook engine (migration 164). Real
// PostgreSQL as omnira_app. What must hold: the module's journal stays the single source (a trigger, same transaction), the public names are exactly the module's
// (no drift), only an installation with the scope service.read hears about them, a fact tied to a conversation follows that conversation's visibility and the
// installation's limits, a fact with no conversation reaches only an installation with no limit, and one company never hears about another's.

var serviceJournalScopes = append(append([]string{}, journalScopes...), "service.read")

// ctxEvent writes one fact of the module's journal the way the module does (the trigger is what is under test).
func (e *env) ctxEvent(tenant uuid.UUID, typ, entity string, conv *uuid.UUID, payload map[string]any) {
	e.t.Helper()
	raw, _ := json.Marshal(payload)
	e.exec(`INSERT INTO context_events(id,tenant_id,event_type,entity_type,entity_id,conversation_id,actor_type,occurred_at,payload,note)
	        VALUES($1,$2,$3,$4,$5,$6,'system',now(),$7::jsonb,'nota digitada que nunca sai')`, uuid.New(), tenant, typ, entity, uuid.New(), conv, string(raw))
}

func (e *env) publicTypes(token string) map[string]int {
	e.t.Helper()
	out := map[string]int{}
	for _, ev := range e.allEvents(token, 0) {
		if !strings.HasPrefix(ev.Type, "conversation.") && !strings.HasPrefix(ev.Type, "message.") && !strings.HasPrefix(ev.Type, "contact.") && !strings.HasPrefix(ev.Type, "ticket.") { // the service facts only
			out[ev.Type]++
		}
	}
	return out
}

func TestServiceFactsAreJournaledUnderTheModulesOwnPublicNames(t *testing.T) {
	e := newEnv(t)
	e.eventsAPI()
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	app := e.install(admin, a, "CRM", serviceJournalScopes...)
	conv, _ := e.conv(a, convOpts{})

	// every type the module can store, with every payload that changes its public name
	type fact struct {
		typ, entity string
		payload     map[string]any
	}
	var facts []fact
	entities := map[string]string{"service_session": "service_session", "service_case": "service_case", "service_task": "service_task", "external_record": "external_record", "external_request": "external_request"}
	for _, typ := range servicedomain.AllEventTypes() {
		entity := entities[strings.SplitN(typ, ".", 2)[0]]
		for _, to := range []string{"", "resolved", "done", "open"} {
			p := map[string]any{}
			if to != "" {
				p["to"] = to
			}
			facts = append(facts, fact{typ, entity, p})
		}
	}
	want := map[string]int{}
	for _, f := range facts {
		var c *uuid.UUID
		if strings.HasPrefix(f.typ, "service_session") {
			c = &conv
		}
		e.ctxEvent(a, f.typ, f.entity, c, f.payload)
		want[servicedomain.PublicEvent(servicedomain.ContextEvent{EventType: f.typ, Payload: f.payload})]++
	}
	e.sequence()
	got := e.publicTypes(app.Credential.Token)
	if len(got) != len(want) {
		t.Errorf("distinct public names: %d journaled, %d expected\n got=%v\nwant=%v", len(got), len(want), got, want)
	}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("%s: %d journaled, want %d (the SQL mapping drifted from servicedomain.PublicEvent)", name, got[name], n)
		}
	}
	// the note a person typed never leaves, and the envelope names no content
	code, body, _ := e.callExt(http.MethodGet, "/api/v1/external/events?after=0&limit=200", app.Credential.Token, nil)
	if code != http.StatusOK || strings.Contains(string(body), "nota digitada") {
		t.Fatalf("events = %d; the note leaked: %v", code, strings.Contains(string(body), "nota digitada"))
	}
}

func TestOnlyAnInstallationWithTheScopeHearsAboutServiceFacts(t *testing.T) {
	e := newEnv(t)
	e.eventsAPI()
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a, "tenant_admin"), e.member(b, "tenant_admin")

	// a company whose only installation lacks the scope: nothing is journaled at all (nobody could read it)
	plain := e.install(adminA, a, "so-conversas", journalScopes...)
	e.ctxEvent(a, "service_task.created", "service_task", nil, map[string]any{})
	e.sequence()
	var n int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1 AND event_type LIKE 'service\_%'`, a).Scan(&n)
	if n != 0 {
		t.Fatalf("service facts were journaled with no installation holding the scope: %d", n)
	}

	// once an installation holds it, facts are journaled; the one without the scope still never sees them
	withScope := e.install(adminA, a, "com-servico", serviceJournalScopes...)
	e.ctxEvent(a, "service_task.created", "service_task", nil, map[string]any{})
	e.sequence()
	if got := e.publicTypes(withScope.Credential.Token)["service_task.created.v1"]; got != 1 {
		t.Errorf("the installation with the scope hears %d, want 1", got)
	}
	if got := e.publicTypes(plain.Credential.Token)["service_task.created.v1"]; got != 0 {
		t.Errorf("the installation WITHOUT the scope heard %d service facts", got)
	}

	// company B: its own installation hears B's facts only, and A's installation never hears B's
	appB := e.install(adminB, b, "CRM-B", serviceJournalScopes...)
	e.ctxEvent(b, "service_case.opened", "service_case", nil, map[string]any{})
	e.sequence()
	if got := e.publicTypes(appB.Credential.Token)["service_case.created.v1"]; got != 1 {
		t.Errorf("company B's installation hears %d of its own case, want 1", got)
	}
	if got := e.publicTypes(withScope.Credential.Token)["service_case.created.v1"]; got != 0 {
		t.Errorf("company A's installation heard company B's case: %d", got)
	}
	if got := e.publicTypes(appB.Credential.Token)["service_task.created.v1"]; got != 0 {
		t.Errorf("company B's installation heard company A's task: %d", got)
	}

	// a revoked installation stops the journaling too (nobody left who may read)
	e.exec(`UPDATE external_app_installations SET status='revoked', revoked_at=now() WHERE tenant_id=$1`, b)
	e.ctxEvent(b, "service_case.opened", "service_case", nil, map[string]any{})
	e.sequence()
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1 AND event_type='service_case.created.v1'`, b).Scan(&n)
	if n != 1 {
		t.Errorf("case facts journaled for company B: %d, want only the one from before the revocation", n)
	}
}

func TestAServiceFactFollowsTheConversationAndTheInstallationsLimits(t *testing.T) {
	e := newEnv(t)
	e.eventsAPI()
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	q1, q2 := e.queue(a), e.queue(a)
	free := e.install(admin, a, "livre", serviceJournalScopes...)
	code, body := e.admin(admin, a, http.MethodPost, "", map[string]any{"name": "so-fila-1", "scopes": serviceJournalScopes, "queue_ids": []string{q1.String()}})
	if code != http.StatusCreated {
		t.Fatalf("limited install = %d %s", code, body)
	}
	var limited created
	_ = json.Unmarshal(body, &limited)

	inQ1, _ := e.conv(a, convOpts{queue: &q1})
	inQ2, _ := e.conv(a, convOpts{queue: &q2})
	staff := e.member(a, "tenant_agent")
	staffConv, _ := e.conv(a, convOpts{staff: &staff})
	spamConv, _ := e.conv(a, convOpts{contactKind: "spam"})

	e.ctxEvent(a, "service_session.opened", "service_session", &inQ1, map[string]any{})
	e.ctxEvent(a, "service_session.opened", "service_session", &inQ2, map[string]any{})
	e.ctxEvent(a, "service_session.opened", "service_session", &staffConv, map[string]any{}) // a conversation with staff: never
	e.ctxEvent(a, "service_session.opened", "service_session", &spamConv, map[string]any{})  // a spam contact: never
	e.ctxEvent(a, "service_case.opened", "service_case", nil, map[string]any{})              // no conversation
	e.sequence()

	cnt := func(app created, typ string) int { return e.publicTypes(app.Credential.Token)[typ] }
	if got := cnt(free, "service_session.opened.v1"); got != 2 {
		t.Errorf("the unlimited installation hears %d sessions, want the 2 of visible conversations (staff and spam stay silent)", got)
	}
	if got := cnt(free, "service_case.created.v1"); got != 1 {
		t.Errorf("the unlimited installation hears %d cases, want 1", got)
	}
	if got := cnt(limited, "service_session.opened.v1"); got != 1 {
		t.Errorf("the installation limited to queue 1 hears %d sessions, want only the one of queue 1", got)
	}
	if got := cnt(limited, "service_case.created.v1"); got != 0 {
		t.Errorf("the limited installation heard a case that cannot be placed in its queue: %d", got)
	}
	// the conversation turned internal AFTER the fact: the fact stops being reported (visibility is asked again at reading)
	e.exec(`UPDATE contacts SET kind='internal', internal_role='partner' WHERE id=(SELECT contact_id FROM conversations WHERE id=$1)`, inQ1)
	if got := cnt(free, "service_session.opened.v1"); got != 1 {
		t.Errorf("after the contact turned internal the unlimited installation hears %d sessions, want 1", got)
	}
}

func TestWebhookSubscriptionsToServiceEventsNeedTheScope(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	plain := h.install(admin, a, "so-conversas", journalScopes...)
	full := h.install(admin, a, "com-servico", serviceJournalScopes...)
	body := func(types ...string) map[string]any {
		return map[string]any{"url": "https://crm.example.com/hook", "event_types": types}
	}
	if code, b := h.admin(admin, a, http.MethodPost, "/"+plain.Installation.ID.String()+"/webhooks", body("service_session.opened.v1")); code != http.StatusUnprocessableEntity {
		t.Errorf("subscribing without the scope = %d %s, want 422", code, b)
	}
	if code, b := h.admin(admin, a, http.MethodPost, "/"+plain.Installation.ID.String()+"/webhooks", body("message.created.v1", "service_case.created.v1")); code != http.StatusUnprocessableEntity {
		t.Errorf("a mixed list without the scope = %d %s, want 422", code, b)
	}
	if code, b := h.admin(admin, a, http.MethodPost, "/"+plain.Installation.ID.String()+"/webhooks", body("message.created.v1")); code != http.StatusCreated {
		t.Errorf("a conversation-only subscription = %d %s, want 201", code, b)
	}
	code, b := h.admin(admin, a, http.MethodPost, "/"+full.Installation.ID.String()+"/webhooks", body("service_session.opened.v1", "service_task.completed.v1", "external_request.state_changed.v1"))
	if code != http.StatusCreated {
		t.Fatalf("subscribing with the scope = %d %s, want 201", code, b)
	}
	var made struct {
		Subscription struct {
			ID uuid.UUID `json:"id"`
		} `json:"subscription"`
	}
	_ = json.Unmarshal(b, &made)
	if code, b := h.admin(admin, a, http.MethodPatch, "/"+plain.Installation.ID.String()+"/webhooks/"+made.Subscription.ID.String(), body("service_session.closed.v1")); code != http.StatusUnprocessableEntity && code != http.StatusNotFound {
		t.Errorf("widening a subscription of another installation = %d %s", code, b)
	}
	if code, b := h.admin(admin, a, http.MethodPost, "/"+full.Installation.ID.String()+"/webhooks", body("service_session.nonsense.v1")); code != http.StatusBadRequest {
		t.Errorf("an unknown name = %d %s, want 400", code, b)
	}
}

func TestAServiceFactIsDeliveredSignedAndStopsWhenTheScopeIsWithdrawn(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	app := h.install(admin, a, "CRM", serviceJournalScopes...)
	rcv := newReceiver(t)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true, "service_task.created.v1", "service_case.created.v1")
	w := h.worker(rcv.client())

	conv, _ := h.conv(a, convOpts{})
	h.ctxEvent(a, "service_task.created", "service_task", nil, map[string]any{"priority": "high"})
	h.ctxEvent(a, "service_case.opened", "service_case", &conv, map[string]any{})
	if n := h.step(w); n < 2 {
		t.Fatalf("the worker attempted %d deliveries, want 2", n)
	}
	calls := rcv.snapshot()
	if len(calls) != 2 {
		var st, ec string
		_ = h.seed.QueryRow(h.ctx, `SELECT string_agg(status || ':' || COALESCE(last_error_class,'-'), ',') FROM webhook_deliveries WHERE subscription_id=$1`, hk.id).Scan(&st)
		_ = ec
		t.Fatalf("the receiver got %d calls, want 2 (deliveries: %s)", len(calls), st)
	}
	for _, c := range calls {
		if !h.twoSignaturesOK(c, hk.secret) {
			t.Errorf("a call does not verify: %v", c.header)
		}
		if strings.Contains(string(c.body), "nota digitada") {
			t.Errorf("the note leaked: %s", c.body)
		}
	}
	if h.deliveries(hk, "delivered") != 2 {
		t.Errorf("delivered = %d", h.deliveries(hk, "delivered"))
	}

	// a fact is journaled while the scope is held, then the scope is withdrawn before the worker sends it: the delivery is not sent
	h.ctxEvent(a, "service_task.created", "service_task", nil, map[string]any{})
	h.sequence()
	h.exec(`UPDATE external_app_installations SET scopes = ARRAY['conversation.read','events.read'] WHERE id=$1`, app.Installation.ID)
	before := len(rcv.snapshot())
	h.step(w)
	if len(rcv.snapshot()) != before {
		t.Errorf("a service fact was delivered after the scope was withdrawn (%d calls)", len(rcv.snapshot())-before)
	}
	var cancelled int
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM webhook_deliveries WHERE subscription_id=$1 AND status='cancelled' AND last_error_class='not_visible'`, hk.id).Scan(&cancelled)
	if cancelled != 1 {
		t.Errorf("the delivery of the withdrawn scope is cancelled as not_visible: %d", cancelled)
	}
}
