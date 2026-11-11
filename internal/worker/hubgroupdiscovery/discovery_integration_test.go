package hubgroupdiscovery_test

// The autodiscovery job on a real PostgreSQL, with a fake Directory standing in for a live WAHA call
// (the job never talks to WAHA directly; it only depends on the small hubgroupdiscovery.Directory
// interface). Proves: a contract without 'groups' never even gets a provider call; a new group is
// written to wa_groups WITHOUT ever enabling it (ADR-0015) and to hub_group_catalog through the
// SECURITY DEFINER function (migration 000187); an already-enabled group's enabled/enabled_by/enabled_at
// are left untouched; one tenant's failure never stops the others; running the same cycle twice never
// duplicates anything.
//
// Run with: scripts/test-integration-one.sh ./internal/worker/hubgroupdiscovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
	"github.com/omnira/omnira/internal/worker/hubgroupdiscovery"
)

type fx struct {
	t     *testing.T
	ctx   context.Context
	owner *pgxpool.Pool
	app   *pgxpool.Pool
	hub   uuid.UUID
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
	f := &fx{t: t, ctx: ctx, owner: owner, app: app, hub: uuid.New()}
	f.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, $2)`, f.hub, "Hub "+f.hub.String()[:8])
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

type tenantFx struct {
	id, conn, contract uuid.UUID
}

// tenant creates an active tenant with an active WAHA connection and a live contract with this
// fixture's hub; groupsScoped decides whether that contract delegates 'groups'.
func (f *fx) tenant(name string, groupsScoped bool) tenantFx {
	f.t.Helper()
	tf := tenantFx{id: uuid.New(), conn: uuid.New(), contract: uuid.New()}
	f.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, tf.id, name+" "+tf.id.String()[:8])
	f.exec(`INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id, status)
	        VALUES ($1, $2, 'whatsapp', 'waha', 'unofficial', $3, 'active')`, tf.conn, tf.id, "n-"+tf.conn.String())
	scopes := []string{}
	if groupsScoped {
		scopes = []string{"groups"}
	}
	f.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from, management_scopes)
	        VALUES ($1, $2, $3, now() - interval '1 day', $4)`, tf.contract, f.hub, tf.id, scopes)
	return tf
}

func (f *fx) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	f.must(f.owner.QueryRow(f.ctx, sql, args...).Scan(&n))
	return n
}

type waGroupState struct {
	enabled   bool
	enabledBy *uuid.UUID
	enabledAt *time.Time
	name      string
	found     bool
}

func (f *fx) waGroup(tenant, conn uuid.UUID, providerGroupID string) waGroupState {
	f.t.Helper()
	var s waGroupState
	err := f.owner.QueryRow(f.ctx, `SELECT enabled, enabled_by, enabled_at, name FROM wa_groups
	                                 WHERE tenant_id = $1 AND channel_connection_id = $2 AND provider_group_id = $3`,
		tenant, conn, providerGroupID).Scan(&s.enabled, &s.enabledBy, &s.enabledAt, &s.name)
	if err == nil {
		s.found = true
	}
	return s
}

type catalogState struct {
	displayName string
	version     int64
	found       bool
}

func (f *fx) catalog(tenant, conn uuid.UUID, providerGroupID string) catalogState {
	f.t.Helper()
	var s catalogState
	err := f.owner.QueryRow(f.ctx, `SELECT display_name, version FROM hub_group_catalog
	                                 WHERE hub_id = $1 AND source_tenant_id = $2 AND source_channel_connection_id = $3 AND provider_group_id = $4`,
		f.hub, tenant, conn, providerGroupID).Scan(&s.displayName, &s.version)
	if err == nil {
		s.found = true
	}
	return s
}

