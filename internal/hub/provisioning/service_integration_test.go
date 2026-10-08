package provisioning_test

// Real-PostgreSQL proof of the operator provisioning service. The service runs through the application pool in a
// system session (as hubctl does); fixtures use the owner connection; effects are checked two ways: the rows and
// audit events themselves, and what a Hub agent can actually read through RLS on their own session.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
)

type fx struct {
	t     *testing.T
	ctx   context.Context
	owner *pgxpool.Pool
	app   *pgxpool.Pool
	svc   *provisioning.Service
}

type tenantFx struct {
	id, queue1, queue2, conv1, conv2 uuid.UUID
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
	svc, err := provisioning.New(app, "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	return &fx{t: t, ctx: ctx, owner: owner, app: app, svc: svc}
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

// tenant: one conversation in each of two queues.
func (f *fx) tenant(name string) tenantFx {
	t := tenantFx{id: uuid.New(), queue1: uuid.New(), queue2: uuid.New()}
	f.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, t.id, name+" "+t.id.String()[:8])
	f.exec(`INSERT INTO queues (id, tenant_id, name) VALUES ($1, $2, 'q1'), ($3, $2, 'q2')`, t.queue1, t.id, t.queue2)
	mk := func(queue uuid.UUID) uuid.UUID {
		id, contact := uuid.New(), uuid.New()
		f.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, 'c', $3)`, contact, t.id, fmt.Sprintf("+5511%09d", rand.Intn(1_000_000_000)))
		f.exec(`INSERT INTO conversations (id, tenant_id, contact_id, queue_id) VALUES ($1, $2, $3, $4)`, id, t.id, contact, queue)
		return id
	}
	t.conv1, t.conv2 = mk(t.queue1), mk(t.queue2)
	return t
}

func (f *fx) user(name string) uuid.UUID {
	u := uuid.New()
	f.exec(`INSERT INTO users (id, external_subject, email) VALUES ($1, $2, $3)`, u, name+"-"+u.String(), name+"-"+u.String()[:8]+"@example.test")
	return u
}

// reads: how many of the tenant's conversations the user can read on their own session.
func (f *fx) reads(user uuid.UUID, t tenantFx) int {
	var n int
	f.must(platformdb.WithTenantSession(f.ctx, f.app, user, false, func(c context.Context) error {
		return platformdb.QuerierFromContext(c, f.app).QueryRow(c, `SELECT count(*) FROM conversations WHERE tenant_id = $1`, t.id).Scan(&n)
	}))
	return n
}

func (f *fx) audits(action string, resource uuid.UUID) int {
	var n int
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2 AND metadata->>'operator' = 'test-operator'`, action, resource).Scan(&n))
	return n
}

func future() *time.Time { v := time.Now().Add(24 * time.Hour); return &v }
func past() *time.Time   { v := time.Now().Add(-time.Hour); return &v }

