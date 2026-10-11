package adapters_test

// ADR-0040 phase 06: a Hub agent reads the MEMORY of the contacts of the instance (earlier attendances with their summary, open follow-ups; key
// attendance.history) and resolves an open item (key followup.resolve), through the instance's own attendance routes. On a real PostgreSQL with the
// application's role and the REAL attendance handler and service. What must hold:
//   - each key is needed, asked by the route AND by the database (a person who is also a member of the instance gets nothing as a member while acting as an agent);
//   - one instance at a time, the contract's queue scope narrows what the contact shows, another instance's closures and items are invisible, a revoked grant is a 404;
//   - resolving needs both keys, only opens an OPEN item, signs it with the agent and writes one audit row naming the hub, never the text; the member's route is unchanged;
//   - a delegate never inserts a closure or an item, and the search over earlier messages stays closed to them.

import (
	"context"
	"encoding/json"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

func TestDelegatedAttendanceHistoryAndFollowUpsThroughTheRealRoutes(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	svc := attendanceapplication.NewService(attendanceadapters.NewPostgresRepository(w.app), attendanceadapters.NewAuthorizer(w.app),
		attendanceadapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(w.app)))
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	session := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	who := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
				r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u}))
			}
			next.ServeHTTP(rw, r)
		})
	}
	mux := http.NewServeMux()
	attendanceadapters.NewHandler(svc).Routes(mux, func(fn http.HandlerFunc) http.Handler { return who(session(fn)) })
	do := func(method, path, body string, user uuid.UUID, acting string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Test-User", user.String())
		req.Header.Set("Content-Type", "application/json")
		if acting != "" {
			req.Header.Set("X-Omnira-Acting-As", acting)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	tenantPath := func(key string) string { return "/api/v1/tenants/" + w.tenant[key].String() }
	historyOf := func(tenantKey string, contact uuid.UUID) string {
		return tenantPath(tenantKey) + "/contacts/" + contact.String() + "/attendance-history"
	}
	hub := "hub:" + w.hub.String()
	base := provisioning.ServingPresets["completo"]
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	w.ceiling("A", with("attendance.history", "followup.resolve")...)
	w.ceiling("B", with("attendance.history", "followup.resolve")...)

	reader := w.colleague("reader", "A", with("attendance.history")...)
	resolver := w.colleague("resolver", "A", with("attendance.history", "followup.resolve")...)
	blind := w.colleague("blind", "A", base...)                                  // can see the conversation and the contact, never the memory
	resolveOnly := w.colleague("resolve-only", "A", with("followup.resolve")...) // the resolving key without the sight that an UPDATE needs
	member := w.user("member")
	w.directMember(member, "A")
	both := w.colleague("both", "A", base...) // a hub agent who is ALSO a member of the instance
	w.directMember(both, "A")
	// a member who is ALSO an agent holding the sight of the memory but not the resolving key, and who ALSO serves the other instance (to prove the pin to one instance)
	memberReader := w.colleague("member-reader", "A", with("attendance.history")...)
	w.directMember(memberReader, "A")
	w.grantKeys(w.grantReply(reader, "B"), with("attendance.history")...)

	contactA, contactB := w.contactOf("A"), w.contactOf("B")
	closure := func(tenantKey, convKey string, contact uuid.UUID, summary string) uuid.UUID {
		id := uuid.New()
		w.exec(`INSERT INTO conversation_closures (id, tenant_id, conversation_id, contact_id, source, reason, summary) VALUES ($1,$2,$3,$4,'agent','resolved',$5)`,
			id, w.tenant[tenantKey], w.conv[convKey], contact, summary)
		return id
	}
	item := func(tenantKey, convKey string, contact, closureID uuid.UUID, text string) uuid.UUID {
		id := uuid.New()
		w.exec(`INSERT INTO follow_up_items (id, tenant_id, contact_id, conversation_id, closure_id, kind, text) VALUES ($1,$2,$3,$4,$5,'pending',$6)`,
			id, w.tenant[tenantKey], contact, w.conv[convKey], closureID, text)
		return id
	}
	c1 := closure("A", "A2", contactA, "Cliente pediu segunda via do boleto.")
	c2 := closure("A", "A0", contactA, "Instalação agendada para sexta.")
	openItem := item("A", "A2", contactA, c1, "ligar para confirmar a instalação")
	toResolve := item("A", "A0", contactA, c2, "enviar o boleto por e-mail")
	closureB := closure("B", "B", contactB, "Segredo da outra empresa.")
	itemB := item("B", "B", contactB, closureB, "pendência da outra empresa")

	t.Run("a reader sees the earlier attendances and the open items of the contact, and nothing of another instance", func(t *testing.T) {
		code, body := do("GET", historyOf("A", contactA), "", reader, hub)
		if code != 200 || !strings.Contains(body, "segunda via do boleto") || !strings.Contains(body, "Instalação agendada") || !strings.Contains(body, "ligar para confirmar") {
			t.Fatalf("history: %d %s", code, body)
		}
		if strings.Contains(body, "Segredo da outra empresa") || strings.Contains(body, "outra empresa") {
			t.Errorf("another instance's memory leaked: %s", body)
		}
		// the conversation-based route answers the same
		code, body = do("GET", tenantPath("A")+"/inbox/conversations/"+w.conv["A"].String()+"/attendance-context", "", reader, hub)
		if code != 200 || !strings.Contains(body, "segunda via do boleto") {
			t.Errorf("attendance-context: %d %s", code, body)
		}
		// another instance's contact under this instance's URL: the same empty answer a member would get
		code, body = do("GET", historyOf("A", contactB), "", reader, hub)
		if code != 200 || strings.Contains(body, "outra empresa") || strings.Contains(body, "Segredo") {
			t.Errorf("another instance's contact under this URL: %d %s", code, body)
		}
		// and a conversation of another instance is not there
		if code, _ := do("GET", tenantPath("A")+"/inbox/conversations/"+w.conv["B"].String()+"/attendance-context", "", reader, hub); code != 404 {
			t.Errorf("another instance's conversation under this URL: %d, want 404", code)
		}
	})

	t.Run("without the key: the route refuses, and the database shows nothing even to a person who is also a member", func(t *testing.T) {
		if code, _ := do("GET", historyOf("A", contactA), "", blind, hub); code != 403 {
			t.Errorf("history without attendance.history: %d, want 403", code)
		}
		if code, _ := do("GET", tenantPath("A")+"/inbox/conversations/"+w.conv["A"].String()+"/attendance-context", "", blind, hub); code != 403 {
			t.Errorf("attendance-context without attendance.history: %d, want 403", code)
		}
		w.attend(both, "A", func(ctx context.Context, q platformdb.Querier) {
			var n, m int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM conversation_closures`).Scan(&n))
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM follow_up_items`).Scan(&m))
			if n != 0 || m != 0 {
				t.Errorf("acting as an agent without the key (and as a member too): %d closures, %d items visible, want none", n, m)
			}
		})
		// the same person as a member (no acting header) reads it as always
		if code, body := do("GET", historyOf("A", contactA), "", both, ""); code != 200 || !strings.Contains(body, "segunda via do boleto") {
			t.Errorf("the member's own route: %d %s", code, body)
		}
	})

	t.Run("the contract's queue scope narrows what the contact shows", func(t *testing.T) {
		quiet := uuid.New()
		w.exec(`INSERT INTO queues (id, tenant_id, name) VALUES ($1, $2, 'sem-conversas')`, quiet, w.tenant["A"])
		w.queueScope("A", quiet)
		t.Cleanup(func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
		})
		code, body := do("GET", historyOf("A", contactA), "", reader, hub)
		if code != 200 || strings.Contains(body, "segunda via") || strings.Contains(body, "ligar para confirmar") {
			t.Errorf("a contact without a conversation in the contract's queues shows its memory: %d %s", code, body)
		}
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
	})

	t.Run("a revoked grant is a 404", func(t *testing.T) {
		g := w.ownerOne(`SELECT id::text FROM effective_access_grants WHERE user_id = $1 AND tenant_id = $2`, reader, w.tenant["A"])
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		if code, _ := do("GET", historyOf("A", contactA), "", reader, hub); code != 404 {
			t.Errorf("revoked grant: %d, want 404", code)
		}
		w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g)
	})

	t.Run("the search over earlier messages stays closed to a delegate", func(t *testing.T) {
		code, _ := do("GET", tenantPath("A")+"/inbox/conversations/"+w.conv["A"].String()+"/history-search?q=boleto", "", resolver, hub)
		if code != 404 && code != 403 {
			t.Errorf("history-search by a delegate: %d, want a refusal", code)
		}
	})

	t.Run("resolving: both keys, an open item, the agent signs it, one audit row naming the hub and never the text", func(t *testing.T) {
		path := func(id uuid.UUID) string { return tenantPath("A") + "/follow-ups/" + id.String() + "/resolve" }
		// the key without the sight: an UPDATE reads what it changes
		if code, _ := do("POST", path(toResolve), `{"status":"done"}`, resolveOnly, hub); code != 404 && code != 403 {
			t.Errorf("followup.resolve without attendance.history: %d, want a refusal", code)
		}
		// the sight without the key
		if code, _ := do("POST", path(toResolve), `{"status":"done"}`, reader, hub); code != 403 {
			t.Errorf("attendance.history without followup.resolve: %d, want 403", code)
		}
		// a person who is also a member does not resolve as a member while acting as an agent without the key
		if code, _ := do("POST", path(toResolve), `{"status":"done"}`, both, hub); code != 403 {
			t.Errorf("a member acting as an agent without the key: %d, want 403", code)
		}
		if st := w.ownerOne(`SELECT status FROM follow_up_items WHERE id = $1`, toResolve); st != "open" {
			t.Fatalf("a refused request changed the item: %s", st)
		}
		// the walls of the body
		for name, b := range map[string]string{
			"a tenant smuggled in": `{"status":"done","tenant_id":"` + w.tenant["B"].String() + `"}`,
			"an unknown status":    `{"status":"open"}`,
		} {
			if code, _ := do("POST", path(toResolve), b, resolver, hub); code != 400 {
				t.Errorf("%s: %d, want 400", name, code)
			}
		}
		// another instance's item
		if code, _ := do("POST", path(itemB), `{"status":"done"}`, resolver, hub); code != 404 {
			t.Errorf("another instance's item: %d, want 404", code)
		}
		if st := w.ownerOne(`SELECT status FROM follow_up_items WHERE id = $1`, itemB); st != "open" {
			t.Fatalf("another instance's item changed: %s", st)
		}
		// the one that works
		code, body := do("POST", path(toResolve), `{"status":"done","note":"boleto enviado"}`, resolver, hub)
		if code != 200 || !strings.Contains(body, `"status":"done"`) {
			t.Fatalf("resolve: %d %s", code, body)
		}
		if got := w.ownerOne(`SELECT status || '|' || resolved_by_user_id::text FROM follow_up_items WHERE id = $1`, toResolve); got != "done|"+resolver.String() {
			t.Errorf("item = %q, want done signed by the agent", got)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'followup.resolved' AND metadata->>'acting_as' = $3`, w.tenant["A"], resolver, hub); n != 1 {
			t.Errorf("audit rows = %d, want 1 naming the hub", n)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND (metadata::text LIKE '%boleto%' OR metadata::text LIKE '%enviado%')`, w.tenant["A"]); n != 0 {
			t.Error("the text of the item or of the resolution reached the audit trail")
		}
		// resolved items leave the open list
		_, listed := do("GET", historyOf("A", contactA), "", reader, hub)
		var hist struct {
			Open []struct {
				Text string `json:"text"`
			} `json:"open_follow_ups"`
		}
		if err := json.Unmarshal([]byte(listed), &hist); err != nil {
			t.Fatalf("history body: %v: %s", err, listed)
		}
		for _, o := range hist.Open {
			if strings.Contains(o.Text, "enviar o boleto") {
				t.Errorf("a resolved item is still listed as open: %s", listed)
			}
		}
		if len(hist.Open) != 1 {
			t.Errorf("open items = %d, want only the one still open", len(hist.Open))
		}
		// resolving it again is refused and changes nothing
		if code, _ := do("POST", path(toResolve), `{"status":"dropped"}`, resolver, hub); code != 409 && code != 404 {
			t.Errorf("resolving twice: %d, want 409 or 404", code)
		}
		if st := w.ownerOne(`SELECT status FROM follow_up_items WHERE id = $1`, toResolve); st != "done" {
			t.Errorf("the second resolution changed the item: %s", st)
		}
	})

	t.Run("a member keeps the original routes", func(t *testing.T) {
		if code, body := do("POST", tenantPath("A")+"/follow-ups/"+openItem.String()+"/resolve", `{"status":"dropped"}`, member, ""); code != 200 || !strings.Contains(body, `"status":"dropped"`) {
			t.Errorf("member resolve: %d %s", code, body)
		}
		if got := w.ownerOne(`SELECT resolved_by_user_id::text FROM follow_up_items WHERE id = $1`, openItem); got != member.String() {
			t.Errorf("resolved by %s, want the member", got)
		}
	})

	t.Run("the database: a delegate never inserts, and a resolution must be signed by the person and be done/dropped", func(t *testing.T) {
		fresh := item("A", "A2", contactA, c1, "pendência nova")
		w.attend(resolver, "A", func(ctx context.Context, q platformdb.Querier) {
			if err := w.try(ctx, q, `INSERT INTO follow_up_items (tenant_id, contact_id, conversation_id, kind, text) VALUES ($1,$2,$3,'pending','forjado')`, w.tenant["A"], contactA, w.conv["A"]); err == nil {
				t.Error("a delegate inserted a follow-up")
			}
			if err := w.try(ctx, q, `INSERT INTO conversation_closures (tenant_id, conversation_id, contact_id, source, reason) VALUES ($1,$2,$3,'agent','resolved')`, w.tenant["A"], w.conv["A"], contactA); err == nil {
				t.Error("a delegate inserted a closure")
			}
			// not signed by themselves
			if changed(ctx, q, w, `UPDATE follow_up_items SET status='done', resolved_at=now(), resolved_by_user_id=$2 WHERE id=$1`, fresh, member) {
				t.Error("a delegate resolved an item in somebody else's name")
			}
		})
		if st := w.ownerOne(`SELECT status FROM follow_up_items WHERE id = $1`, fresh); st != "open" {
			t.Errorf("the item changed: %s", st)
		}
		w.attend(reader, "A", func(ctx context.Context, q platformdb.Querier) {
			if changed(ctx, q, w, `UPDATE follow_up_items SET status='done', resolved_at=now(), resolved_by_user_id=$2 WHERE id=$1`, fresh, reader) {
				t.Error("a reader without followup.resolve resolved an item directly")
			}
		})
		// a member who is also an agent still sees the item with the key (the sight does not depend on being a member)
		w.attend(memberReader, "A", func(ctx context.Context, q platformdb.Querier) {
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM follow_up_items WHERE id = $1`, fresh).Scan(&n))
			if n != 1 {
				t.Fatalf("the member-agent with attendance.history does not see the item (%d)", n)
			}
		})
		// even with the platform-administrator flag on in the session, the acting context keeps to the key and to one instance: the member policies of these tables
		// (has_active_membership OR is_system_admin) would otherwise open them (has_active_membership alone is already false in the acting context, migration 109)
		// (the flag is on from the first statement of the transaction: a session context is accepted once per transaction, so it cannot be raised half-way)
		w.must(platformdb.WithTenantSession(w.ctx, w.app, uuid.Nil, true, func(c context.Context) error {
			ctx, q := c, platformdb.QuerierFromContext(c, w.app)
			if !w.enter(ctx, q, reader, "A") {
				t.Fatal("could not enter the delegated context as a platform administrator session")
			}
			var n, m int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM conversation_closures WHERE tenant_id = $1`, w.tenant["B"]).Scan(&n))
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM follow_up_items WHERE tenant_id = $1`, w.tenant["B"]).Scan(&m))
			if n != 0 || m != 0 {
				t.Errorf("with the administrator flag the context of A shows %d closures and %d items of B", n, m)
			}
			if changed(ctx, q, w, `UPDATE follow_up_items SET status='done', resolved_at=now(), resolved_by_user_id=$2 WHERE id=$1`, fresh, reader) {
				t.Error("with the administrator flag a reader resolved an item without followup.resolve")
			}
			if err := w.try(ctx, q, `INSERT INTO follow_up_items (tenant_id, contact_id, conversation_id, kind, text) VALUES ($1,$2,$3,'pending','forjado')`, w.tenant["A"], contactA, w.conv["A"]); err == nil {
				t.Error("with the administrator flag a delegate inserted a follow-up")
			}
			if err := w.try(ctx, q, `INSERT INTO conversation_closures (tenant_id, conversation_id, contact_id, source, reason) VALUES ($1,$2,$3,'agent','resolved')`, w.tenant["A"], w.conv["A"], contactA); err == nil {
				t.Error("with the administrator flag a delegate inserted a closure")
			}
			if changed(ctx, q, w, `DELETE FROM follow_up_items WHERE id = $1`, fresh) {
				t.Error("with the administrator flag a delegate deleted an item")
			}
			return nil
		}))
		if n := w.count(`SELECT count(*) FROM follow_up_items WHERE id = $1`, fresh); n != 1 {
			t.Error("the item was deleted")
		}
		// an agent who may resolve does not edit an item either: the only change a delegate can make is open -> done/dropped
		w.attend(resolver, "A", func(ctx context.Context, q platformdb.Querier) {
			if changed(ctx, q, w, `UPDATE follow_up_items SET text='reescrito' WHERE id=$1`, fresh) {
				t.Error("a delegate edited the text of an open item")
			}
			// resolving while rewriting anything else is refused too: only the resolution itself may change
			for name, set := range map[string]string{
				"the text":         `text='reescrito'`,
				"the contact":      `contact_id='` + contactB.String() + `'`,
				"the conversation": `conversation_id='` + w.conv["A0"].String() + `'`,
				"the owner":        `owner_user_id='` + resolver.String() + `'`,
				"the due date":     `due_at=now()`,
			} {
				if changed(ctx, q, w, `UPDATE follow_up_items SET status='done', resolved_at=now(), resolved_by_user_id=$2, `+set+` WHERE id=$1`, fresh, resolver) {
					t.Errorf("a delegate rewrote %s while resolving", name)
				}
			}
			// signing an open item is not a resolution: the item has to leave the open state
			if changed(ctx, q, w, `UPDATE follow_up_items SET resolved_by_user_id=$2 WHERE id=$1`, fresh, resolver) {
				t.Error("a delegate signed an item without resolving it")
			}
			if changed(ctx, q, w, `UPDATE follow_up_items SET text='reescrito', resolved_by_user_id=$2 WHERE id=$1`, fresh, resolver) {
				t.Error("a delegate edited an open item by signing it")
			}
		})
		if got := w.ownerOne(`SELECT text FROM follow_up_items WHERE id = $1`, fresh); got != "pendência nova" {
			t.Errorf("the open item changed: %s", got)
		}
		// one instance at a time: holding the key in both instances, the context of A shows nothing of B
		w.attend(reader, "A", func(ctx context.Context, q platformdb.Querier) {
			var n, m int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM conversation_closures WHERE tenant_id = $1`, w.tenant["B"]).Scan(&n))
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM follow_up_items WHERE tenant_id = $1`, w.tenant["B"]).Scan(&m))
			if n != 0 || m != 0 {
				t.Errorf("the context of A shows %d closures and %d items of B", n, m)
			}
		})
		// a resolved item is not rewritten, not even by an agent who holds both keys
		w.attend(resolver, "A", func(ctx context.Context, q platformdb.Querier) {
			if changed(ctx, q, w, `UPDATE follow_up_items SET status='dropped', resolved_at=now(), resolved_by_user_id=$2, resolution_note='reescrito' WHERE id=$1`, toResolve, resolver) {
				t.Error("a delegate rewrote an item that was already resolved")
			}
		})
		if got := w.ownerOne(`SELECT status || '|' || resolution_note FROM follow_up_items WHERE id = $1`, toResolve); got != "done|boleto enviado" {
			t.Errorf("the resolved item changed: %s", got)
		}
		if st := w.ownerOne(`SELECT status FROM follow_up_items WHERE id = $1`, fresh); st != "open" {
			t.Errorf("the item changed: %s", st)
		}
		if n := w.count(`SELECT count(*) FROM follow_up_items WHERE tenant_id = $1 AND text = 'forjado'`, w.tenant["A"]); n != 0 {
			t.Error("a forged item exists")
		}
	})
}

// changed runs one statement that is expected to be refused (an error or no row) inside a savepoint, and tells whether it changed a row.
func changed(ctx context.Context, q platformdb.Querier, w *world, sql string, args ...any) bool {
	_, err := q.Exec(ctx, "SAVEPOINT changed")
	w.must(err)
	tag, err := q.Exec(ctx, sql, args...)
	if err != nil {
		_, rerr := q.Exec(ctx, "ROLLBACK TO SAVEPOINT changed")
		w.must(rerr)
		return false
	}
	return tag.RowsAffected() != 0
}
