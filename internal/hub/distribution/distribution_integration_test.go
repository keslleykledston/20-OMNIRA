package distribution_test

// Work pools and automatic distribution (ADR-0038 phase 4) on a real PostgreSQL. What must hold: only a hub admin edits pools; a pool
// answers for instances that have an active contract; distribution picks the least loaded member and only among people who hold a LIVE
// reply-capable grant for that conversation's queue; it never touches a closed, assigned, busy or suspended-company conversation; and a
// revocation that races the assignment wins (the authorization is pinned, then re-proven, inside the assignment's transaction).

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/distribution"
	"github.com/omnira/omnira/internal/testhelpers"
)

type world struct {
	t                                     *testing.T
	ctx                                   context.Context
	owner                                 *pgxpool.Pool
	app                                   *pgxpool.Pool
	svc                                   *distribution.Service
	hub                                   uuid.UUID
	admin                                 uuid.UUID
	tenant                                map[string]uuid.UUID
	queue                                 map[string]uuid.UUID
	contract                              map[string]uuid.UUID
	roleAgent, roleAdmin, roleTenantAgent uuid.UUID
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	w := &world{t: t, ctx: ctx, owner: owner, app: app, svc: distribution.New(app),
		tenant: map[string]uuid.UUID{}, queue: map[string]uuid.UUID{}, contract: map[string]uuid.UUID{}}
	w.roleAgent, w.roleAdmin, w.roleTenantAgent = w.role("hub_agent"), w.role("hub_admin"), w.role("tenant_agent")
	w.hub = uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, $2)`, w.hub, "Hub "+w.hub.String()[:8])
	for _, k := range []string{"A", "B"} {
		id := uuid.New()
		w.tenant[k] = id
		w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, id, "Tenant "+k+" "+id.String()[:8])
		w.queue[k] = uuid.New()
		w.exec(`INSERT INTO queues (id, tenant_id, name) VALUES ($1, $2, 'q')`, w.queue[k], id)
		w.contract[k] = uuid.New()
		w.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from) VALUES ($1, $2, $3, now() - interval '1 day')`, w.contract[k], w.hub, id)
	}
	w.admin = w.person("admin", w.roleAdmin)
	return w
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
func (w *world) count(sql string, args ...any) int {
	w.t.Helper()
	var n int
	w.must(w.owner.QueryRow(w.ctx, sql, args...).Scan(&n))
	return n
}
func (w *world) role(key string) uuid.UUID {
	var id uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = $1 LIMIT 1`, key).Scan(&id))
	return id
}
func (w *world) person(name string, role uuid.UUID) uuid.UUID {
	id := uuid.New()
	w.exec(`INSERT INTO users (id, external_subject, email) VALUES ($1, $2, $3)`, id, name+"-"+id.String(), name+"-"+id.String()[:8]+"@example.com")
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, id, role)
	return id
}
func (w *world) agent(name string) uuid.UUID { return w.person(name, w.roleAgent) }

// grant gives the agent a live grant on the tenant; reply says whether it may claim and answer.
func (w *world) grant(user uuid.UUID, tenant string, reply bool) uuid.UUID {
	id := uuid.New()
	w.exec(`INSERT INTO effective_access_grants (id, hub_id, user_id, tenant_id, service_contract_id, can_reply, valid_from)
	        VALUES ($1, $2, $3, $4, $5, $6, now() - interval '1 day')`, id, w.hub, user, w.tenant[tenant], w.contract[tenant], reply)
	return id
}

// item makes an open, unassigned conversation (in the tenant's queue when q is true) with its hub inbox item.
func (w *world) item(tenant string, q bool) (conv, item uuid.UUID) {
	contact := uuid.New()
	w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, 'Cliente', $3)`, contact, w.tenant[tenant], fmt.Sprintf("+5511%09d", rand.Intn(1_000_000_000)))
	conv, item = uuid.New(), uuid.New()
	var queue *uuid.UUID
	if q {
		queue = &[]uuid.UUID{w.queue[tenant]}[0]
	}
	w.exec(`INSERT INTO conversations (id, tenant_id, contact_id, queue_id) VALUES ($1, $2, $3, $4)`, conv, w.tenant[tenant], contact, queue)
	w.exec(`INSERT INTO messages (tenant_id, conversation_id, direction) VALUES ($1, $2, 'inbound')`, w.tenant[tenant], conv)
	w.exec(`INSERT INTO hub_inbox_items (id, hub_id, tenant_id, conversation_id, queue_id) VALUES ($1, $2, $3, $4, $5)`, item, w.hub, w.tenant[tenant], conv, queue)
	return conv, item
}