func TestProvisioning_EndToEnd_WhatTheOperatorCreatesIsWhatTheAgentSees(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A")
	agent, admin := f.user("agent"), f.user("admin")

	hub, err := f.svc.CreateHub(f.ctx, "  K3G Service Desk  ", "BPO")
	f.must(err)
	f.must(f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAgent))
	f.must(f.svc.AddMember(f.ctx, hub, admin, provisioning.RoleAdmin))
	contract, err := f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: a.id, QueueIDs: []uuid.UUID{a.queue1, a.queue1}})
	f.must(err)

	if got := f.reads(agent, a); got != 0 {
		t.Fatalf("a hub member with no grant reads %d conversation(s)", got)
	}
	grant, err := f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent})
	f.must(err)
	if got := f.reads(agent, a); got != 1 {
		t.Fatalf("with a grant on a queue-restricted contract the agent should read exactly the q1 conversation, got %d", got)
	}
	if got := f.reads(admin, a); got != 0 {
		t.Fatalf("a hub_admin role alone must not read tenant data, got %d", got)
	}

	f.must(f.svc.RevokeGrant(f.ctx, hub, a.id, agent))
	if got := f.reads(agent, a); got != 0 {
		t.Fatalf("a revoked grant still reads %d conversation(s)", got)
	}
	// renewing the same grant reactivates it: same row, version moves on
	grant2, err := f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent, ValidUntil: future(), Renew: true})
	f.must(err)
	if grant2 != grant {
		t.Fatalf("renewal created a second grant (%s vs %s)", grant2, grant)
	}
	if got := f.reads(agent, a); got != 1 {
		t.Fatalf("renewed grant reads %d", got)
	}
	f.must(f.svc.SetContractStatus(f.ctx, hub, a.id, "revoked"))
	if got := f.reads(agent, a); got != 0 {
		t.Fatalf("a revoked contract still lets the agent read %d", got)
	}

	// every change is attributable, in the same transaction
	for _, c := range []struct {
		action   string
		resource uuid.UUID
		want     int
	}{
		{"hub.created", hub, 1}, {"hub.member.added", hub, 2}, {"hub.contract.created", contract, 1},
		{"hub.grant.granted", grant, 2}, {"hub.grant.revoked", grant, 1}, {"hub.contract.status_changed", contract, 1},
	} {
		if got := f.audits(c.action, c.resource); got != c.want {
			t.Errorf("audit %s: %d event(s), want %d", c.action, got, c.want)
		}
	}
	var tenantOnAudit uuid.UUID
	f.must(f.owner.QueryRow(f.ctx, `SELECT tenant_id FROM audit_events WHERE action = 'hub.grant.granted' AND resource_id = $1 LIMIT 1`, grant).Scan(&tenantOnAudit))
	if tenantOnAudit != a.id {
		t.Errorf("a grant event must be attached to the tenant it concerns")
	}

	sum, err := f.svc.Describe(f.ctx, hub)
	f.must(err)
	if sum.Hub.Name != "K3G Service Desk" || len(sum.Members) != 2 || len(sum.Contracts) != 1 || len(sum.Grants) != 1 ||
		!sum.Contracts[0].Restricted || len(sum.Contracts[0].QueueIDs) != 1 || sum.Contracts[0].Status != "revoked" {
		t.Errorf("Describe disagrees with what was provisioned: %+v", sum)
	}
}

