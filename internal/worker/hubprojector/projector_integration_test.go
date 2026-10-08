package hubprojector_test

// Real-PostgreSQL proof of the Hub inbox projector. Fixtures go through the owner connection (superuser, like
// seeding); the projector runs through the application pool (omnira_app, FORCE RLS) exactly as the worker does,
// and access to the projected rows is checked through the application role with the user's own session.

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
	"github.com/omnira/omnira/internal/worker/hubprojector"
)

type fx struct {
	t            *testing.T
	ctx          context.Context
	owner        *pgxpool.Pool
	app          *pgxpool.Pool
	proj         *hubprojector.Projector
	hub          uuid.UUID
	roleHubAgent uuid.UUID
	pairs        [][2]uuid.UUID // the (hub, tenant) pairs THIS test created: tests never rely on other tests' data
}

type tenantFx struct {
	id, contact, queue1, queue2, conn, contract uuid.UUID
	name                                        string
}

func newFx(t *testing.T) *fx {
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
	f := &fx{t: t, ctx: ctx, owner: owner, app: app, proj: hubprojector.New(app)}
	f.hub = uuid.New()
	f.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, $2)`, f.hub, "K3G "+f.hub.String()[:8])
	f.must(owner.QueryRow(ctx, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = 'hub_agent' LIMIT 1`).Scan(&f.roleHubAgent))
	return f
}

func (f *fx) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.owner.Exec(f.ctx, sql, args...)
	f.must(err)
}

// tenant creates a tenant with a contact, two queues, a channel connection, and (when hubID != Nil) a live contract.
func (f *fx) tenant(name string, hubID uuid.UUID) tenantFx {
	t := tenantFx{name: name, id: uuid.New(), contact: uuid.New(), queue1: uuid.New(), queue2: uuid.New(), conn: uuid.New(), contract: uuid.New()}
	f.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, t.id, name+" "+t.id.String()[:8])
	f.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, $3, $4)`, t.contact, t.id, "Cliente "+name, fmt.Sprintf("+5511%09d", rand.Intn(1_000_000_000)))
	f.exec(`INSERT INTO queues (id, tenant_id, name) VALUES ($1, $2, 'q1'), ($3, $2, 'q2')`, t.queue1, t.id, t.queue2)
	f.exec(`INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id) VALUES ($1, $2, 'whatsapp', 'waha', 'unofficial', $3)`, t.conn, t.id, "n-"+t.conn.String())
	if hubID != uuid.Nil {
		f.pairs = append(f.pairs, [2]uuid.UUID{hubID, t.id})
		f.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from) VALUES ($1, $2, $3, now() - interval '1 day')`, t.contract, hubID, t.id)
	}
	return t
}

func (f *fx) conversation(t tenantFx, queue *uuid.UUID) uuid.UUID {
	// one open conversation per (contact, channel) is a schema rule, so every conversation gets its own contact
	id, contact := uuid.New(), uuid.New()
	f.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, $3, $4)`, contact, t.id, "Cliente "+t.name, fmt.Sprintf("+5511%09d", rand.Intn(1_000_000_000)))
	f.exec(`INSERT INTO conversations (id, tenant_id, contact_id, queue_id, channel_connection_id) VALUES ($1, $2, $3, $4, $5)`, id, t.id, contact, queue, t.conn)
	return id
}

func (f *fx) message(t tenantFx, conv uuid.UUID, direction string, ago time.Duration) {
	status := "received"
	if direction == "outbound" {
		status = "sent"
	}
	f.exec(`INSERT INTO messages (tenant_id, conversation_id, direction, status, created_at) VALUES ($1, $2, $3, $4, now() - $5::interval)`,
		t.id, conv, direction, status, fmt.Sprintf("%d seconds", int(ago.Seconds())))
}

type item struct {
	Queue            *uuid.UUID
	Assigned         *uuid.UUID
	Customer, Chan   string
	Status, Priority string
	Unread           int
	Version          int64
}

func (f *fx) item(hub uuid.UUID, tenant uuid.UUID, conv uuid.UUID) (*item, bool) {
	var it item
	err := f.owner.QueryRow(f.ctx, `SELECT queue_id, assigned_user_id, customer_name, channel, status, priority, unread_count, version
		FROM hub_inbox_items WHERE hub_id = $1 AND tenant_id = $2 AND conversation_id = $3`, hub, tenant, conv).
		Scan(&it.Queue, &it.Assigned, &it.Customer, &it.Chan, &it.Status, &it.Priority, &it.Unread, &it.Version)
	if err != nil {
		return nil, false
	}
	return &it, true
}

func (f *fx) count(hub, tenant uuid.UUID) int {
	var n int
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM hub_inbox_items WHERE hub_id = $1 AND tenant_id = $2`, hub, tenant).Scan(&n))
	return n
}

