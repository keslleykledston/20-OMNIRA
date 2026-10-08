package adapters_test

// Real-PostgreSQL proof of Hub delegated access (migrations 093..095).
//
// Fixtures are written through the OWNER connection (superuser, bypasses RLS, exactly like seeding).
// Every assertion runs through the APPLICATION role (omnira_app, not superuser, FORCE RLS) using the
// same platformdb.WithTenantSession the HTTP layer uses, always with isSystemAdmin=false: no Hub agent
// in this file is ever a system admin. Nothing is mocked; a wrong policy makes these tests fail.
//
// Run with: scripts/test-integration.sh ./internal/hub/adapters

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
)

type world struct {
	t     *testing.T
	ctx   context.Context
	owner *pgxpool.Pool
	app   *pgxpool.Pool

	hub             uuid.UUID
	tenant          map[string]uuid.UUID // "A","B","C"
	contract        map[string]uuid.UUID
	queue1, queue2  map[string]uuid.UUID // per tenant (queues are tenant-owned)
	conv            map[string]uuid.UUID // "A","B","C" (queue1), "A2" (tenant A, queue2), "A0" (tenant A, no queue)
	roleHubAgent    uuid.UUID
	roleHubAdmin    uuid.UUID
	roleTenantAgent uuid.UUID
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	w := &world{t: t, ctx: ctx, owner: owner, app: app,
		tenant: map[string]uuid.UUID{}, contract: map[string]uuid.UUID{},
		queue1: map[string]uuid.UUID{}, queue2: map[string]uuid.UUID{}, conv: map[string]uuid.UUID{}}

	// The runtime role must be unable to bypass RLS, otherwise nothing below proves anything.
	var bypass bool
	w.must(app.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass))
	if bypass {
		t.Fatal("application role can bypass RLS; refusing to run")
	}

	w.roleHubAgent = w.role("hub_agent")
	w.roleHubAdmin = w.role("hub_admin")
	w.roleTenantAgent = w.role("tenant_agent")

	w.hub = uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, $2)`, w.hub, "K3G "+w.hub.String()[:8])

	for _, k := range []string{"A", "B", "C"} {
		tid := uuid.New()
		w.tenant[k] = tid
		w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, tid, "Tenant "+k+" "+tid.String()[:8])
		contact := uuid.New()
		w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, $3, $4)`,
			contact, tid, "Cliente "+k, fmt.Sprintf("+5511%09d", rand.Intn(1_000_000_000)))
		w.queue1[k], w.queue2[k] = uuid.New(), uuid.New()
		w.exec(`INSERT INTO queues (id, tenant_id, name) VALUES ($1, $2, 'q1'), ($3, $2, 'q2')`, w.queue1[k], tid, w.queue2[k])
		w.conv[k] = w.conversation(tid, contact, &[]uuid.UUID{w.queue1[k]}[0])
		if k == "A" {
			w.conv["A2"] = w.conversation(tid, contact, &[]uuid.UUID{w.queue2[k]}[0])
			w.conv["A0"] = w.conversation(tid, contact, nil)
		}
		w.contract[k] = uuid.New()
		w.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from) VALUES ($1, $2, $3, now() - interval '1 day')`,
			w.contract[k], w.hub, tid)
	}
	return w
}

func (w *world) conversation(tenant, contact uuid.UUID, queue *uuid.UUID) uuid.UUID {
	id := uuid.New()
	w.exec(`INSERT INTO conversations (id, tenant_id, contact_id, queue_id) VALUES ($1, $2, $3, $4)`, id, tenant, contact, queue)
	w.exec(`INSERT INTO messages (tenant_id, conversation_id, direction) VALUES ($1, $2, 'inbound')`, tenant, id)
	w.exec(`INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id, queue_id) VALUES ($1, $2, $3, $4)`, w.hub, tenant, id, queue)
	return id
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) exec(sql string, args ...any) {
	w.t.Helper()
	_, err := w.owner.Exec(w.ctx, sql, args...)
	w.must(err)
}

func (w *world) role(key string) uuid.UUID {
	var id uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = $1 LIMIT 1`, key).Scan(&id))
	return id
}

func (w *world) user(name string) uuid.UUID {
	id := uuid.New()
	w.exec(`INSERT INTO users (id, external_subject) VALUES ($1, $2)`, id, name+"-"+id.String())
	return id
}

func (w *world) hubAgent(name string) uuid.UUID {
	u := w.user(name)
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, u, w.roleHubAgent)
	return u
}