func TestProvisioning_Validation(t *testing.T) {
	f := newFx(t)
	a, b := f.tenant("A"), f.tenant("B")
	agent, ghost := f.user("agent"), uuid.New()
	hub, err := f.svc.CreateHub(f.ctx, "Hub", "")
	f.must(err)
	f.must(f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAgent))
	inactiveUser := f.user("suspended")
	f.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, inactiveUser)
	inactiveTenant := f.tenant("Inactive")
	f.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, inactiveTenant.id)
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: a.id})
	f.must(err)

	check := func(name string, want error, err error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", name, err, want)
		}
	}
	_, err = f.svc.CreateHub(f.ctx, "   ", "")
	check("blank hub name", provisioning.ErrInvalid, err)
	long := make([]rune, 201)
	for i := range long {
		long[i] = 'x'
	}
	_, err = f.svc.CreateHub(f.ctx, string(long), "")
	check("201-char hub name", provisioning.ErrInvalid, err)
	check("unknown role", provisioning.ErrInvalid, f.svc.AddMember(f.ctx, hub, agent, "root"))
	check("unknown hub", provisioning.ErrNotFound, f.svc.AddMember(f.ctx, uuid.New(), agent, provisioning.RoleAgent))
	check("unknown user", provisioning.ErrNotFound, f.svc.AddMember(f.ctx, hub, ghost, provisioning.RoleAgent))
	check("inactive user", provisioning.ErrInvalid, f.svc.AddMember(f.ctx, hub, inactiveUser, provisioning.RoleAgent))

	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: uuid.New()})
	check("unknown tenant", provisioning.ErrNotFound, err)
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: inactiveTenant.id})
	check("inactive tenant", provisioning.ErrInvalid, err)
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: a.id})
	check("second contract for the same pair", provisioning.ErrConflict, err)
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: b.id, QueueIDs: []uuid.UUID{a.queue1}})
	check("another tenant's queue in the allowlist", provisioning.ErrInvalid, err)
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: b.id, QueueIDs: []uuid.UUID{uuid.New()}})
	check("nonexistent queue", provisioning.ErrInvalid, err)
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: b.id, QueueIDs: []uuid.UUID{}})
	check("empty allowlist", provisioning.ErrInvalid, err)
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: b.id, ValidUntil: past()})
	check("contract already expired", provisioning.ErrInvalid, err)
	var leftover int
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM hub_tenant_service_contracts WHERE hub_id = $1 AND tenant_id = $2`, hub, b.id).Scan(&leftover))
	if leftover != 0 {
		t.Errorf("a refused contract left %d row(s) behind", leftover)
	}

	_, err = f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: b.id, User: agent})
	check("grant without a contract", provisioning.ErrNotFound, err)
	_, err = f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: f.user("outsider")})
	check("grant to a user outside the hub", provisioning.ErrInvalid, err)
	_, err = f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent, ValidUntil: past()})
	check("grant already expired", provisioning.ErrInvalid, err)
	f.must(f.svc.SetContractStatus(f.ctx, hub, a.id, "suspended"))
	_, err = f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent})
	check("grant on a suspended contract", provisioning.ErrInvalid, err)
	check("bad contract status", provisioning.ErrInvalid, f.svc.SetContractStatus(f.ctx, hub, a.id, "deleted"))
	check("status of a missing contract", provisioning.ErrNotFound, f.svc.SetContractStatus(f.ctx, hub, b.id, "active"))
	check("revoking a grant that never existed", provisioning.ErrNotFound, f.svc.RevokeGrant(f.ctx, hub, a.id, agent))
	f.exec(`UPDATE hub_tenant_service_contracts SET valid_from = now() - interval '3 days', valid_until = now() - interval '1 day' WHERE hub_id = $1 AND tenant_id = $2`, hub, a.id)
	check("reactivating an expired contract", provisioning.ErrInvalid, f.svc.SetContractStatus(f.ctx, hub, a.id, "active"))
	check("bad hub status", provisioning.ErrInvalid, f.svc.SetHubStatus(f.ctx, hub, "gone"))

	if _, err := provisioning.New(f.app, "   "); !errors.Is(err, provisioning.ErrInvalid) {
		t.Errorf("an operator name is mandatory for the audit trail, got %v", err)
	}
}

func TestProvisioning_IdempotencyAndMembership(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A")
	agent := f.user("agent")
	hub, err := f.svc.CreateHub(f.ctx, "Hub", "")
	f.must(err)

	f.must(f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAgent))
	f.must(f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAgent))
	if got := f.audits("hub.member.added", hub); got != 1 {
		t.Errorf("re-adding the same member audited %d times, want 1", got)
	}
	f.must(f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAdmin))
	if got := f.audits("hub.member.role_changed", hub); got != 1 {
		t.Errorf("a role change must be audited, got %d", got)
	}
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: a.id})
	f.must(err)
	g1, err := f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent})
	f.must(err)
	g2, err := f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent})
	f.must(err)
	var rows int
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM effective_access_grants WHERE hub_id = $1 AND user_id = $2`, hub, agent).Scan(&rows))
	if g1 != g2 || rows != 1 {
		t.Fatalf("granting twice must be one grant (ids %s/%s, rows %d)", g1, g2, rows)
	}
	f.must(f.svc.RevokeGrant(f.ctx, hub, a.id, agent))
	f.must(f.svc.RevokeGrant(f.ctx, hub, a.id, agent)) // already revoked: no-op
	if got := f.audits("hub.grant.revoked", g1); got != 1 {
		t.Errorf("revoking twice audited %d times, want 1", got)
	}

	// removing a member takes their grants with them and says how many
	f.must(f.svc.RevokeGrant(f.ctx, hub, a.id, agent))
	g3, err := f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent, Renew: true})
	f.must(err)
	removed, err := f.svc.RemoveMember(f.ctx, hub, agent)
	f.must(err)
	if removed != 1 {
		t.Errorf("RemoveMember reported %d grants removed, want 1", removed)
	}
	if got := f.reads(agent, a); got != 0 {
		t.Errorf("a removed member still reads %d", got)
	}
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM effective_access_grants WHERE id = $1`, g3).Scan(&rows))
	if rows != 0 {
		t.Error("the grant outlived the membership")
	}
	if _, err := f.svc.RemoveMember(f.ctx, hub, agent); !errors.Is(err, provisioning.ErrNotFound) {
		t.Errorf("removing a non-member: %v", err)
	}

	// a suspended hub refuses new relationships
	f.must(f.svc.SetHubStatus(f.ctx, hub, "suspended"))
	if err := f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAgent); !errors.Is(err, provisioning.ErrInvalid) {
		t.Errorf("a suspended hub accepted a member: %v", err)
	}
}

// The service must not be a way around the database rules: its effects obey the same RLS/constraints, and a
// user (not system) session can neither call it nor reach the tables it writes.
func TestProvisioning_RunsOnlyInASystemSession_AndLeavesNoPartialStateOnFailure(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A")
	hub, err := f.svc.CreateHub(f.ctx, "Hub", "")
	f.must(err)
	var before int
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM audit_events WHERE action LIKE 'hub.%'`).Scan(&before))
	// a failing operation (queue of another tenant) must roll back everything, including its audit event
	other := f.tenant("Other")
	if _, err := f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: a.id, QueueIDs: []uuid.UUID{other.queue1}}); err == nil {
		t.Fatal("expected a refusal")
	}
	var after int
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM audit_events WHERE action LIKE 'hub.%'`).Scan(&after))
	if after != before {
		t.Errorf("a refused operation left %d audit event(s)", after-before)
	}
	// an ordinary user session cannot write the provisioning tables directly
	user := f.user("plain")
	err = platformdb.WithTenantSession(f.ctx, f.app, user, false, func(c context.Context) error {
		_, e := platformdb.QuerierFromContext(c, f.app).Exec(c, `INSERT INTO service_hubs (name) VALUES ('rogue')`)
		return e
	})
	if err == nil {
		t.Error("a user session created a hub directly")
	}
}

func TestProvisioning_FindUserByEmail(t *testing.T) {
	f := newFx(t)
	u := f.user("findme")
	var email string
	f.must(f.owner.QueryRow(f.ctx, `SELECT email FROM users WHERE id = $1`, u).Scan(&email))
	got, err := f.svc.FindUserByEmail(f.ctx, "  "+upper(email)+" ")
	if err != nil || got != u {
		t.Fatalf("case-insensitive lookup: %v %v", got, err)
	}
	if _, err := f.svc.FindUserByEmail(f.ctx, "nobody-"+uuid.NewString()+"@example.test"); !errors.Is(err, provisioning.ErrNotFound) {
		t.Errorf("unknown e-mail: %v", err)
	}
	if _, err := f.svc.FindUserByEmail(f.ctx, " "); !errors.Is(err, provisioning.ErrInvalid) {
		t.Errorf("blank e-mail: %v", err)
	}
	dup := f.user("dup")
	f.exec(`UPDATE users SET email = $2 WHERE id = $1`, dup, "Dup-"+u.String()[:8]+"@example.test")
	other := f.user("dup2")
	f.exec(`UPDATE users SET email = $2 WHERE id = $1`, other, "dup-"+u.String()[:8]+"@example.test") // same address, different case
	if _, err := f.svc.FindUserByEmail(f.ctx, "dup-"+u.String()[:8]+"@example.test"); !errors.Is(err, provisioning.ErrInvalid) {
		t.Errorf("an ambiguous e-mail must be refused, not guessed: %v", err)
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestProvisioning_ReplyCapabilityIsExplicitAndAudited(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A")
	agent := f.user("agent")
	hub, err := f.svc.CreateHub(f.ctx, "Hub", "")
	f.must(err)
	f.must(f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAgent))
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: a.id})
	f.must(err)
	canReply := func() bool {
		var v bool
		f.must(f.owner.QueryRow(f.ctx, `SELECT can_reply FROM effective_access_grants WHERE hub_id = $1 AND user_id = $2`, hub, agent).Scan(&v))
		return v
	}
	g, err := f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent})
	f.must(err)
	if canReply() {
		t.Fatal("a grant must be read-only unless reply is asked for explicitly")
	}
	_, err = f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent, CanReply: true, Renew: true})
	f.must(err)
	if !canReply() {
		t.Fatal("the reply capability was not stored")
	}
	_, err = f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent})
	f.must(err)
	if canReply() {
		t.Fatal("renewing without --reply must remove the capability (it is set exactly as given)")
	}
	var withReply int
	f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM audit_events WHERE action = 'hub.grant.granted' AND resource_id = $1 AND (metadata->>'can_reply')::bool`, g).Scan(&withReply))
	if withReply != 1 {
		t.Errorf("exactly one grant event records can_reply=true, got %d", withReply)
	}
	sum, err := f.svc.Describe(f.ctx, hub)
	f.must(err)
	if len(sum.Grants) != 1 || sum.Grants[0].CanReply {
		t.Errorf("Describe disagrees: %+v", sum.Grants)
	}
	// reading is unchanged by the capability
	if got := f.reads(agent, a); got != 2 {
		t.Errorf("a read-only grant must still read the tenant's conversations, got %d", got)
	}
}