// reconcile runs the projector over this test's own pairs only (hermetic even on a dirty database).
func (f *fx) reconcile() hubprojector.Result {
	f.t.Helper()
	var total hubprojector.Result
	for _, pr := range f.pairs {
		res, err := f.proj.ReconcileTenant(f.ctx, pr[0], pr[1])
		f.must(err)
		total.Upserted += res.Upserted
		total.Removed += res.Removed
	}
	return total
}

func ptr(u uuid.UUID) *uuid.UUID { return &u }

func TestProjector_MirrorsTheConversationAndIsIdempotent(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A", f.hub)
	conv := f.conversation(a, ptr(a.queue1))
	f.message(a, conv, "inbound", 120*time.Second)
	f.message(a, conv, "inbound", 60*time.Second)
	f.exec(`INSERT INTO tickets (tenant_id, conversation_id, status, priority) VALUES ($1, $2, 'open', 'critical')`, a.id, conv)

	if res := f.reconcile(); res.Upserted != 1 || res.Removed != 0 {
		t.Fatalf("first run: %+v", res)
	}
	it, ok := f.item(f.hub, a.id, conv)
	if !ok {
		t.Fatal("conversation was not projected")
	}
	if it.Customer != "Cliente A" || it.Chan != "whatsapp" || it.Status != "open" || it.Priority != "urgent" || it.Unread != 2 || it.Queue == nil || *it.Queue != a.queue1 || it.Version != 1 {
		t.Fatalf("projection does not mirror the conversation: %+v", it)
	}
	var lastActivityAgo float64
	f.must(f.owner.QueryRow(f.ctx, `SELECT extract(epoch FROM now() - last_activity_at) FROM hub_inbox_items WHERE conversation_id = $1`, conv).Scan(&lastActivityAgo))
	if lastActivityAgo < 55 || lastActivityAgo > 70 {
		t.Fatalf("last_activity_at should be the newest message (~60s ago), got %.0fs", lastActivityAgo)
	}
	if res := f.reconcile(); res.Upserted != 0 || res.Removed != 0 {
		t.Fatalf("an idempotent run must change nothing, got %+v", res)
	}
	if it2, _ := f.item(f.hub, a.id, conv); it2.Version != 1 {
		t.Fatalf("an idempotent run bumped version to %d", it2.Version)
	}
	if got := f.count(f.hub, a.id); got != 1 {
		t.Fatalf("duplicate rows: %d", got)
	}
}

func TestProjector_FollowsChangesInTheSource(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A", f.hub)
	agent := f.hubAgent("agent")
	conv := f.conversation(a, ptr(a.queue1))
	f.message(a, conv, "inbound", 300*time.Second)
	f.reconcile()

	steps := []struct {
		name   string
		change func()
		check  func(*item) bool
	}{
		{"queue moves", func() { f.exec(`UPDATE conversations SET queue_id = $2 WHERE id = $1`, conv, a.queue2) }, func(i *item) bool { return i.Queue != nil && *i.Queue == a.queue2 }},
		{"assignment", func() { f.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, conv, agent) }, func(i *item) bool { return i.Assigned != nil && *i.Assigned == agent }},
		{"customer writes again", func() { f.message(a, conv, "inbound", 10*time.Second) }, func(i *item) bool { return i.Unread == 2 }},
		{"operator replies", func() { f.message(a, conv, "outbound", 5*time.Second) }, func(i *item) bool { return i.Unread == 0 }},
		{"ticket opens as high", func() {
			f.exec(`INSERT INTO tickets (tenant_id, conversation_id, status, priority) VALUES ($1, $2, 'open', 'high')`, a.id, conv)
		}, func(i *item) bool { return i.Priority == "high" }},
		{"conversation closes", func() { f.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, conv) }, func(i *item) bool { return i.Status == "closed" }},
	}
	last := int64(1)
	for _, s := range steps {
		s.change()
		if res := f.reconcile(); res.Upserted != 1 {
			t.Fatalf("%s: expected one upsert, got %+v", s.name, res)
		}
		it, _ := f.item(f.hub, a.id, conv)
		if !s.check(it) {
			t.Errorf("%s: projection not updated: %+v", s.name, it)
		}
		if it.Version <= last {
			t.Errorf("%s: version did not advance (%d -> %d)", s.name, last, it.Version)
		}
		last = it.Version
	}
}

func (f *fx) hubAgent(name string) uuid.UUID {
	u := uuid.New()
	f.exec(`INSERT INTO users (id, external_subject) VALUES ($1, $2)`, u, name+"-"+u.String())
	f.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, f.hub, u, f.roleHubAgent)
	return u
}