// fakeDirectory stands in for a live WAHA call: fixed per connection, optionally failing, and counting
// how many times each connection was asked.
type fakeDirectory struct {
	mu        sync.Mutex
	byConn    map[uuid.UUID][]hubgroupdiscovery.Group
	errByConn map[uuid.UUID]error
	calls     map[uuid.UUID]int
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{byConn: map[uuid.UUID][]hubgroupdiscovery.Group{}, errByConn: map[uuid.UUID]error{}, calls: map[uuid.UUID]int{}}
}

func (f *fakeDirectory) set(conn uuid.UUID, groups ...hubgroupdiscovery.Group) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byConn[conn] = groups
}

func (f *fakeDirectory) failOn(conn uuid.UUID, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errByConn[conn] = err
}

func (f *fakeDirectory) callCount(conn uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[conn]
}

func (f *fakeDirectory) List(_ context.Context, _ uuid.UUID, connectionID uuid.UUID) ([]hubgroupdiscovery.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[connectionID]++
	if err, ok := f.errByConn[connectionID]; ok {
		return nil, err
	}
	return f.byConn[connectionID], nil
}

func TestRunOnce_ContractWithoutGroupsScopeIsIgnored(t *testing.T) {
	f := newFx(t)
	tn := f.tenant("NoScope", false)
	jid := "551199990000001@g.us"
	dir := newFakeDirectory()
	dir.set(tn.conn, hubgroupdiscovery.Group{ProviderGroupID: jid, Name: "Grupo"})

	job := hubgroupdiscovery.New(f.app, dir)
	if err := job.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := dir.callCount(tn.conn); got != 0 {
		t.Fatalf("a contract without 'groups' must never even reach the directory: got %d calls, want 0", got)
	}
	if n := f.count(`SELECT count(*) FROM wa_groups WHERE tenant_id = $1`, tn.id); n != 0 {
		t.Fatalf("expected no wa_groups row, got %d", n)
	}
	if n := f.count(`SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, tn.id); n != 0 {
		t.Fatalf("expected no hub_group_catalog row, got %d", n)
	}
}

func TestRunOnce_DiscoversGroupsIntoWaGroupsAndCatalogWithoutEnabling(t *testing.T) {
	f := newFx(t)
	tn := f.tenant("Alpha", true)
	jid := "551188880000001@g.us"
	dir := newFakeDirectory()
	dir.set(tn.conn, hubgroupdiscovery.Group{ProviderGroupID: jid, Name: "Grupo Alpha"})

	job := hubgroupdiscovery.New(f.app, dir)
	if err := job.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	wa := f.waGroup(tn.id, tn.conn, jid)
	if !wa.found {
		t.Fatal("expected a wa_groups row to be created")
	}
	if wa.enabled {
		t.Fatal("autodiscovery must never set enabled=true (ADR-0015: only the Enable handler may)")
	}
	if wa.enabledBy != nil || wa.enabledAt != nil {
		t.Fatalf("autodiscovery must never set enabled_by/enabled_at, got enabled_by=%v enabled_at=%v", wa.enabledBy, wa.enabledAt)
	}
	if wa.name != "Grupo Alpha" {
		t.Fatalf("wa_groups.name = %q, want %q", wa.name, "Grupo Alpha")
	}

	cat := f.catalog(tn.id, tn.conn, jid)
	if !cat.found {
		t.Fatal("expected a hub_group_catalog row to be created")
	}
	if cat.displayName != "Grupo Alpha" || cat.version != 1 {
		t.Fatalf("catalog row = (display_name=%q, version=%d), want (%q, 1)", cat.displayName, cat.version, "Grupo Alpha")
	}
}

func TestRunOnce_NeverChangesAnAlreadyEnabledGroup(t *testing.T) {
	f := newFx(t)
	tn := f.tenant("Beta", true)
	jid := "551188880000002@g.us"
	humanAdmin := uuid.New()
	f.exec(`INSERT INTO users (id, external_subject) VALUES ($1, $2)`, humanAdmin, "admin-"+humanAdmin.String())
	f.exec(`INSERT INTO wa_groups (tenant_id, channel_connection_id, provider_group_id, name, enabled, enabled_by, enabled_at)
	        VALUES ($1, $2, $3, 'Nome Antigo', true, $4, now() - interval '2 hours')`, tn.id, tn.conn, jid, humanAdmin)
	before := f.waGroup(tn.id, tn.conn, jid)
	if !before.found || !before.enabled || before.enabledAt == nil {
		t.Fatal("fixture setup failed")
	}

	dir := newFakeDirectory()
	dir.set(tn.conn, hubgroupdiscovery.Group{ProviderGroupID: jid, Name: "Nome Novo Do WhatsApp"})
	job := hubgroupdiscovery.New(f.app, dir)
	if err := job.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	after := f.waGroup(tn.id, tn.conn, jid)
	if !after.enabled {
		t.Fatal("an already-enabled group must stay enabled")
	}
	if after.enabledBy == nil || *after.enabledBy != humanAdmin {
		t.Fatalf("enabled_by must stay the human administrator who enabled it, got %v", after.enabledBy)
	}
	if after.enabledAt == nil || !after.enabledAt.Equal(*before.enabledAt) {
		t.Fatalf("enabled_at must stay exactly what the human set, got %v want %v", after.enabledAt, before.enabledAt)
	}
	if after.name != "Nome Novo Do WhatsApp" {
		t.Fatalf("name should still refresh from the provider, got %q", after.name)
	}
}

func TestRunOnce_ErrorInOneTenantDoesNotStopOthers(t *testing.T) {
	f := newFx(t)
	bad := f.tenant("Failing", true)
	good := f.tenant("Working", true)
	jidGood := "551188880000003@g.us"
	dir := newFakeDirectory()
	dir.failOn(bad.conn, errors.New("waha: timeout"))
	dir.set(good.conn, hubgroupdiscovery.Group{ProviderGroupID: jidGood, Name: "Grupo Working"})

	job := hubgroupdiscovery.New(f.app, dir)
	if err := job.RunOnce(f.ctx); err == nil {
		t.Fatal("expected RunOnce to report the failing triple's error")
	}
	if n := f.count(`SELECT count(*) FROM wa_groups WHERE tenant_id = $1`, bad.id); n != 0 {
		t.Fatalf("failing tenant: expected no wa_groups row, got %d", n)
	}
	if cat := f.catalog(good.id, good.conn, jidGood); !cat.found {
		t.Fatal("the other, healthy tenant must still be discovered despite the first one failing")
	}
}

func TestRunOnce_RepeatedGroupOnSecondCycleDoesNotDuplicate(t *testing.T) {
	f := newFx(t)
	tn := f.tenant("Gamma", true)
	jid := "551188880000004@g.us"
	dir := newFakeDirectory()
	dir.set(tn.conn, hubgroupdiscovery.Group{ProviderGroupID: jid, Name: "Grupo Gamma"})
	job := hubgroupdiscovery.New(f.app, dir)

	for i := 0; i < 2; i++ {
		if err := job.RunOnce(f.ctx); err != nil {
			t.Fatalf("RunOnce #%d: %v", i+1, err)
		}
	}

	if n := f.count(`SELECT count(*) FROM wa_groups WHERE tenant_id = $1 AND channel_connection_id = $2 AND provider_group_id = $3`,
		tn.id, tn.conn, jid); n != 1 {
		t.Fatalf("wa_groups: expected exactly 1 row after two cycles, got %d", n)
	}
	if n := f.count(`SELECT count(*) FROM hub_group_catalog WHERE hub_id = $1 AND source_tenant_id = $2 AND source_channel_connection_id = $3 AND provider_group_id = $4`,
		f.hub, tn.id, tn.conn, jid); n != 1 {
		t.Fatalf("hub_group_catalog: expected exactly 1 row after two cycles, got %d", n)
	}
}