func (w *world) holder(conv uuid.UUID) *uuid.UUID {
	var u *uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT assigned_to_user_id FROM conversations WHERE id = $1`, conv).Scan(&u))
	return u
}

// pool makes a pool answering for the tenants, with the members.
func (w *world) pool(mode string, tenants []string, members ...uuid.UUID) uuid.UUID {
	p, err := w.svc.Create(w.ctx, w.admin, w.hub, "Pool "+uuid.NewString()[:6], mode)
	w.must(err)
	var ms []distribution.MemberSpec
	for _, m := range members {
		ms = append(ms, distribution.MemberSpec{UserID: m})
	}
	w.must(w.svc.SetMembers(w.ctx, w.admin, w.hub, p.ID, ms))
	var is []distribution.InstanceSpec
	for _, k := range tenants {
		is = append(is, distribution.InstanceSpec{TenantID: w.tenant[k]})
	}
	w.must(w.svc.SetInstances(w.ctx, w.admin, w.hub, p.ID, is))
	return p.ID
}

func TestPools_OnlyAnAdminOfTheHubEditsThem(t *testing.T) {
	w := newWorld(t)
	agent := w.agent("agent")
	otherHub := uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Other')`, otherHub)
	otherAdmin := w.person("x", w.roleAdmin)
	w.exec(`DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, w.hub, otherAdmin)
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, otherHub, otherAdmin, w.roleAdmin)
	p, err := w.svc.Create(w.ctx, w.admin, w.hub, "Atendimento", "manual")
	w.must(err)

	for who, u := range map[string]uuid.UUID{"agent": agent, "admin of another hub": otherAdmin, "nobody": uuid.Nil} {
		if _, err := w.svc.List(w.ctx, u, w.hub); !errors.Is(err, distribution.ErrForbidden) {
			t.Errorf("%s: List = %v", who, err)
		}
		if _, err := w.svc.Create(w.ctx, u, w.hub, "x", "manual"); !errors.Is(err, distribution.ErrForbidden) {
			t.Errorf("%s: Create = %v", who, err)
		}
		if err := w.svc.SetMembers(w.ctx, u, w.hub, p.ID, nil); !errors.Is(err, distribution.ErrForbidden) {
			t.Errorf("%s: SetMembers = %v", who, err)
		}
		if err := w.svc.SetInstances(w.ctx, u, w.hub, p.ID, nil); !errors.Is(err, distribution.ErrForbidden) {
			t.Errorf("%s: SetInstances = %v", who, err)
		}
		name := "hacked"
		if err := w.svc.Update(w.ctx, u, w.hub, p.ID, &name, nil); !errors.Is(err, distribution.ErrForbidden) {
			t.Errorf("%s: Update = %v", who, err)
		}
		if err := w.svc.Delete(w.ctx, u, w.hub, p.ID); !errors.Is(err, distribution.ErrForbidden) {
			t.Errorf("%s: Delete = %v", who, err)
		}
	}
	if n := w.count(`SELECT count(*) FROM work_pools WHERE id = $1 AND name = 'Atendimento'`, p.ID); n != 1 {
		t.Fatal("a refused call changed the pool")
	}
	// the admin of another hub cannot touch THIS hub's pool even by its id
	other := "x"
	if err := w.svc.Update(w.ctx, otherAdmin, otherHub, p.ID, &other, nil); !errors.Is(err, distribution.ErrNotFound) {
		t.Errorf("another hub's admin on this hub's pool id: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM audit_events WHERE action = 'hub.pool.created' AND actor_id = $1`, w.admin); n != 1 {
		t.Errorf("creating a pool must be audited with the actor, got %d", n)
	}
}