func TestProjector_Eligibility(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A", f.hub)
	open := f.conversation(a, ptr(a.queue1))
	recentClosed := f.conversation(a, ptr(a.queue1))
	oldClosed := f.conversation(a, ptr(a.queue1))
	f.exec(`UPDATE conversations SET status = 'closed' WHERE id = ANY($1)`, []uuid.UUID{recentClosed, oldClosed})
	f.message(a, recentClosed, "inbound", 2*24*time.Hour)
	f.message(a, oldClosed, "inbound", 90*24*time.Hour)
	// a staff-to-staff conversation must never reach the Hub
	staff := uuid.New()
	f.exec(`INSERT INTO users (id, external_subject) VALUES ($1, $2)`, staff, "staff-"+staff.String())
	f.exec(`INSERT INTO memberships (tenant_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE tenant_id IS NULL AND key = 'tenant_agent' LIMIT 1`, a.id, staff)
	internal := uuid.New()
	f.exec(`INSERT INTO conversations (id, tenant_id, contact_id, internal_user_id, conversation_kind, channel_connection_id) VALUES ($1, $2, NULL, $3, 'internal', $4)`, internal, a.id, staff, a.conn)

	f.reconcile()
	for name, want := range map[string]struct {
		conv uuid.UUID
		in   bool
	}{"open": {open, true}, "recently closed": {recentClosed, true}, "closed long ago": {oldClosed, false}, "internal staff chat": {internal, false}} {
		if _, got := f.item(f.hub, a.id, want.conv); got != want.in {
			t.Errorf("%s: projected=%v, want %v", name, got, want.in)
		}
	}
	// the window is configurable and an item that falls out of it is removed
	res, err := hubprojector.New(f.app).WithLookbackDays(1).ReconcileTenant(f.ctx, f.hub, a.id)
	f.must(err)
	if _, still := f.item(f.hub, a.id, recentClosed); still || res.Removed != 1 {
		t.Errorf("a closed conversation outside a 1-day window must be removed (removed=%d, still=%v)", res.Removed, still)
	}
}

func TestProjector_RelationshipLifecycle(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A", f.hub)
	conv := f.conversation(a, ptr(a.queue1))
	f.reconcile()
	present := func() bool { _, ok := f.item(f.hub, a.id, conv); return ok }
	if !present() {
		t.Fatal("setup: not projected")
	}
	cases := []struct{ name, set, undo string }{
		{"contract revoked", `UPDATE hub_tenant_service_contracts SET status = 'revoked' WHERE id = '` + a.contract.String() + `'`, `UPDATE hub_tenant_service_contracts SET status = 'active' WHERE id = '` + a.contract.String() + `'`},
		{"contract suspended", `UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = '` + a.contract.String() + `'`, `UPDATE hub_tenant_service_contracts SET status = 'active' WHERE id = '` + a.contract.String() + `'`},
		{"contract expired", `UPDATE hub_tenant_service_contracts SET valid_from = now() - interval '2 days', valid_until = now() - interval '1 day' WHERE id = '` + a.contract.String() + `'`, `UPDATE hub_tenant_service_contracts SET valid_until = NULL WHERE id = '` + a.contract.String() + `'`},
		{"hub suspended", `UPDATE service_hubs SET status = 'suspended' WHERE id = '` + f.hub.String() + `'`, `UPDATE service_hubs SET status = 'active' WHERE id = '` + f.hub.String() + `'`},
	}
	for _, c := range cases {
		f.exec(c.set)
		f.reconcile()
		if present() {
			t.Errorf("%s: personal data stayed in the Hub projection", c.name)
		}
		f.exec(c.undo)
		f.reconcile()
		if !present() {
			t.Errorf("%s: not projected again after the relationship was restored", c.name)
		}
	}
	// the contract row is deleted outright: the pair is still found through the rows it left behind
	f.exec(`DELETE FROM hub_tenant_service_contracts WHERE id = $1`, a.contract)
	if _, err := f.proj.ReconcileAll(f.ctx); err != nil { // the pair is found through the rows it left behind
		t.Fatal(err)
	}
	if present() {
		t.Error("orphaned rows survived the deletion of their contract")
	}
	// and a deleted conversation disappears with it
	f.exec(`INSERT INTO hub_tenant_service_contracts (hub_id, tenant_id, valid_from) VALUES ($1, $2, now() - interval '1 day')`, f.hub, a.id)
	f.reconcile()
	f.exec(`DELETE FROM messages WHERE conversation_id = $1`, conv)
	f.exec(`DELETE FROM conversations WHERE id = $1`, conv)
	if present() {
		t.Error("a deleted conversation left a projection row (FK cascade missing)")
	}
}