func (w *world) grant(user uuid.UUID, tenantKey string) uuid.UUID {
	id := uuid.New()
	w.exec(`INSERT INTO effective_access_grants (id, hub_id, user_id, tenant_id, service_contract_id, valid_from)
	        VALUES ($1, $2, $3, $4, $5, now() - interval '1 day')`, id, w.hub, user, w.tenant[tenantKey], w.contract[tenantKey])
	return id
}

func (w *world) directMember(user uuid.UUID, tenantKey string) {
	w.exec(`INSERT INTO memberships (tenant_id, user_id, role_id) VALUES ($1, $2, $3)`, w.tenant[tenantKey], user, w.roleTenantAgent)
}

// n runs a scalar COUNT as the given user through the application's session machinery (never system admin).
func (w *world) n(user uuid.UUID, sql string, args ...any) int {
	w.t.Helper()
	var n int
	err := platformdb.WithTenantSession(w.ctx, w.app, user, false, func(c context.Context) error {
		return platformdb.QuerierFromContext(c, w.app).QueryRow(c, sql, args...).Scan(&n)
	})
	w.must(err)
	return n
}

// write attempts a statement as the user and returns rows affected plus the error (RLS violations surface here).
func (w *world) write(user uuid.UUID, sql string, args ...any) (int64, error) {
	var affected int64
	err := platformdb.WithTenantSession(w.ctx, w.app, user, false, func(c context.Context) error {
		tag, err := platformdb.QuerierFromContext(c, w.app).Exec(c, sql, args...)
		affected = tag.RowsAffected()
		return err
	})
	return affected, err
}

// resources counts what the user can read for one tenant across the real tenant-owned resources.
type resources struct{ tenants, conversations, messages, inbox int }

func (w *world) reads(user uuid.UUID, tenantKey string) resources {
	tid := w.tenant[tenantKey]
	return resources{
		tenants:       w.n(user, `SELECT count(*) FROM tenants WHERE id = $1`, tid),
		conversations: w.n(user, `SELECT count(*) FROM conversations WHERE tenant_id = $1`, tid),
		messages:      w.n(user, `SELECT count(*) FROM messages WHERE tenant_id = $1`, tid),
		inbox:         w.n(user, `SELECT count(*) FROM hub_inbox_items WHERE tenant_id = $1`, tid),
	}
}

func (w *world) expectFull(user uuid.UUID, tenantKey, why string) {
	w.t.Helper()
	r := w.reads(user, tenantKey)
	if r.tenants != 1 || r.conversations == 0 || r.messages == 0 || r.inbox == 0 {
		w.t.Errorf("%s: expected access to tenant %s, got %+v", why, tenantKey, r)
	}
}

func (w *world) expectNone(user uuid.UUID, tenantKey, why string) {
	w.t.Helper()
	if r := w.reads(user, tenantKey); r != (resources{}) {
		w.t.Errorf("%s: expected NO access to tenant %s, got %+v", why, tenantKey, r)
	}
}