func TestPools_MembersAndInstancesAreValidated(t *testing.T) {
	w := newWorld(t)
	a1, a2 := w.agent("a1"), w.agent("a2")
	outsider := uuid.New()
	w.exec(`INSERT INTO users (id, external_subject) VALUES ($1, $2)`, outsider, "out-"+outsider.String())
	p1, err := w.svc.Create(w.ctx, w.admin, w.hub, "Um", "round_robin")
	w.must(err)
	p2, err := w.svc.Create(w.ctx, w.admin, w.hub, "Dois", "manual")
	w.must(err)

	if err := w.svc.SetMembers(w.ctx, w.admin, w.hub, p1.ID, []distribution.MemberSpec{{UserID: outsider}}); !errors.Is(err, distribution.ErrNotFound) {
		t.Errorf("a person outside the hub: %v", err)
	}
	if err := w.svc.SetMembers(w.ctx, w.admin, w.hub, p1.ID, []distribution.MemberSpec{{UserID: a1}, {UserID: a1}}); !errors.Is(err, distribution.ErrInvalid) {
		t.Errorf("the same person twice: %v", err)
	}
	if err := w.svc.SetMembers(w.ctx, w.admin, w.hub, p1.ID, []distribution.MemberSpec{{UserID: a1, MaxOpen: 501}}); !errors.Is(err, distribution.ErrInvalid) {
		t.Errorf("max_open out of range: %v", err)
	}
	w.must(w.svc.SetMembers(w.ctx, w.admin, w.hub, p1.ID, []distribution.MemberSpec{{UserID: a1, MaxOpen: 3}, {UserID: a2}}))
	w.exec(`UPDATE work_pool_members SET last_assigned_at = now() - interval '1 hour' WHERE work_pool_id = $1 AND user_id = $2`, p1.ID, a1)
	// replacing keeps the place in the rotation of whoever stays, and removes whoever is not listed
	w.must(w.svc.SetMembers(w.ctx, w.admin, w.hub, p1.ID, []distribution.MemberSpec{{UserID: a1, MaxOpen: 5}}))
	if n := w.count(`SELECT count(*) FROM work_pool_members WHERE work_pool_id = $1 AND user_id = $2 AND max_open = 5 AND last_assigned_at IS NOT NULL`, p1.ID, a1); n != 1 {
		t.Error("a member who stays must keep last_assigned_at and take the new capacity")
	}
	if n := w.count(`SELECT count(*) FROM work_pool_members WHERE work_pool_id = $1`, p1.ID); n != 1 {
		t.Errorf("members not listed must be removed, found %d", n)
	}

	// instances: contract required, queue must belong, one pool per scope
	stranger := uuid.New()
	if err := w.svc.SetInstances(w.ctx, w.admin, w.hub, p1.ID, []distribution.InstanceSpec{{TenantID: stranger}}); !errors.Is(err, distribution.ErrNotFound) {
		t.Errorf("an instance with no contract: %v", err)
	}
	q := w.queue["B"]
	if err := w.svc.SetInstances(w.ctx, w.admin, w.hub, p1.ID, []distribution.InstanceSpec{{TenantID: w.tenant["A"], QueueID: &q}}); !errors.Is(err, distribution.ErrNotFound) {
		t.Errorf("another instance's queue: %v", err)
	}
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = $1`, w.contract["B"])
	if err := w.svc.SetInstances(w.ctx, w.admin, w.hub, p1.ID, []distribution.InstanceSpec{{TenantID: w.tenant["B"]}}); !errors.Is(err, distribution.ErrNotFound) {
		t.Errorf("a suspended contract: %v", err)
	}
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'active' WHERE id = $1`, w.contract["B"])
	w.must(w.svc.SetInstances(w.ctx, w.admin, w.hub, p1.ID, []distribution.InstanceSpec{{TenantID: w.tenant["A"]}}))
	if err := w.svc.SetInstances(w.ctx, w.admin, w.hub, p2.ID, []distribution.InstanceSpec{{TenantID: w.tenant["A"]}}); !errors.Is(err, distribution.ErrConflict) {
		t.Errorf("two pools for the same instance: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM work_pool_instances WHERE work_pool_id = $1`, p2.ID); n != 0 {
		t.Error("a conflicting replacement must leave nothing behind")
	}
	// one queue of A can have its own pool next to the pool of the whole instance
	qa := w.queue["A"]
	w.must(w.svc.SetInstances(w.ctx, w.admin, w.hub, p2.ID, []distribution.InstanceSpec{{TenantID: w.tenant["A"], QueueID: &qa}}))

	pools, err := w.svc.List(w.ctx, w.admin, w.hub)
	w.must(err)
	if len(pools) != 2 {
		t.Fatalf("listed %d pools", len(pools))
	}
	// deleting removes members and instances with it
	w.must(w.svc.Delete(w.ctx, w.admin, w.hub, p1.ID))
	if w.count(`SELECT count(*) FROM work_pool_members WHERE work_pool_id = $1`, p1.ID)+w.count(`SELECT count(*) FROM work_pool_instances WHERE work_pool_id = $1`, p1.ID) != 0 {
		t.Error("deleting a pool must delete its members and instances")
	}
}