func TestProvisioning_GrantingAgainNeverWidensAccessByAccident(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A")
	agent := f.user("agent")
	hub, err := f.svc.CreateHub(f.ctx, "Hub", "BPO")
	f.must(err)
	f.must(f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAgent))
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: a.id})
	f.must(err)
	spec := provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent, ValidUntil: future()}
	_, err = f.svc.Grant(f.ctx, spec)
	f.must(err)

	widening := map[string]provisioning.GrantSpec{
		"lifting the validity": {Hub: hub, Tenant: a.id, User: agent},
		"extending it":         {Hub: hub, Tenant: a.id, User: agent, ValidUntil: ptrTime(time.Now().Add(72 * time.Hour))},
		"read-only to reply":   {Hub: hub, Tenant: a.id, User: agent, ValidUntil: spec.ValidUntil, CanReply: true},
	}
	for name, w := range widening {
		if _, err := f.svc.Grant(f.ctx, w); !errors.Is(err, provisioning.ErrConflict) {
			t.Errorf("%s without Renew: got %v, want ErrConflict", name, err)
		}
	}
	// keeping or narrowing needs no ceremony
	if _, err := f.svc.Grant(f.ctx, spec); err != nil {
		t.Errorf("granting the same thing again: %v", err)
	}
	if _, err := f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent, ValidUntil: ptrTime(time.Now().Add(time.Hour))}); err != nil {
		t.Errorf("shortening the validity: %v", err)
	}

	// a revoked grant stays revoked until someone renews it on purpose
	f.must(f.svc.RevokeGrant(f.ctx, hub, a.id, agent))
	if _, err := f.svc.Grant(f.ctx, spec); !errors.Is(err, provisioning.ErrConflict) {
		t.Fatalf("a revoked grant was revived without Renew: %v", err)
	}
	if got := f.reads(agent, a); got != 0 {
		t.Fatalf("the refused re-grant still opened access (%d)", got)
	}
	spec.Renew = true
	if _, err := f.svc.Grant(f.ctx, spec); err != nil {
		t.Fatalf("explicit renewal: %v", err)
	}
	if got := f.reads(agent, a); got == 0 {
		t.Fatalf("explicit renewal did not restore access")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