func TestHubRLS_DirectAndDelegatedMatrix(t *testing.T) {
	w := newWorld(t)
	alice := w.hubAgent("alice")
	w.grant(alice, "A")
	w.grant(alice, "B")
	bob := w.hubAgent("bob")
	w.grant(bob, "B")
	noGrant := w.hubAgent("nogrant")
	carol := w.user("carol")
	w.directMember(carol, "A")
	stranger := w.user("stranger")

	t.Run("RLS-001 direct tenant member reads own tenant", func(t *testing.T) {
		w.expectFull(carol, "A", "carol")
	})
	t.Run("RLS-002 direct member of A does not read B or C", func(t *testing.T) {
		w.expectNone(carol, "B", "carol")
		w.expectNone(carol, "C", "carol")
	})
	t.Run("RLS-003 Alice reads A through the Hub", func(t *testing.T) { w.expectFull(alice, "A", "alice") })
	t.Run("RLS-004 Alice reads B through the Hub", func(t *testing.T) { w.expectFull(alice, "B", "alice") })
	t.Run("RLS-005 Alice does not read C", func(t *testing.T) { w.expectNone(alice, "C", "alice") })
	t.Run("RLS-006 Bob reads B and not A", func(t *testing.T) {
		w.expectFull(bob, "B", "bob")
		w.expectNone(bob, "A", "bob")
		w.expectNone(bob, "C", "bob")
	})
	t.Run("RLS-007 Hub membership without a grant gives nothing", func(t *testing.T) {
		for _, k := range []string{"A", "B", "C"} {
			w.expectNone(noGrant, k, "nogrant")
		}
	})
	t.Run("a user with no relation to any tenant or hub reads nothing", func(t *testing.T) {
		for _, k := range []string{"A", "B", "C"} {
			w.expectNone(stranger, k, "stranger")
		}
	})
	t.Run("RLS-012 no Hub agent here is a system admin, and the session says so", func(t *testing.T) {
		if got := w.n(alice, `SELECT CASE WHEN is_system_admin() THEN 1 ELSE 0 END`); got != 0 {
			t.Fatal("alice's session is a system-admin session")
		}
		if got := w.n(alice, `SELECT CASE WHEN current_user_id() = $1 THEN 1 ELSE 0 END`, alice); got != 1 {
			t.Fatal("session user is not alice")
		}
	})
	t.Run("IDOR: a known resource UUID of an ungranted tenant is not readable", func(t *testing.T) {
		if got := w.n(alice, `SELECT count(*) FROM conversations WHERE id = $1`, w.conv["C"]); got != 0 {
			t.Fatalf("alice read conversation of tenant C by id: %d", got)
		}
		if got := w.n(alice, `SELECT count(*) FROM messages WHERE conversation_id = $1`, w.conv["C"]); got != 0 {
			t.Fatalf("alice read messages of tenant C by conversation id: %d", got)
		}
		if got := w.n(bob, `SELECT count(*) FROM conversations WHERE id = $1`, w.conv["A"]); got != 0 {
			t.Fatalf("bob read conversation of tenant A by id: %d", got)
		}
	})
	t.Run("delegated access is read-only: no insert/update/delete on tenant resources", func(t *testing.T) {
		contact := uuid.New()
		w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1,$2,'x',$3)`, contact, w.tenant["A"], fmt.Sprintf("+5511%09d", rand.Intn(1_000_000_000)))
		if _, err := w.write(alice, `INSERT INTO conversations (tenant_id, contact_id) VALUES ($1, $2)`, w.tenant["A"], contact); err == nil {
			t.Error("alice inserted a conversation into tenant A through the Hub")
		}
		if n, _ := w.write(alice, `UPDATE conversations SET status = 'closed' WHERE id = $1`, w.conv["A"]); n != 0 {
			t.Errorf("alice updated %d conversation row(s) of tenant A", n)
		}
		if n, _ := w.write(alice, `DELETE FROM messages WHERE tenant_id = $1`, w.tenant["A"]); n != 0 {
			t.Errorf("alice deleted %d message(s) of tenant A", n)
		}
		if n, _ := w.write(alice, `UPDATE hub_inbox_items SET status = 'closed' WHERE tenant_id = $1`, w.tenant["A"]); n != 0 {
			t.Errorf("alice updated %d inbox row(s) directly", n)
		}
	})
	t.Run("a Hub agent cannot widen their own access", func(t *testing.T) {
		if _, err := w.write(noGrant, `INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id)
		                               VALUES ($1, $2, $3, $4)`, w.hub, noGrant, w.tenant["A"], w.contract["A"]); err == nil {
			t.Error("nogrant created their own grant")
		}
		if n, _ := w.write(bob, `UPDATE effective_access_grants SET tenant_id = tenant_id WHERE user_id = $1`, bob); n != 0 {
			t.Errorf("bob modified %d grant row(s)", n)
		}
		if _, err := w.write(alice, `INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, stranger, w.roleHubAdmin); err == nil {
			t.Error("alice added a hub member")
		}
	})
	t.Run("integration, credential and ticketing state stays invisible to a Hub agent", func(t *testing.T) {
		conn := uuid.New()
		w.exec(`INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id)
		        VALUES ($1, $2, 'whatsapp', 'waha', 'unofficial', $3)`, conn, w.tenant["A"], "n-"+conn.String())
		w.exec(`INSERT INTO channel_credentials (tenant_id, connection_id, ciphertext) VALUES ($1, $2, '\x00'::bytea)`, w.tenant["A"], conn)
		w.exec(`INSERT INTO tenant_ai_integrations (tenant_id, provider) VALUES ($1, 'gemini')`, w.tenant["A"])
		w.exec(`INSERT INTO ticket_external_create_attempts (tenant_id, conversation_id, actor_user_id, idempotency_key, request_hash)
		        VALUES ($1, $2, $3, $4, 'h')`, w.tenant["A"], w.conv["A"], alice, "idem-"+uuid.NewString())
		// control: the owner sees the rows, so the zeros below mean "hidden", not "never inserted"
		for _, tbl := range []string{"channel_connections", "channel_credentials", "tenant_ai_integrations", "ticket_external_create_attempts", "contacts"} {
			var seen int
			w.must(w.owner.QueryRow(w.ctx, `SELECT count(*) FROM `+tbl+` WHERE tenant_id = $1`, w.tenant["A"]).Scan(&seen))
			if seen == 0 {
				t.Fatalf("control failed: no %s row exists for tenant A", tbl)
			}
			if got := w.n(alice, `SELECT count(*) FROM `+tbl+` WHERE tenant_id = $1`, w.tenant["A"]); got != 0 {
				t.Errorf("alice (granted on A) can read %d row(s) of %s", got, tbl)
			}
		}
		// regression guard: direct members keep the access the tenant policies already gave them
		if got := w.n(carol, `SELECT count(*) FROM contacts WHERE tenant_id = $1`, w.tenant["A"]); got == 0 {
			t.Error("carol (direct member of A) lost access to contacts")
		}
	})
	t.Run("the Hub tables themselves can be queried without recursion errors", func(t *testing.T) {
		for _, tbl := range []string{"service_hubs", "hub_memberships", "hub_tenant_service_contracts", "work_pools",
			"work_pool_members", "skills", "agent_skills", "effective_access_grants", "hub_inbox_items"} {
			for name, u := range map[string]uuid.UUID{"alice": alice, "nogrant": noGrant, "stranger": stranger} {
				w.n(u, `SELECT count(*) FROM `+tbl) // w.n fails the test on any SQL error, incl. 42P17
				_ = name
			}
		}
	})
	t.Run("visibility of the Hub's own bookkeeping is least-privilege", func(t *testing.T) {
		if got := w.n(alice, `SELECT count(*) FROM effective_access_grants`); got != 2 {
			t.Errorf("alice sees %d grants, want only her own 2", got)
		}
		if got := w.n(alice, `SELECT count(*) FROM hub_tenant_service_contracts`); got != 2 {
			t.Errorf("alice sees %d contracts, want only the 2 behind her grants (not tenant C's)", got)
		}
		if got := w.n(noGrant, `SELECT count(*) FROM hub_tenant_service_contracts`); got != 0 {
			t.Errorf("an agent without grants sees %d contracts", got)
		}
		if got := w.n(alice, `SELECT count(*) FROM hub_memberships`); got != 1 {
			t.Errorf("alice sees %d hub memberships, want only her own", got)
		}
		if got := w.n(stranger, `SELECT count(*) FROM service_hubs`); got != 0 {
			t.Errorf("a stranger sees %d hubs", got)
		}
		admin := w.user("hubadmin")
		w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, admin, w.roleHubAdmin)
		if got := w.n(admin, `SELECT count(*) FROM hub_tenant_service_contracts`); got != 3 {
			t.Errorf("hub admin sees %d contracts, want all 3", got)
		}
		if got := w.n(admin, `SELECT count(*) FROM effective_access_grants`); got != 3 {
			t.Errorf("hub admin sees %d grants, want all 3", got)
		}
		w.expectNone(admin, "A", "a hub_admin role alone carries no tenant data access")
	})
}