func TestDistribute_ChoosesTheLeastLoadedAmongPeopleWhoMayReply(t *testing.T) {
	w := newWorld(t)
	reply1, reply2 := w.agent("reply1"), w.agent("reply2")
	readOnly, noGrant, full := w.agent("readonly"), w.agent("nogrant"), w.agent("full")
	w.grant(reply1, "A", true)
	w.grant(reply2, "A", true)
	w.grant(readOnly, "A", false)
	w.grant(full, "A", true)
	w.pool("round_robin", []string{"A"}, reply1, reply2, readOnly, noGrant, full)
	w.exec(`UPDATE work_pool_members SET max_open = 1 WHERE user_id = $1`, full)
	// `full` already holds one open conversation: at capacity
	busyConv, _ := w.item("A", true)
	w.exec(`UPDATE conversations SET assigned_to_user_id = $1 WHERE id = $2`, full, busyConv)

	got := map[uuid.UUID]int{}
	for i := 0; i < 6; i++ {
		conv, item := w.item("A", true)
		who, err := w.svc.Distribute(w.ctx, w.hub, item)
		w.must(err)
		if who == nil {
			t.Fatalf("round %d: nobody got it although two people can reply", i)
		}
		if h := w.holder(conv); h == nil || *h != *who {
			t.Fatalf("round %d: the answer and the database disagree", i)
		}
		got[*who]++
	}
	if got[readOnly]+got[noGrant]+got[full] != 0 {
		t.Fatalf("assigned to somebody who may not take it: %v", got)
	}
	if got[reply1] != 3 || got[reply2] != 3 {
		t.Fatalf("the least loaded must alternate evenly: %v", got)
	}
	if n := w.count(`SELECT count(*) FROM assignment_events WHERE reason = 'hub_pool' AND actor_source = 'system' AND changed_by IS NULL`); n != 6 {
		t.Errorf("each assignment needs a history row marked as the system, got %d", n)
	}
	if n := w.count(`SELECT count(*) FROM audit_events WHERE action = 'hub.conversation.auto_assigned'`); n != 6 {
		t.Errorf("each assignment is audited, got %d", n)
	}
}

func TestDistribute_NeverTouchesWhatItShouldNot(t *testing.T) {
	w := newWorld(t)
	a := w.agent("a")
	w.grant(a, "A", true)
	w.grant(a, "B", true)
	manual := w.pool("manual", []string{"B"}, a)
	_ = manual
	w.pool("round_robin", []string{"A"}, a)

	try := func(why string, conv, item uuid.UUID, want bool) {
		t.Helper()
		who, err := w.svc.Distribute(w.ctx, w.hub, item)
		w.must(err)
		if (who != nil) != want {
			t.Errorf("%s: assigned=%v, want %v", why, who != nil, want)
		}
		if want && (w.holder(conv) == nil || *w.holder(conv) != *who) {
			t.Errorf("%s: the database disagrees with the answer", why)
		}
	}
	c, i := w.item("B", true)
	try("a manual pool distributes nothing", c, i, false)
	c, i = w.item("A", true)
	w.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, c)
	try("a closed conversation", c, i, false)
	c, i = w.item("A", true)
	other := w.agent("other")
	w.exec(`UPDATE conversations SET assigned_to_user_id = $1 WHERE id = $2`, other, c)
	try("an already assigned conversation", c, i, false)
	if h := w.holder(c); h == nil || *h != other {
		t.Error("an assigned conversation must keep its holder")
	}
	c, i = w.item("A", true)
	w.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, w.tenant["A"])
	try("a suspended company", c, i, false)
	w.exec(`UPDATE tenants SET status = 'active' WHERE id = $1`, w.tenant["A"])
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = $1`, w.contract["A"])
	try("a suspended contract", c, i, false)
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'active' WHERE id = $1`, w.contract["A"])
	w.exec(`UPDATE effective_access_grants SET valid_until = now() - interval '1 second' WHERE user_id = $1 AND tenant_id = $2`, a, w.tenant["A"])
	try("an expired grant", c, i, false)
	w.exec(`UPDATE effective_access_grants SET valid_until = NULL WHERE user_id = $1 AND tenant_id = $2`, a, w.tenant["A"])
	w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, a)
	try("a deactivated account", c, i, false)
	w.exec(`UPDATE users SET status = 'active' WHERE id = $1`, a)
	// a contract limited to some queues: a conversation outside them is not for this member
	w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = jsonb_build_object('queue_ids', '[]'::jsonb) WHERE id = $1`, w.contract["A"])
	try("a queue outside the contract's scope", c, i, false)
	w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}' WHERE id = $1`, w.contract["A"])
	try("everything restored: it works", c, i, true)
	// idempotent: asking again does not move it
	first := *w.holder(c)
	who, err := w.svc.Distribute(w.ctx, w.hub, i)
	w.must(err)
	if who != nil || *w.holder(c) != first {
		t.Error("distributing an assigned conversation again must change nothing")
	}
	// an unknown item and an item of another hub are simply nothing
	if who, err := w.svc.Distribute(w.ctx, w.hub, uuid.New()); who != nil || err != nil {
		t.Errorf("unknown item: %v %v", who, err)
	}
	if who, err := w.svc.Distribute(w.ctx, uuid.New(), i); who != nil || err != nil {
		t.Errorf("another hub's id on this item: %v %v", who, err)
	}
}