func TestProjector_TenantAndHubIsolation(t *testing.T) {
	f := newFx(t)
	hub2 := uuid.New()
	f.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'other hub')`, hub2)
	a := f.tenant("A", f.hub)    // served by hub 1 only
	b := f.tenant("B", hub2)     // served by hub 2 only
	c := f.tenant("C", uuid.Nil) // served by nobody
	ca, cb, cc := f.conversation(a, ptr(a.queue1)), f.conversation(b, ptr(b.queue1)), f.conversation(c, ptr(c.queue1))
	f.reconcile()

	if _, ok := f.item(f.hub, a.id, ca); !ok {
		t.Error("hub 1 lost its own tenant")
	}
	if _, ok := f.item(hub2, b.id, cb); !ok {
		t.Error("hub 2 lost its own tenant")
	}
	var wrong int
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM hub_inbox_items WHERE (hub_id = $1 AND tenant_id <> $2) OR (hub_id = $3 AND tenant_id <> $4) OR tenant_id = $5 OR conversation_id = $6`,
		f.hub, a.id, hub2, b.id, c.id, cc).Scan(&wrong))
	if wrong != 0 {
		t.Fatalf("%d row(s) crossed a hub/tenant boundary", wrong)
	}
	// asking explicitly to project a tenant a hub has no contract with writes nothing
	if res, err := f.proj.ReconcileTenant(f.ctx, f.hub, b.id); err != nil || res.Upserted != 0 {
		t.Fatalf("hub 1 projected tenant B without a contract: %+v err=%v", res, err)
	}
	if _, err := f.proj.ReconcileTenant(f.ctx, uuid.Nil, a.id); err == nil {
		t.Error("a nil hub id must be refused")
	}
	// a conversation id that belongs to another tenant never lands in a hub that does not serve it
	if res, err := f.proj.ProjectConversation(f.ctx, cc); err != nil || res.Upserted != 0 {
		t.Fatalf("a conversation of an unserved tenant was projected: %+v err=%v", res, err)
	}
}

func TestProjector_SingleConversationPath(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A", f.hub)
	c1, c2 := f.conversation(a, ptr(a.queue1)), f.conversation(a, ptr(a.queue1))
	res, err := f.proj.ProjectConversation(f.ctx, c1)
	f.must(err)
	if res.Upserted != 1 {
		t.Fatalf("expected exactly the one conversation, got %+v", res)
	}
	if _, ok := f.item(f.hub, a.id, c2); ok {
		t.Error("projecting one conversation also projected another")
	}
	if _, err := f.proj.ProjectConversation(f.ctx, uuid.Nil); err == nil {
		t.Error("a nil conversation id must be refused")
	}
}

// The projection only feeds the read model; who may SEE a row is still decided by RLS on the user's own session.
func TestProjector_RowsAreOnlyVisibleThroughALiveGrant(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A", f.hub)
	conv := f.conversation(a, ptr(a.queue1))
	f.message(a, conv, "inbound", 30*time.Second)
	f.reconcile()

	granted, member, stranger := f.hubAgent("granted"), f.hubAgent("member"), uuid.New()
	f.exec(`INSERT INTO users (id, external_subject) VALUES ($1, $2)`, stranger, "stranger-"+stranger.String())
	f.exec(`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, valid_from) VALUES ($1, $2, $3, $4, now() - interval '1 day')`, f.hub, granted, a.id, a.contract)

	see := func(u uuid.UUID) int {
		var n int
		f.must(platformdb.WithTenantSession(f.ctx, f.app, u, false, func(c context.Context) error {
			return platformdb.QuerierFromContext(c, f.app).QueryRow(c, `SELECT count(*) FROM hub_inbox_items`).Scan(&n)
		}))
		return n
	}
	if see(granted) != 1 || see(member) != 0 || see(stranger) != 0 {
		t.Fatalf("visibility wrong: granted=%d member(no grant)=%d stranger=%d", see(granted), see(member), see(stranger))
	}
	f.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1`, granted)
	if see(granted) != 0 {
		t.Error("a revoked grant still sees the projected row (the row exists, RLS must hide it)")
	}
	// the application role can neither write the projection nor read it unscoped
	if err := platformdb.WithTenantSession(f.ctx, f.app, granted, false, func(c context.Context) error {
		tag, err := platformdb.QuerierFromContext(c, f.app).Exec(c, `DELETE FROM hub_inbox_items`)
		if err == nil && tag.RowsAffected() != 0 {
			t.Errorf("a user session deleted %d projection row(s)", tag.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