func TestHubRLS_RevocationAndValidity(t *testing.T) {
	w := newWorld(t)

	t.Run("RLS-008 expired grant gives no access, immediately", func(t *testing.T) {
		u := w.hubAgent("expiring")
		g := w.grant(u, "A")
		w.expectFull(u, "A", "before expiry")
		w.exec(`UPDATE effective_access_grants SET valid_from = now() - interval '2 hours', valid_until = now() - interval '1 hour' WHERE id = $1`, g)
		w.expectNone(u, "A", "expired grant")
	})
	t.Run("a grant that has not started yet gives no access", func(t *testing.T) {
		u := w.hubAgent("future")
		w.exec(`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, valid_from)
		        VALUES ($1, $2, $3, $4, now() + interval '1 hour')`, w.hub, u, w.tenant["A"], w.contract["A"])
		w.expectNone(u, "A", "future grant")
	})
	t.Run("RLS-009 revoked and suspended grants give no access, immediately", func(t *testing.T) {
		for _, status := range []string{"revoked", "suspended"} {
			u := w.hubAgent(status)
			g := w.grant(u, "B")
			w.expectFull(u, "B", "before "+status)
			w.exec(`UPDATE effective_access_grants SET status = $2 WHERE id = $1`, g, status)
			w.expectNone(u, "B", status+" grant")
		}
	})
	t.Run("leaving the Hub removes the delegated access", func(t *testing.T) {
		u := w.hubAgent("leaver")
		w.grant(u, "A")
		w.expectFull(u, "A", "while member")
		w.exec(`DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, w.hub, u)
		w.expectNone(u, "A", "after leaving the hub")
		var left int
		w.must(w.owner.QueryRow(w.ctx, `SELECT count(*) FROM effective_access_grants WHERE user_id = $1`, u).Scan(&left))
		if left != 0 {
			t.Errorf("%d grant row(s) outlived the hub membership (FK cascade missing)", left)
		}
	})
	t.Run("RLS-010 a revoked, suspended or expired service contract invalidates every grant behind it", func(t *testing.T) {
		for _, tc := range []struct{ name, set string }{
			{"revoked", `status = 'revoked'`},
			{"suspended", `status = 'suspended'`},
			{"expired", `valid_from = now() - interval '2 days', valid_until = now() - interval '1 day'`},
		} {
			u1, u2 := w.hubAgent("c1-"+tc.name), w.hubAgent("c2-"+tc.name)
			w.grant(u1, "C")
			w.grant(u2, "C")
			w.expectFull(u1, "C", "before contract "+tc.name)
			w.exec(`UPDATE hub_tenant_service_contracts SET `+tc.set+` WHERE id = $1`, w.contract["C"])
			w.expectNone(u1, "C", "contract "+tc.name)
			w.expectNone(u2, "C", "contract "+tc.name)
			w.exec(`UPDATE hub_tenant_service_contracts SET status = 'active', valid_from = now() - interval '3 days', valid_until = NULL WHERE id = $1`, w.contract["C"])
			w.expectFull(u1, "C", "contract restored")
		}
	})
	t.Run("a suspended hub gives no delegated access", func(t *testing.T) {
		u := w.hubAgent("hubsusp")
		w.grant(u, "B")
		w.expectFull(u, "B", "active hub")
		w.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, w.hub)
		w.expectNone(u, "B", "suspended hub")
		w.exec(`UPDATE service_hubs SET status = 'active' WHERE id = $1`, w.hub)
		w.expectFull(u, "B", "hub reactivated")
	})
	t.Run("direct tenant membership is unaffected by Hub revocations", func(t *testing.T) {
		u := w.user("direct-and-hub")
		w.directMember(u, "A")
		w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, u, w.roleHubAgent)
		g := w.grant(u, "A")
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		w.expectFull(u, "A", "direct membership must survive a revoked hub grant")
	})
}

func TestHubRLS_QueueScope(t *testing.T) {
	w := newWorld(t)
	scoped := w.hubAgent("scoped")
	w.grant(scoped, "A")
	msgsQ1 := func() int {
		return w.n(scoped, `SELECT count(*) FROM messages WHERE conversation_id = $1 AND tenant_id = $2`, w.conv["A"], w.tenant["A"])
	}
	convs := func() (q1, q2, noQueue, msgQ2, inboxQ2 int) {
		a := w.tenant["A"]
		return w.n(scoped, `SELECT count(*) FROM conversations WHERE id = $1`, w.conv["A"]),
			w.n(scoped, `SELECT count(*) FROM conversations WHERE id = $1`, w.conv["A2"]),
			w.n(scoped, `SELECT count(*) FROM conversations WHERE id = $1`, w.conv["A0"]),
			w.n(scoped, `SELECT count(*) FROM messages WHERE conversation_id = $1 AND tenant_id = $2`, w.conv["A2"], a),
			w.n(scoped, `SELECT count(*) FROM hub_inbox_items WHERE conversation_id = $1`, w.conv["A2"])
	}
	setScope := func(json string) {
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, w.contract["A"], json)
	}

	t.Run("no queue_ids key means every queue, including a queue-less conversation", func(t *testing.T) {
		q1, q2, none, m, i := convs()
		if q1 != 1 || q2 != 1 || none != 1 || m != 1 || i != 1 {
			t.Fatalf("unrestricted contract should expose all: q1=%d q2=%d none=%d msgQ2=%d inboxQ2=%d", q1, q2, none, m, i)
		}
	})
	t.Run("RLS-011 an allowlist exposes only its queues for conversations, messages and inbox", func(t *testing.T) {
		setScope(fmt.Sprintf(`{"queue_ids": ["%s"]}`, w.queue1["A"]))
		q1, q2, none, m, i := convs()
		if q1 != 1 {
			t.Errorf("allowed queue conversation not visible")
		}
		if got := msgsQ1(); got != 1 {
			t.Errorf("messages of the ALLOWED queue are hidden by a queue-restricted contract: %d", got)
		}
		if got := w.n(scoped, `SELECT count(*) FROM tenants WHERE id = $1`, w.tenant["A"]); got != 1 {
			t.Errorf("the tenant row itself must stay visible under a queue-restricted contract: %d", got)
		}
		if q2 != 0 || none != 0 || m != 0 || i != 0 {
			t.Errorf("out-of-scope data leaked: q2=%d noQueue=%d msgQ2=%d inboxQ2=%d", q2, none, m, i)
		}
	})
	t.Run("an empty allowlist means no queue at all", func(t *testing.T) {
		setScope(`{"queue_ids": []}`)
		if q1, q2, none, _, _ := convs(); q1+q2+none != 0 {
			t.Errorf("empty allowlist exposed conversations: %d %d %d", q1, q2, none)
		}
	})
	t.Run("a malformed scope fails closed", func(t *testing.T) {
		for _, bad := range []string{`{"queue_ids": "everything"}`, `{"queue_ids": {"a": 1}}`, `{"queue_ids": 7}`} {
			setScope(bad)
			if q1, q2, none, _, _ := convs(); q1+q2+none != 0 {
				t.Errorf("malformed scope %s exposed data", bad)
			}
		}
	})
	t.Run("another tenant's queue id in the allowlist does not grant this tenant's queue", func(t *testing.T) {
		setScope(fmt.Sprintf(`{"queue_ids": ["%s"]}`, w.queue1["B"]))
		if q1, _, _, _, _ := convs(); q1 != 0 {
			t.Error("a queue id from tenant B opened a queue of tenant A")
		}
	})
}

func TestHubRLS_RelationalIntegrity(t *testing.T) {
	w := newWorld(t)
	member := w.hubAgent("member")
	outsider := w.user("outsider-not-in-hub")

	// a second hub with a real pool, and a fresh tenant with no contract yet
	hub2, pool2, tenantD := uuid.New(), uuid.New(), uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'other hub')`, hub2)
	w.exec(`INSERT INTO work_pools (id, hub_id, name) VALUES ($1, $2, 'other pool')`, pool2, hub2)
	w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, tenantD, "Tenant D "+tenantD.String()[:8])

	expectFail := func(name, wantKind, sql string, args ...any) {
		t.Run(name, func(t *testing.T) {
			_, err := w.owner.Exec(w.ctx, sql, args...)
			if err == nil {
				t.Fatal("statement succeeded; the constraint is missing")
			}
			if !strings.Contains(err.Error(), wantKind) {
				t.Fatalf("failed for the wrong reason (want %q): %v", wantKind, err)
			}
		})
	}
	expectFail("a grant cannot point at a contract of another tenant", "foreign key",
		`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id) VALUES ($1,$2,$3,$4)`,
		w.hub, member, w.tenant["C"], w.contract["A"])
	expectFail("a grant cannot be issued to a user who is not a member of the hub", "foreign key",
		`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id) VALUES ($1,$2,$3,$4)`,
		w.hub, outsider, w.tenant["A"], w.contract["A"])
	expectFail("a grant cannot use a work pool of a different hub", "foreign key",
		`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, work_pool_id) VALUES ($1,$2,$3,$4,$5)`,
		w.hub, member, w.tenant["A"], w.contract["A"], pool2)
	expectFail("an inbox item cannot point at another tenant's conversation", "foreign key",
		`INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id) VALUES ($1,$2,$3)`,
		w.hub, w.tenant["B"], w.conv["A"])
	expectFail("a contract cannot end before it starts", "check constraint",
		`INSERT INTO hub_tenant_service_contracts (hub_id, tenant_id, valid_from, valid_until) VALUES ($1,$2, now(), now() - interval '1 day')`,
		w.hub, tenantD)
	expectFail("a grant cannot end before it starts", "check constraint",
		`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, valid_from, valid_until)
		 VALUES ($1,$2,$3,$4, now(), now() - interval '1 day')`, w.hub, member, w.tenant["A"], w.contract["A"])
}