func TestDistribute_PrefersThePoolOfTheQueueOverThePoolOfTheInstance(t *testing.T) {
	w := newWorld(t)
	wide, narrow := w.agent("wide"), w.agent("narrow")
	w.grant(wide, "A", true)
	w.grant(narrow, "A", true)
	w.pool("round_robin", []string{"A"}, wide)
	p2, err := w.svc.Create(w.ctx, w.admin, w.hub, "Fila", "round_robin")
	w.must(err)
	w.must(w.svc.SetMembers(w.ctx, w.admin, w.hub, p2.ID, []distribution.MemberSpec{{UserID: narrow}}))
	q := w.queue["A"]
	w.must(w.svc.SetInstances(w.ctx, w.admin, w.hub, p2.ID, []distribution.InstanceSpec{{TenantID: w.tenant["A"], QueueID: &q}}))
	_, item := w.item("A", true)
	who, err := w.svc.Distribute(w.ctx, w.hub, item)
	w.must(err)
	if who == nil || *who != narrow {
		t.Fatalf("the pool of the queue must win over the pool of the whole instance: %v", who)
	}
	_, item = w.item("A", false) // no queue: only the whole-instance pool applies
	who, err = w.svc.Distribute(w.ctx, w.hub, item)
	w.must(err)
	if who == nil || *who != wide {
		t.Fatalf("a conversation without queue goes to the pool of the instance: %v", who)
	}
}

func TestDistribute_TwoWorkersAssignExactlyOnce(t *testing.T) {
	w := newWorld(t)
	a, b := w.agent("a"), w.agent("b")
	w.grant(a, "A", true)
	w.grant(b, "A", true)
	w.pool("round_robin", []string{"A"}, a, b)
	conv, item := w.item("A", true)
	var wg sync.WaitGroup
	results := make([]*uuid.UUID, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			who, err := w.svc.Distribute(w.ctx, w.hub, item)
			if err != nil {
				t.Error(err)
			}
			results[i] = who
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, r := range results {
		if r != nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one worker may assign, %d did", winners)
	}
	if n := w.count(`SELECT count(*) FROM assignment_events WHERE conversation_id = $1`, conv); n != 1 {
		t.Fatalf("one history row expected, got %d", n)
	}
}

// A revocation that is being committed WHILE the assignment pins the grant wins: the assignment waits, then re-proves, then refuses.
func TestDistribute_ARevocationRacingTheAssignmentWins(t *testing.T) {
	w := newWorld(t)
	a := w.agent("a")
	g := w.grant(a, "A", true)
	w.pool("round_robin", []string{"A"}, a)
	conv, item := w.item("A", true)

	tx, err := w.owner.Begin(w.ctx)
	w.must(err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) }) // never leave the revocation parked if an assertion fails
	_, err = tx.Exec(w.ctx, `UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
	w.must(err)

	done := make(chan *uuid.UUID, 1)
	go func() {
		who, err := w.svc.Distribute(w.ctx, w.hub, item)
		if err != nil {
			t.Error(err)
		}
		done <- who
	}()
	select {
	case <-done:
		t.Fatal("the assignment did not wait for the revocation in progress")
	case <-time.After(700 * time.Millisecond):
	}
	w.must(tx.Commit(w.ctx))
	if who := <-done; who != nil {
		t.Fatalf("assigned to somebody whose grant was revoked while it ran: %v", who)
	}
	if w.holder(conv) != nil {
		t.Fatal("the conversation must stay unassigned")
	}
}

// A person who holds the conversation row (claiming, transferring) is never overridden or blocked: the distributor skips it.
func TestDistribute_SkipsAConversationSomeoneElseHolds(t *testing.T) {
	w := newWorld(t)
	a := w.agent("a")
	w.grant(a, "A", true)
	w.pool("round_robin", []string{"A"}, a)
	conv, item := w.item("A", true)
	tx, err := w.owner.Begin(w.ctx)
	w.must(err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var id uuid.UUID
	w.must(tx.QueryRow(w.ctx, `SELECT id FROM conversations WHERE id = $1 FOR UPDATE`, conv).Scan(&id))
	start := time.Now()
	who, err := w.svc.Distribute(w.ctx, w.hub, item)
	w.must(err)
	if who != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("must skip a locked conversation at once (assigned=%v after %v)", who != nil, time.Since(start))
	}
	w.must(tx.Rollback(w.ctx))
	if who, err := w.svc.Distribute(w.ctx, w.hub, item); err != nil || who == nil {
		t.Fatalf("once released it is distributed: %v %v", who, err)
	}
}

func TestPending_ListsOnlyWhatARoundRobinPoolCouldTake(t *testing.T) {
	w := newWorld(t)
	a := w.agent("a")
	w.grant(a, "A", true)
	w.pool("round_robin", []string{"A"}, a)
	w.pool("manual", []string{"B"}, a)
	_, wanted := w.item("A", true)
	_, _ = w.item("B", true) // a manual pool: not listed
	c3, _ := w.item("A", true)
	w.exec(`UPDATE hub_inbox_items SET assigned_user_id = $1 WHERE conversation_id = $2`, a, c3) // assigned: not listed
	c4, _ := w.item("A", true)
	w.exec(`UPDATE hub_inbox_items SET status = 'closed' WHERE conversation_id = $1`, c4)
	got, err := w.svc.Pending(w.ctx, 100)
	w.must(err)
	var mine []uuid.UUID
	for _, it := range got {
		if it.Hub == w.hub {
			mine = append(mine, it.ID)
		}
	}
	if len(mine) != 1 || mine[0] != wanted {
		t.Fatalf("Pending = %v, want only %s", mine, wanted)
	}
	w.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, w.tenant["A"])
	got, err = w.svc.Pending(w.ctx, 100)
	w.must(err)
	for _, it := range got {
		if it.ID == wanted {
			t.Fatal("a suspended company's conversations must not be offered")
		}
	}
}

var _ = pgx.ErrNoRows

// Codex review: two workers distributing DIFFERENT conversations at the same time must not both see room for the last slot.
func TestDistribute_CapacityIsNotOvershotByConcurrentWorkers(t *testing.T) {
	w := newWorld(t)
	a := w.agent("a")
	w.grant(a, "A", true)
	w.pool("round_robin", []string{"A"}, a)
	w.exec(`UPDATE work_pool_members SET max_open = 2 WHERE user_id = $1`, a)
	var items []uuid.UUID
	for i := 0; i < 8; i++ {
		_, item := w.item("A", true)
		items = append(items, item)
	}
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func(item uuid.UUID) {
			defer wg.Done()
			if _, err := w.svc.Distribute(w.ctx, w.hub, item); err != nil {
				t.Error(err)
			}
		}(item)
	}
	wg.Wait()
	if n := w.count(`SELECT count(*) FROM conversations WHERE assigned_to_user_id = $1 AND status <> 'closed'`, a); n != 2 {
		t.Fatalf("a member with capacity 2 must hold exactly 2 after eight concurrent distributions, holds %d", n)
	}
}
