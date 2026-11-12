package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Real Postgres, runtime role under RLS. Tenant A is the caller; tenant B owns data that must stay untouched.

type env struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool
	app  *pgxpool.Pool
}

func newEnv(t *testing.T) *env {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return &env{t: t, ctx: ctx, seed: seed, app: app}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.seed.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// tenant creates a tenant with one active WAHA connection.
func (e *env) tenant() (tenant uuid.UUID, conn channeldomain.ChannelConnection) {
	tenant = uuid.New()
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String())
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.seed.Exec(bg, `DELETE FROM wa_groups WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM channel_connections WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM tenants WHERE id=$1`, tenant)
	})
	conn = e.connection(tenant)
	return tenant, conn
}

// connection adds another active WAHA connection to an EXISTING tenant (a second WhatsApp instance).
func (e *env) connection(tenant uuid.UUID) channeldomain.ChannelConnection {
	conn := channeldomain.ChannelConnection{ID: uuid.New(), TenantID: tenant, Channel: channeldomain.ChannelWhatsApp, Provider: channeldomain.ProviderWAHA, ProviderKind: channeldomain.ProviderKindUnofficial, Status: channeldomain.ConnectionStatusActive}
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, conn.ID, tenant, conn.ID.String())
	return conn
}

func (e *env) member(tenant uuid.UUID, role, status string) uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	var roleID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, role).Scan(&roleID); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4)`, tenant, u, roleID, status)
	return u
}

func (e *env) group(tenant uuid.UUID, conn channeldomain.ChannelConnection, jid, name string, enabled bool) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO wa_groups(id,tenant_id,channel_connection_id,provider_group_id,name,enabled) VALUES($1,$2,$3,$4,$5,$6)`, id, tenant, conn.ID, jid, name, enabled)
	return id
}

// account creates a "cliente final" (customer_accounts, ADR-0018) a group can be linked to.
func (e *env) account(tenant uuid.UUID, name string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO customer_accounts(id,tenant_id,name) VALUES($1,$2,$3)`, id, tenant, name)
	return id
}

type fakeDir struct {
	conn   uuid.UUID
	groups []channeldomain.ProviderGroup
	err    error
	calls  int

	// otherActive simulates additional active connections that exist but are not conn (ambiguity testing):
	// with no hint, List refuses with ErrAmbiguousConnection instead of silently picking one.
	otherActive []uuid.UUID
}

func (d *fakeDir) List(_ context.Context, _, hint uuid.UUID) (uuid.UUID, []channeldomain.ProviderGroup, error) {
	d.calls++
	if hint == uuid.Nil {
		if len(d.otherActive) > 0 {
			return uuid.Nil, nil, ErrAmbiguousConnection
		}
		return d.conn, d.groups, d.err
	}
	if hint == d.conn {
		return d.conn, d.groups, d.err
	}
	for _, o := range d.otherActive {
		if o == hint {
			return o, nil, nil // a different instance: this fixture does not model its own groups
		}
	}
	return uuid.Nil, nil, ErrNoConnection
}

func call(t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID, method, target, body string, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	err := platformdb.WithTenantSession(context.Background(), pool, user, false, func(sessionCtx context.Context) error {
		tc, tcErr := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if tcErr != nil {
			return tcErr
		}
		req := httptest.NewRequest(method, target, bytes.NewReader([]byte(body))).WithContext(tenancydomain.WithTenantContext(sessionCtx, tc))
		for k, v := range path {
			req.SetPathValue(k, v)
		}
		fn(rec, req)
		return nil
	})
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
	return rec
}

const (
	jid1 = "120363000000000001@g.us"
	jid2 = "120363000000000002@g.us"
	jid3 = "120363000000000003@g.us"
)

func groupMsg(conn channeldomain.ChannelConnection, jid, id, body string, sent time.Time) channeldomain.InboundGroupMessage {
	return channeldomain.InboundGroupMessage{ProviderMessageID: id, ConnectionID: conn.ID.String(), GroupJID: jid, AuthorJID: "111@lid", AuthorName: "Rafael", Type: "text", Text: body, SentAt: sent}
}

// Only a group an administrator enabled has messages stored. Everything else is dropped before any
// persistence - not even the dedup record - and a duplicate never stores twice.
func TestIntakeStoresOnlyEnabledGroupsAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	g1 := e.group(tenantA, connA, jid1, "Oficial", true)
	e.group(tenantA, connA, jid2, "Desligado", false)
	// Tenant B knows the SAME group id but never enabled it.
	e.group(tenantB, connB, jid1, "Do B", false)
	intake := NewIntake(e.app, channeladapters.NewPostgresWebhookEventStore(e.app))
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	process := func(conn channeldomain.ChannelConnection, key string, m channeldomain.InboundGroupMessage) (bool, bool) {
		t.Helper()
		ing, dup, err := intake.ProcessGroupMessage(e.ctx, conn, key, "message.any", "digest-"+key, m)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return ing, dup
	}

	if ing, dup := process(connA, "k1", groupMsg(connA, jid1, "m1", "ola", base.Add(10*time.Minute))); !ing || dup {
		t.Fatalf("enabled group: ingested=%v duplicate=%v", ing, dup)
	}
	// A group that is disabled, one that is unknown, and tenant B's lookalike: all dropped, nothing persisted.
	for name, tc := range map[string]struct {
		conn channeldomain.ChannelConnection
		jid  string
	}{"disabled": {connA, jid2}, "unknown": {connA, jid3}, "other tenant, same id": {connB, jid1}} {
		if ing, dup := process(tc.conn, "drop-"+name, groupMsg(tc.conn, tc.jid, "x-"+name, "segredo", base)); ing || dup {
			t.Errorf("%s must be dropped, got ingested=%v duplicate=%v", name, ing, dup)
		}
		if n := e.count(`SELECT count(*) FROM channel_webhook_events WHERE provider_event_id=$1`, "drop-"+name); n != 0 {
			t.Errorf("%s: a dropped group must leave no dedup record, got %d", name, n)
		}
	}
	if n := e.count(`SELECT count(*) FROM wa_group_messages WHERE tenant_id IN ($1,$2)`, tenantA, tenantB); n != 1 {
		t.Fatalf("only the enabled group's message may exist, got %d", n)
	}

	// Same provider event again, and the same message under a new event id: stored once.
	if ing, dup := process(connA, "k1", groupMsg(connA, jid1, "m1", "ola", base.Add(10*time.Minute))); ing || !dup {
		t.Fatalf("repeated event: ingested=%v duplicate=%v", ing, dup)
	}
	if ing, dup := process(connA, "k1b", groupMsg(connA, jid1, "m1", "ola", base.Add(10*time.Minute))); ing || !dup {
		t.Fatalf("same message, new event id: ingested=%v duplicate=%v", ing, dup)
	}
	// Out-of-order delivery: an older message arriving later must not move last_message_at back.
	if ing, _ := process(connA, "k2", groupMsg(connA, jid1, "m0", "antiga", base)); !ing {
		t.Fatal("older message not stored")
	}
	var last time.Time
	if err := e.seed.QueryRow(e.ctx, `SELECT last_message_at FROM wa_groups WHERE id=$1`, g1).Scan(&last); err != nil || !last.Equal(base.Add(10*time.Minute)) {
		t.Fatalf("last_message_at = %v (%v), want %v", last, err, base.Add(10*time.Minute))
	}
	// Our own message and an empty author are stored as given.
	own := groupMsg(connA, jid1, "m2", "eu", base.Add(20*time.Minute))
	own.FromMe, own.AuthorName, own.AuthorJID = true, "Você", ""
	if ing, _ := process(connA, "k3", own); !ing {
		t.Fatal("own message not stored")
	}
	if n := e.count(`SELECT count(*) FROM wa_group_messages WHERE group_id=$1 AND from_me AND author_name='Você'`, g1); n != 1 {
		t.Fatalf("own message flag/author: %d", n)
	}
}

// Codex H-02 / ADR-0038: a suspended company stores nothing from its groups either; after reactivation the same
// message is stored (nothing, not even the dedup record, was kept while suspended).
func TestIntakeDropsGroupMessagesOfASuspendedCompany(t *testing.T) {
	e := newEnv(t)
	tenant, conn := e.tenant()
	g := e.group(tenant, conn, jid1, "Oficial", true)
	intake := NewIntake(e.app, channeladapters.NewPostgresWebhookEventStore(e.app))
	msg := groupMsg(conn, jid1, "m-susp", "durante a suspensao", time.Now().UTC().Add(-time.Minute))

	if _, err := e.seed.Exec(e.ctx, `UPDATE tenants SET status='suspended' WHERE id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	ing, dup, err := intake.ProcessGroupMessage(e.ctx, conn, "k-susp", "message.any", "digest-k-susp", msg)
	if err != nil || ing || dup {
		t.Fatalf("suspended: ingested=%v duplicate=%v err=%v, want a silent drop", ing, dup, err)
	}
	if n := e.count(`SELECT count(*) FROM wa_group_messages WHERE group_id=$1`, g); n != 0 {
		t.Fatalf("a suspended company stored %d group messages", n)
	}
	if n := e.count(`SELECT count(*) FROM channel_webhook_events WHERE provider_event_id=$1`, "k-susp"); n != 0 {
		t.Fatalf("a suspended company left %d dedup records", n)
	}
	if _, err := e.seed.Exec(e.ctx, `UPDATE tenants SET status='active' WHERE id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if ing, dup, err := intake.ProcessGroupMessage(e.ctx, conn, "k-susp", "message.any", "digest-k-susp", msg); err != nil || !ing || dup {
		t.Fatalf("after reactivation: ingested=%v duplicate=%v err=%v", ing, dup, err)
	}
}

func TestGroupAPIPermissionsEnableDisableCursorAndDeleteAreTenantScoped(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	supervisor := e.member(tenantA, "tenant_supervisor", "active")
	agent := e.member(tenantA, "tenant_agent", "active")
	revoked := e.member(tenantA, "tenant_admin", "revoked")
	adminB := e.member(tenantB, "tenant_admin", "active")

	dir := &fakeDir{conn: connA.ID, groups: []channeldomain.ProviderGroup{
		{JID: jid1, Name: "0 - K3G Solutions Oficial", ParticipantCount: 14},
		{JID: jid2, Name: "Cobrança", ParticipantCount: 5},
		{JID: jid3, Name: "Amigos", ParticipantCount: 30},
		{JID: "not-a-group", Name: "Lixo"},
	}}
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), dir)
	clock := time.Now()
	h.now = func() time.Time { return clock }

	as := func(tenant, user uuid.UUID, method, target, body string, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, method, target, body, path, fn)
	}
	audits := func(action string) int {
		return e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenantA, action)
	}

	// Permissions: the plain agent has neither; a revoked admin has nothing; supervisor and admin have both.
	for name, u := range map[string]uuid.UUID{"agent": agent, "revoked": revoked} {
		for label, rec := range map[string]*httptest.ResponseRecorder{
			"list":      as(tenantA, u, http.MethodGet, "/", "", nil, h.List),
			"available": as(tenantA, u, http.MethodGet, "/", "", nil, h.Available),
			"enable":    as(tenantA, u, http.MethodPost, "/", `{"provider_group_id":"`+jid1+`"}`, nil, h.Enable),
		} {
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s/%s = %d, want 403", name, label, rec.Code)
			}
		}
	}
	if e.count(`SELECT count(*) FROM wa_groups WHERE tenant_id=$1`, tenantA) != 0 {
		t.Fatal("a refused enable created a group")
	}

	// Available: filters out ids that are not groups, sorts, searches and caps; nothing enabled yet.
	var avail struct {
		Items []availableItem `json:"items"`
		Total int             `json:"total"`
	}
	rec := as(tenantA, supervisor, http.MethodGet, "/", "", nil, h.Available)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &avail) != nil || avail.Total != 3 || avail.Items[0].Name != "0 - K3G Solutions Oficial" || avail.Items[0].Enabled {
		t.Fatalf("available = %d %s", rec.Code, rec.Body.String())
	}
	rec = as(tenantA, supervisor, http.MethodGet, "/?q=COBR&limit=1", "", nil, h.Available)
	if json.Unmarshal(rec.Body.Bytes(), &avail); len(avail.Items) != 1 || avail.Items[0].ProviderGroupID != jid2 {
		t.Fatalf("search: %s", rec.Body.String())
	}
	if rec := as(tenantA, supervisor, http.MethodGet, "/?limit=0", "", nil, h.Available); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=0: %d", rec.Code)
	}
	if dir.calls != 1 {
		t.Fatalf("the provider listing must be cached for a minute, got %d calls", dir.calls)
	}

	// Enable: validated, must exist in the account, audited; a repeat keeps the row.
	for name, body := range map[string]string{"missing": `{}`, "not a group": `{"provider_group_id":"5511999999999@c.us"}`, "unknown field": `{"provider_group_id":"` + jid1 + `","tenant_id":"` + tenantB.String() + `"}`, "not json": `x`} {
		if rec := as(tenantA, admin, http.MethodPost, "/", body, nil, h.Enable); rec.Code != http.StatusBadRequest {
			t.Errorf("enable %s: %d, want 400", name, rec.Code)
		}
	}
	if rec := as(tenantA, admin, http.MethodPost, "/", `{"provider_group_id":"120363999999999999@g.us"}`, nil, h.Enable); rec.Code != http.StatusNotFound {
		t.Fatalf("a group outside the account: %d, want 404", rec.Code)
	}
	var created groupItem
	rec = as(tenantA, admin, http.MethodPost, "/", `{"provider_group_id":"`+jid1+`"}`, nil, h.Enable)
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &created) != nil || !created.Enabled || created.Name != "0 - K3G Solutions Oficial" {
		t.Fatalf("enable = %d %s", rec.Code, rec.Body.String())
	}
	gid := created.ID
	if audits("group.enabled") != 1 {
		t.Fatalf("group.enabled audits = %d", audits("group.enabled"))
	}
	if rec := as(tenantA, admin, http.MethodPost, "/", `{"provider_group_id":"`+jid1+`"}`, nil, h.Enable); rec.Code != http.StatusOK {
		t.Fatalf("enable again = %d, want 200", rec.Code)
	}
	if e.count(`SELECT count(*) FROM wa_groups WHERE tenant_id=$1`, tenantA) != 1 {
		t.Fatal("enabling twice created a second row")
	}
	// A directory failure is reported, not hidden.
	dir.err = ErrNoConnection
	clock = clock.Add(2 * directoryTTL)
	if rec := as(tenantA, admin, http.MethodGet, "/", "", nil, h.Available); rec.Code != http.StatusConflict {
		t.Fatalf("no connection: %d, want 409", rec.Code)
	}
	dir.err = errors.New("waha down")
	if rec := as(tenantA, admin, http.MethodGet, "/", "", nil, h.Available); rec.Code != http.StatusBadGateway {
		t.Fatalf("provider down: %d, want 502", rec.Code)
	}
	dir.err = nil

	// Messages: seed five, page newest first with a small limit, no gaps, no repeats.
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	want := []string{}
	for i := 0; i < 5; i++ {
		id := uuid.New()
		e.exec(`INSERT INTO wa_group_messages(id,tenant_id,group_id,provider_message_id,author_name,message_type,body,sent_at) VALUES($1,$2,$3,$4,'Rafael','text',$5,$6)`,
			id, tenantA, gid, fmt.Sprintf("m%d", i), fmt.Sprintf("texto %d", i), base.Add(time.Duration(i)*time.Minute))
		want = append([]string{id.String()}, want...)
	}
	e.exec(`UPDATE wa_groups SET last_message_at=$2 WHERE id=$1`, gid, base.Add(4*time.Minute))
	var walked []string
	cursor := ""
	for i := 0; i < 8; i++ {
		q := "/?limit=2"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := as(tenantA, supervisor, http.MethodGet, q, "", map[string]string{"group_id": gid.String()}, h.ListMessages)
		var page struct {
			Items      []messageItem `json:"items"`
			HasMore    bool          `json:"has_more"`
			NextCursor string        `json:"next_cursor"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			t.Fatalf("page %d: %d %s", i, rec.Code, rec.Body.String())
		}
		for _, m := range page.Items {
			walked = append(walked, m.ID.String())
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if fmt.Sprint(walked) != fmt.Sprint(want) {
		t.Fatalf("message walk %v, want newest first %v", walked, want)
	}
	// The list of enabled groups carries the last message preview.
	var list struct {
		Items []groupItem `json:"items"`
	}
	rec = as(tenantA, supervisor, http.MethodGet, "/", "", nil, h.List)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Items[0].LastMessage == nil || list.Items[0].LastMessage.Preview != "texto 4" {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}

	// Tenant B: its admin sees none of A's groups, and A's group id behaves exactly like an unknown id.
	e.group(tenantB, connB, jid3, "Só do B", true)
	rec = as(tenantB, adminB, http.MethodGet, "/", "", nil, h.List)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Items[0].Name != "Só do B" {
		t.Fatalf("tenant B list = %d %s", rec.Code, rec.Body.String())
	}
	foreign := as(tenantB, adminB, http.MethodGet, "/", "", map[string]string{"group_id": gid.String()}, h.ListMessages)
	unknown := as(tenantB, adminB, http.MethodGet, "/", "", map[string]string{"group_id": uuid.NewString()}, h.ListMessages)
	if foreign.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound || foreign.Body.String() != unknown.Body.String() {
		t.Fatalf("enumeration oracle: %d %q vs %d %q", foreign.Code, foreign.Body.String(), unknown.Code, unknown.Body.String())
	}
	if rec := as(tenantB, adminB, http.MethodPatch, "/", `{"enabled":false}`, map[string]string{"group_id": gid.String()}, h.SetEnabled); rec.Code != http.StatusNotFound {
		t.Fatalf("B disabling A's group: %d, want 404", rec.Code)
	}
	if rec := as(tenantB, adminB, http.MethodDelete, "/", "", map[string]string{"group_id": gid.String()}, h.DeleteHistory); rec.Code != http.StatusNotFound {
		t.Fatalf("B deleting A's history: %d, want 404", rec.Code)
	}
	if e.count(`SELECT count(*) FROM wa_group_messages WHERE group_id=$1`, gid) != 5 {
		t.Fatal("tenant B touched tenant A's history")
	}

	// Disable stops storing but keeps the history; it is audited once, and idempotent.
	path := map[string]string{"group_id": gid.String()}
	for name, body := range map[string]string{"missing": `{}`, "wrong type": `{"enabled":"yes"}`, "unknown field": `{"enabled":false,"name":"x"}`} {
		if rec := as(tenantA, admin, http.MethodPatch, "/", body, path, h.SetEnabled); rec.Code != http.StatusBadRequest {
			t.Errorf("disable %s: %d, want 400", name, rec.Code)
		}
	}
	if rec := as(tenantA, agent, http.MethodPatch, "/", `{"enabled":false}`, path, h.SetEnabled); rec.Code != http.StatusForbidden {
		t.Fatalf("agent disabling: %d, want 403", rec.Code)
	}
	for i := 0; i < 2; i++ {
		if rec := as(tenantA, admin, http.MethodPatch, "/", `{"enabled":false}`, path, h.SetEnabled); rec.Code != http.StatusOK {
			t.Fatalf("disable #%d = %d", i, rec.Code)
		}
	}
	if audits("group.disabled") != 1 || e.count(`SELECT count(*) FROM wa_group_messages WHERE group_id=$1`, gid) != 5 {
		t.Fatalf("disable: audits=%d, history must stay", audits("group.disabled"))
	}
	intake := NewIntake(e.app, channeladapters.NewPostgresWebhookEventStore(e.app))
	if ing, dup, err := intake.ProcessGroupMessage(e.ctx, connA, "after-off", "message.any", "d", groupMsg(connA, jid1, "late", "depois", time.Now())); err != nil || ing || dup {
		t.Fatalf("a disabled group must stop storing: ing=%v dup=%v err=%v", ing, dup, err)
	}
	// The disabled group's history is still readable until it is deleted.
	if rec := as(tenantA, supervisor, http.MethodGet, "/", "", path, h.ListMessages); rec.Code != 200 {
		t.Fatalf("history of a disabled group: %d", rec.Code)
	}

	// Delete history: manage only, audited with the count, leaves the group row.
	if rec := as(tenantA, agent, http.MethodDelete, "/", "", path, h.DeleteHistory); rec.Code != http.StatusForbidden {
		t.Fatalf("agent deleting history: %d", rec.Code)
	}
	rec = as(tenantA, admin, http.MethodDelete, "/", "", path, h.DeleteHistory)
	if rec.Code != 200 || e.count(`SELECT count(*) FROM wa_group_messages WHERE group_id=$1`, gid) != 0 || e.count(`SELECT count(*) FROM wa_groups WHERE id=$1 AND last_message_at IS NULL`, gid) != 1 {
		t.Fatalf("delete history = %d %s", rec.Code, rec.Body.String())
	}
	var deleted int
	if err := e.seed.QueryRow(e.ctx, `SELECT (metadata->>'deleted')::int FROM audit_events WHERE tenant_id=$1 AND action='group.history_deleted'`, tenantA).Scan(&deleted); err != nil || deleted != 5 {
		t.Fatalf("audit deleted = %d (%v)", deleted, err)
	}
	// Deleting the history also asks the archive job to remove the group's cold files (the API container
	// cannot reach the external disk, so it leaves the request on the group).
	if e.count(`SELECT count(*) FROM wa_groups WHERE id=$1 AND archive_purge_requested_at IS NOT NULL`, gid) != 1 {
		t.Fatal("delete history must request the purge of the group's archived files")
	}
	if e.count(`SELECT count(*) FROM wa_groups WHERE tenant_id=$1 AND id<>$2 AND archive_purge_requested_at IS NOT NULL`, tenantB, gid) != 0 {
		t.Fatal("tenant B's groups must not be touched")
	}
}

// purpose (migration 000176) is a manual, closed-list tag an administrator picks per group, independent of
// enabled/disabled and of the automatic conversation_kind. Validated, audited, tenant-scoped, idempotent.
func TestGroupSetPurposeIsValidatedAuditedAndTenantScoped(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, _ := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	agent := e.member(tenantA, "tenant_agent", "active")
	adminB := e.member(tenantB, "tenant_admin", "active")
	gid := e.group(tenantA, connA, jid1, "Suporte ClienteX", true)

	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{})
	as := func(tenant, user uuid.UUID, body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodPatch, "/", body, map[string]string{"group_id": gid.String()}, h.SetEnabled)
	}
	audits := func(action string) int {
		return e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenantA, action)
	}

	// New group defaults to "other" until someone tags it.
	if got := e.count(`SELECT count(*) FROM wa_groups WHERE id=$1 AND purpose='other'`, gid); got != 1 {
		t.Fatalf("default purpose must be other, query matched %d rows", got)
	}

	// Validated: an unknown value is rejected before anything is written.
	if rec := as(tenantA, admin, `{"purpose":"made_up"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid purpose: %d, want 400", rec.Code)
	}

	// Permission: group.manage only, same as enable/disable.
	if rec := as(tenantA, agent, `{"purpose":"client_support"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("agent setting purpose: %d, want 403", rec.Code)
	}

	// Set: 200, reflected on the response, audited once with from/to.
	var it groupItem
	rec := as(tenantA, admin, `{"purpose":"client_support"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &it) != nil || it.Purpose != "client_support" || !it.Enabled {
		t.Fatalf("set purpose = %d %s", rec.Code, rec.Body.String())
	}
	if audits("group.purpose_set") != 1 {
		t.Fatalf("group.purpose_set audits = %d", audits("group.purpose_set"))
	}
	var from, to string
	if err := e.seed.QueryRow(e.ctx, `SELECT metadata->>'from', metadata->>'to' FROM audit_events WHERE tenant_id=$1 AND action='group.purpose_set'`, tenantA).Scan(&from, &to); err != nil || from != "other" || to != "client_support" {
		t.Fatalf("audit from/to = %q/%q (%v)", from, to, err)
	}

	// Idempotent: re-sending the same purpose changes nothing and is not re-audited.
	rec = as(tenantA, admin, `{"purpose":"client_support"}`)
	if rec.Code != http.StatusOK || audits("group.purpose_set") != 1 {
		t.Fatalf("repeat set: %d, audits=%d, want no new audit", rec.Code, audits("group.purpose_set"))
	}

	// enabled and purpose can change together in one call, each audited under its own action.
	rec = as(tenantA, admin, `{"enabled":false,"purpose":"internal_team"}`)
	if json.Unmarshal(rec.Body.Bytes(), &it); rec.Code != http.StatusOK || it.Enabled || it.Purpose != "internal_team" {
		t.Fatalf("combined update = %d %s", rec.Code, rec.Body.String())
	}
	if audits("group.purpose_set") != 2 || audits("group.disabled") != 1 {
		t.Fatalf("combined audits: purpose_set=%d disabled=%d", audits("group.purpose_set"), audits("group.disabled"))
	}

	// Tenant B: A's group id behaves exactly like an unknown one.
	if rec := call(t, e.app, tenantB, adminB, http.MethodPatch, "/", `{"purpose":"other"}`, map[string]string{"group_id": gid.String()}, h.SetEnabled); rec.Code != http.StatusNotFound {
		t.Fatalf("B setting A's group purpose: %d, want 404", rec.Code)
	}
	if got := e.count(`SELECT count(*) FROM wa_groups WHERE id=$1 AND purpose='internal_team'`, gid); got != 1 {
		t.Fatal("tenant B must not have changed tenant A's group purpose")
	}
}

// ai_observer_enabled (ADR-0055, migration 000178): suggest-only AI classification, opt-in per group,
// independent of purpose, but never on for a group that is not reading — there would be nothing to observe.
func TestGroupAIObserverToggleRequiresReadingAndIsAudited(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, _ := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	agent := e.member(tenantA, "tenant_agent", "active")
	adminB := e.member(tenantB, "tenant_admin", "active")
	gid := e.group(tenantA, connA, jid1, "Homologação", true)

	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{})
	as := func(tenant, user uuid.UUID, body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodPatch, "/", body, map[string]string{"group_id": gid.String()}, h.SetEnabled)
	}
	audits := func(action string) int {
		return e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenantA, action)
	}

	// New group defaults to the observer off.
	if got := e.count(`SELECT count(*) FROM wa_groups WHERE id=$1 AND NOT ai_observer_enabled`, gid); got != 1 {
		t.Fatal("default must be ai_observer_enabled=false")
	}

	// Permission: group.manage only, same as everything else SetEnabled does.
	if rec := as(tenantA, agent, `{"ai_observer_enabled":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("agent toggling observer: %d, want 403", rec.Code)
	}

	// Turn it on: 200, reflected, audited.
	var it groupItem
	rec := as(tenantA, admin, `{"ai_observer_enabled":true}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &it) != nil || !it.AIObserverEnabled {
		t.Fatalf("enable observer = %d %s", rec.Code, rec.Body.String())
	}
	if audits("group.ai_observer_enabled") != 1 {
		t.Fatalf("group.ai_observer_enabled audits = %d", audits("group.ai_observer_enabled"))
	}

	// Idempotent: resending the same value changes nothing and is not re-audited.
	rec = as(tenantA, admin, `{"ai_observer_enabled":true}`)
	if rec.Code != http.StatusOK || audits("group.ai_observer_enabled") != 1 {
		t.Fatalf("repeat enable: %d, audits=%d, want no new audit", rec.Code, audits("group.ai_observer_enabled"))
	}

	// Turning reading off while the observer would stay on is rejected outright — nothing silently cascades;
	// the caller must say explicitly what happens to the observer in the same request.
	if rec := as(tenantA, admin, `{"enabled":false}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("disable reading while observer stays on: %d, want 400", rec.Code)
	}
	rec = as(tenantA, admin, `{"enabled":false,"ai_observer_enabled":false}`)
	if rec.Code != http.StatusOK || audits("group.ai_observer_disabled") != 1 || audits("group.disabled") != 1 {
		t.Fatalf("disable reading together with the observer: %d, observer audits=%d, disabled audits=%d",
			rec.Code, audits("group.ai_observer_disabled"), audits("group.disabled"))
	}

	gid2 := e.group(tenantA, connA, jid2, "Outro, desligado", false)
	as2 := func(body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenantA, admin, http.MethodPatch, "/", body, map[string]string{"group_id": gid2.String()}, h.SetEnabled)
	}
	if rec := as2(`{"ai_observer_enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("observer on, reading off (and staying off): %d, want 400", rec.Code)
	}
	if rec := as2(`{"enabled":true,"ai_observer_enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("observer on together with turning reading on: %d, want 200", rec.Code)
	}

	// Disabling the observer alone, with reading left on, is its own audited action too.
	if rec := as2(`{"ai_observer_enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable observer alone: %d", rec.Code)
	}
	if n := audits("group.ai_observer_disabled"); n != 2 {
		t.Fatalf("group.ai_observer_disabled audits = %d, want 2 (gid's combined disable + gid2's standalone one)", n)
	}

	// Tenant B: A's group id behaves exactly like an unknown one.
	if rec := call(t, e.app, tenantB, adminB, http.MethodPatch, "/", `{"ai_observer_enabled":true}`, map[string]string{"group_id": gid.String()}, h.SetEnabled); rec.Code != http.StatusNotFound {
		t.Fatalf("B toggling A's group observer: %d, want 404", rec.Code)
	}
}

// ADR-0058: is_alert_channel is the one exception to "nothing is ever sent to a group" — kept as narrow as the
// database itself can make it. Only one group per tenant, never a group linked to a customer account, in
// either order of the two updates, and requires the group to actually be read.
func TestGroupAlertChannelIsValidatedAuditedAndTenantScoped(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, _ := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	agent := e.member(tenantA, "tenant_agent", "active")
	adminB := e.member(tenantB, "tenant_admin", "active")
	account := e.account(tenantA, "Cliente A")
	gid := e.group(tenantA, connA, jid1, "Canal interno", true)
	gid2 := e.group(tenantA, connA, jid2, "Outro grupo", true)

	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{})
	setEnabled := func(tenant, user, gid uuid.UUID, body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodPatch, "/", body, map[string]string{"group_id": gid.String()}, h.SetEnabled)
	}
	link := func(tenant, user, gid uuid.UUID, body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodPut, "/", body, map[string]string{"group_id": gid.String()}, h.LinkAccount)
	}
	audits := func(action string) int {
		return e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenantA, action)
	}

	// New group defaults to no alert channel.
	if got := e.count(`SELECT count(*) FROM wa_groups WHERE id=$1 AND NOT is_alert_channel`, gid); got != 1 {
		t.Fatal("default must be is_alert_channel=false")
	}

	// Permission: group.manage only.
	if rec := setEnabled(tenantA, agent, gid, `{"is_alert_channel":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("agent toggling alert channel: %d, want 403", rec.Code)
	}

	// Cannot turn it on while reading is off (and staying off).
	g3 := e.group(tenantA, connA, jid3, "Lido depois", false)
	if rec := setEnabled(tenantA, admin, g3, `{"is_alert_channel":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("alert channel without reading: %d, want 400", rec.Code)
	}

	// Turn it on: 200, reflected, audited.
	var it groupItem
	rec := setEnabled(tenantA, admin, gid, `{"is_alert_channel":true}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &it) != nil || !it.IsAlertChannel {
		t.Fatalf("enable alert channel = %d %s", rec.Code, rec.Body.String())
	}
	if audits("group.alert_channel_enabled") != 1 {
		t.Fatalf("group.alert_channel_enabled audits = %d", audits("group.alert_channel_enabled"))
	}
	// Idempotent: resending the same value changes nothing and is not re-audited.
	if rec := setEnabled(tenantA, admin, gid, `{"is_alert_channel":true}`); rec.Code != http.StatusOK || audits("group.alert_channel_enabled") != 1 {
		t.Fatalf("repeat enable: %d, audits=%d", rec.Code, audits("group.alert_channel_enabled"))
	}

	// Only one group per tenant: a second group cannot also become the alert channel.
	if rec := setEnabled(tenantA, admin, gid2, `{"is_alert_channel":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("second group as alert channel: %d, want 409", rec.Code)
	}

	// Never a group linked to a customer account, in either order.
	e.exec(`UPDATE wa_groups SET is_alert_channel=false WHERE id=$1`, gid)
	if rec := link(tenantA, admin, gid2, `{"account_id":"`+account.String()+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("link account to gid2: %d %s", rec.Code, rec.Body.String())
	}
	if rec := setEnabled(tenantA, admin, gid2, `{"is_alert_channel":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("alert channel on a client-linked group: %d, want 409", rec.Code)
	}
	if rec := setEnabled(tenantA, admin, gid, `{"is_alert_channel":true}`); rec.Code != http.StatusOK {
		t.Fatalf("re-enable on the unlinked group: %d %s", rec.Code, rec.Body.String())
	}
	if rec := link(tenantA, admin, gid, `{"account_id":"`+account.String()+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("linking an account to the current alert channel: %d, want 409", rec.Code)
	}

	// Disabling is its own audited action.
	if rec := setEnabled(tenantA, admin, gid, `{"is_alert_channel":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable alert channel: %d", rec.Code)
	}
	if audits("group.alert_channel_disabled") != 1 {
		t.Fatalf("group.alert_channel_disabled audits = %d", audits("group.alert_channel_disabled"))
	}

	// Tenant B: A's group id behaves exactly like an unknown one.
	if rec := setEnabled(tenantB, adminB, gid, `{"is_alert_channel":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("B toggling A's group alert channel: %d, want 404", rec.Code)
	}
}

// ADR-0056: ListMessages surfaces a stored suggestion's suggested_action alongside category/summary/confidence,
// and a message with no suggestion yet carries a nil Suggestion — same JSON shape as before this ADR, just one
// more field.
func TestGroupMessageSuggestionIncludesSuggestedAction(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	gid := e.group(tenantA, connA, jid1, "Homologação", true)

	withSuggestion := uuid.New()
	e.exec(`INSERT INTO wa_group_messages(id,tenant_id,group_id,provider_message_id,author_name,message_type,body,sent_at) VALUES($1,$2,$3,'m1','Rafael','text','preciso de ajuda',now())`,
		withSuggestion, tenantA, gid)
	e.exec(`INSERT INTO wa_group_message_suggestions(tenant_id,group_message_id,category,summary,suggested_action,confidence,model_provider,model_name,latency_ms)
		VALUES($1,$2,'request','Pedido de ajuda','verificar o que o cliente precisa',0.75,'fake','mini',10)`, tenantA, withSuggestion)
	withoutSuggestion := uuid.New()
	e.exec(`INSERT INTO wa_group_messages(id,tenant_id,group_id,provider_message_id,author_name,message_type,body,sent_at) VALUES($1,$2,$3,'m2','Rafael','text','oi',now() + interval '1 minute')`,
		withoutSuggestion, tenantA, gid)

	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{})
	rec := call(t, e.app, tenantA, admin, http.MethodGet, "/", "", map[string]string{"group_id": gid.String()}, h.ListMessages)
	var page struct {
		Items []messageItem `json:"items"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 2 {
		t.Fatalf("list messages: %d %s", rec.Code, rec.Body.String())
	}
	byID := map[uuid.UUID]messageItem{}
	for _, m := range page.Items {
		byID[m.ID] = m
	}
	got := byID[withSuggestion].Suggestion
	if got == nil || got.Category != "request" || got.Summary != "Pedido de ajuda" || got.SuggestedAction != "verificar o que o cliente precisa" || got.Confidence != 0.75 {
		t.Fatalf("suggestion = %+v", got)
	}
	if byID[withoutSuggestion].Suggestion != nil {
		t.Fatalf("message without a suggestion must carry nil, got %+v", byID[withoutSuggestion].Suggestion)
	}
}

// account_id (migration 000177) links a group to the "cliente final" it belongs to, reusing customer_accounts
// (ADR-0018) instead of a second company catalog. Validated against the SAME tenant, audited, idempotent,
// and List's account_id filter finds both "this client's groups" and "groups nobody linked yet".
func TestGroupAccountLinkIsValidatedAuditedAndTenantScoped(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	agent := e.member(tenantA, "tenant_agent", "active")
	adminB := e.member(tenantB, "tenant_admin", "active")
	accountA := e.account(tenantA, "Cliente A")
	accountB := e.account(tenantB, "Cliente B")
	g1 := e.group(tenantA, connA, jid1, "Grupo 1", true)
	g2 := e.group(tenantA, connA, jid2, "Grupo 2", true)
	e.group(tenantB, connB, jid3, "Grupo de B", true)

	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{})
	link := func(tenant, user, gid uuid.UUID, body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodPut, "/", body, map[string]string{"group_id": gid.String()}, h.LinkAccount)
	}
	unlink := func(tenant, user, gid uuid.UUID) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodDelete, "/", "", map[string]string{"group_id": gid.String()}, h.UnlinkAccount)
	}
	audits := func(action string) int {
		return e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenantA, action)
	}

	// Validated: not JSON, missing, and not-a-uuid are all 400 before anything is written.
	for name, body := range map[string]string{"missing": `{}`, "not a uuid": `{"account_id":"nope"}`, "not json": `x`} {
		if rec := link(tenantA, admin, g1, body); rec.Code != http.StatusBadRequest {
			t.Errorf("link %s: %d, want 400", name, rec.Code)
		}
	}
	// An account id that does not exist in THIS tenant (including a real id from tenant B): 404.
	if rec := link(tenantA, admin, g1, `{"account_id":"`+uuid.NewString()+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d, want 404", rec.Code)
	}
	if rec := link(tenantA, admin, g1, `{"account_id":"`+accountB.String()+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("tenant B's real account from tenant A: %d, want 404", rec.Code)
	}

	// Permission: group.manage only.
	if rec := link(tenantA, agent, g1, `{"account_id":"`+accountA.String()+`"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("agent linking: %d, want 403", rec.Code)
	}

	// Link: 200, reflected on the response (id and the account's own name), audited once.
	var it groupItem
	rec := link(tenantA, admin, g1, `{"account_id":"`+accountA.String()+`"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &it) != nil || it.AccountID == nil || *it.AccountID != accountA || it.AccountName == nil || *it.AccountName != "Cliente A" {
		t.Fatalf("link = %d %s", rec.Code, rec.Body.String())
	}
	if audits("group.account_linked") != 1 {
		t.Fatalf("group.account_linked audits = %d", audits("group.account_linked"))
	}
	// Idempotent: linking the same account again changes nothing and is not re-audited.
	if rec := link(tenantA, admin, g1, `{"account_id":"`+accountA.String()+`"}`); rec.Code != http.StatusOK || audits("group.account_linked") != 1 {
		t.Fatalf("repeat link: %d, audits=%d", rec.Code, audits("group.account_linked"))
	}

	// List's account_id filter: this client's groups, and the ones nobody linked yet.
	var list struct {
		Items []groupItem `json:"items"`
	}
	rec = call(t, e.app, tenantA, admin, http.MethodGet, "/?account_id="+accountA.String(), "", nil, h.List)
	if json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != g1 {
		t.Fatalf("list by account = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, e.app, tenantA, admin, http.MethodGet, "/?account_id=none", "", nil, h.List)
	if json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != g2 {
		t.Fatalf("list unlinked = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, e.app, tenantA, admin, http.MethodGet, "/?account_id=not-a-uuid", "", nil, h.List); rec.Code != http.StatusBadRequest {
		t.Fatalf("list bad account_id: %d, want 400", rec.Code)
	}

	// Tenant B: A's group id behaves exactly like an unknown one for link and unlink alike.
	if rec := link(tenantB, adminB, g1, `{"account_id":"`+accountB.String()+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("B linking A's group: %d, want 404", rec.Code)
	}
	if rec := unlink(tenantB, adminB, g1); rec.Code != http.StatusNotFound {
		t.Fatalf("B unlinking A's group: %d, want 404", rec.Code)
	}
	if got := e.count(`SELECT count(*) FROM wa_groups WHERE id=$1 AND account_id=$2`, g1, accountA); got != 1 {
		t.Fatal("tenant B must not have changed tenant A's group account link")
	}

	// Unlink: 200, audited, idempotent; the group remains (only the link is cleared).
	if rec := unlink(tenantA, agent, g1); rec.Code != http.StatusForbidden {
		t.Fatalf("agent unlinking: %d, want 403", rec.Code)
	}
	rec = unlink(tenantA, admin, g1)
	if json.Unmarshal(rec.Body.Bytes(), &it); rec.Code != http.StatusOK || it.AccountID != nil {
		t.Fatalf("unlink = %d %s", rec.Code, rec.Body.String())
	}
	if audits("group.account_unlinked") != 1 {
		t.Fatalf("group.account_unlinked audits = %d", audits("group.account_unlinked"))
	}
	if rec := unlink(tenantA, admin, g1); rec.Code != http.StatusOK || audits("group.account_unlinked") != 1 {
		t.Fatalf("repeat unlink: %d, audits=%d, want no new audit", rec.Code, audits("group.account_unlinked"))
	}
	if e.count(`SELECT count(*) FROM wa_groups WHERE id=$1`, g1) != 1 {
		t.Fatal("unlinking must not delete the group")
	}
}

// TestGroupAdditionalAccountsAllowMoreThanOneClientPerGroup proves ADR-0059 addendum 8: some groups (a
// "swap"/peering group between two of the tenant's own clients, where the tenant operates on behalf of
// both) genuinely concern more than one CRM client at once. The additional-accounts table is a pure N:N
// side table — it must never touch wa_groups.account_id (the group's ONE primary client), must refuse to
// duplicate the primary into the additional set, must be idempotent, audited, and tenant-scoped exactly
// like the primary link.
func TestGroupAdditionalAccountsAllowMoreThanOneClientPerGroup(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	agent := e.member(tenantA, "tenant_agent", "active")
	adminB := e.member(tenantB, "tenant_admin", "active")
	primary := e.account(tenantA, "InfoRR Consultoria")
	other := e.account(tenantA, "IP Fibra")
	accountB := e.account(tenantB, "Cliente B")
	g := e.group(tenantA, connA, jid1, "Swap - InfoRR x IP Fibra", true)
	e.group(tenantB, connB, jid3, "Grupo de B", true)

	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{})
	link := func(tenant, user, gid uuid.UUID, body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodPut, "/", body, map[string]string{"group_id": gid.String()}, h.LinkAccount)
	}
	add := func(tenant, user, gid uuid.UUID, body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodPost, "/", body, map[string]string{"group_id": gid.String()}, h.AddAdditionalAccount)
	}
	remove := func(tenant, user, gid, aid uuid.UUID) *httptest.ResponseRecorder {
		return call(t, e.app, tenant, user, http.MethodDelete, "/", "", map[string]string{"group_id": gid.String(), "account_id": aid.String()}, h.RemoveAdditionalAccount)
	}
	audits := func(action string) int {
		return e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenantA, action)
	}

	// The group has no primary client yet: adding an additional one still works (independent axis).
	if rec := add(tenantA, agent, g, `{"account_id":"`+other.String()+`"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("agent adding: %d, want 403", rec.Code)
	}
	var it groupItem
	rec := add(tenantA, admin, g, `{"account_id":"`+other.String()+`"}`)
	if json.Unmarshal(rec.Body.Bytes(), &it); rec.Code != http.StatusOK || len(it.AdditionalAccounts) != 1 || it.AdditionalAccounts[0].ID != other || it.AdditionalAccounts[0].Name != "IP Fibra" {
		t.Fatalf("add = %d %s", rec.Code, rec.Body.String())
	}
	if it.AccountID != nil {
		t.Fatalf("adding an additional account must never set the primary: got %v", it.AccountID)
	}
	if audits("group.additional_account_linked") != 1 {
		t.Fatalf("group.additional_account_linked audits = %d", audits("group.additional_account_linked"))
	}
	// Idempotent: adding the same one again changes nothing, no new audit.
	if rec := add(tenantA, admin, g, `{"account_id":"`+other.String()+`"}`); rec.Code != http.StatusOK || audits("group.additional_account_linked") != 1 {
		t.Fatalf("repeat add: %d, audits=%d", rec.Code, audits("group.additional_account_linked"))
	}

	// Now set the primary (InfoRR) — both the primary link and the additional one coexist.
	if rec := link(tenantA, admin, g, `{"account_id":"`+primary.String()+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("link primary: %d %s", rec.Code, rec.Body.String())
	}
	// Adding the PRIMARY as an additional account is refused (redundant/ambiguous).
	if rec := add(tenantA, admin, g, `{"account_id":"`+primary.String()+`"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("adding the primary as additional: %d, want 422", rec.Code)
	}
	rec = call(t, e.app, tenantA, admin, http.MethodGet, "/?account_id="+primary.String(), "", nil, h.List)
	var list struct {
		Items []groupItem `json:"items"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != 200 || len(list.Items) != 1 || len(list.Items[0].AdditionalAccounts) != 1 || list.Items[0].AdditionalAccounts[0].ID != other {
		t.Fatalf("list reflects both primary and additional: %d %s", rec.Code, rec.Body.String())
	}

	// Unknown account / a real account from tenant B: 404, never silently cross tenants.
	if rec := add(tenantA, admin, g, `{"account_id":"`+uuid.NewString()+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d, want 404", rec.Code)
	}
	if rec := add(tenantA, admin, g, `{"account_id":"`+accountB.String()+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("tenant B's account from tenant A: %d, want 404", rec.Code)
	}
	// Tenant B cannot add to, or remove from, A's group — behaves exactly like an unknown group.
	if rec := add(tenantB, adminB, g, `{"account_id":"`+accountB.String()+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("B adding to A's group: %d, want 404", rec.Code)
	}
	if rec := remove(tenantB, adminB, g, other); rec.Code != http.StatusNotFound {
		t.Fatalf("B removing from A's group: %d, want 404", rec.Code)
	}
	if e.count(`SELECT count(*) FROM wa_group_additional_accounts WHERE group_id=$1`, g) != 1 {
		t.Fatal("tenant B must not have changed tenant A's additional accounts")
	}

	// Remove: 200, audited, idempotent; the primary link is untouched.
	if rec := remove(tenantA, agent, g, other); rec.Code != http.StatusForbidden {
		t.Fatalf("agent removing: %d, want 403", rec.Code)
	}
	rec = remove(tenantA, admin, g, other)
	if json.Unmarshal(rec.Body.Bytes(), &it); rec.Code != http.StatusOK || len(it.AdditionalAccounts) != 0 {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	if it.AccountID == nil || *it.AccountID != primary {
		t.Fatalf("removing an additional account must never touch the primary: got %v", it.AccountID)
	}
	if audits("group.additional_account_unlinked") != 1 {
		t.Fatalf("group.additional_account_unlinked audits = %d", audits("group.additional_account_unlinked"))
	}
	if rec := remove(tenantA, admin, g, other); rec.Code != http.StatusOK || audits("group.additional_account_unlinked") != 1 {
		t.Fatalf("repeat remove: %d, audits=%d, want no new audit", rec.Code, audits("group.additional_account_unlinked"))
	}
}

// A tenant running more than one WhatsApp instance at once (e.g. a second number connected to train on
// one group before mixing it with the main line) must never have its groups merged into one pool: Available
// and Enable stay ambiguous (409) without a connection_id hint, resolve correctly with one, and List's
// connection_id filter separates each instance's enabled groups.
func TestGroupsAreScopedPerWhatsAppInstanceWhenATenantRunsMoreThanOne(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	connA2 := e.connection(tenantA) // a second WhatsApp instance of the SAME tenant
	admin := e.member(tenantA, "tenant_admin", "active")
	g1 := e.group(tenantA, connA, jid1, "Grupo da instância 1", true)
	g2 := e.group(tenantA, connA2, jid2, "Grupo da instância 2", true)

	dir := &fakeDir{conn: connA.ID, groups: []channeldomain.ProviderGroup{{JID: jid1, Name: "Grupo da instância 1", ParticipantCount: 3}}, otherActive: []uuid.UUID{connA2.ID}}
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), dir)

	// No hint, two active connections: ambiguous, exactly like today with a single instance's worth of API surface.
	if rec := call(t, e.app, tenantA, admin, http.MethodGet, "/", "", nil, h.Available); rec.Code != http.StatusConflict {
		t.Fatalf("available, ambiguous: %d, want 409", rec.Code)
	}
	if rec := call(t, e.app, tenantA, admin, http.MethodPost, "/", `{"provider_group_id":"`+jid1+`"}`, nil, h.Enable); rec.Code != http.StatusConflict {
		t.Fatalf("enable, ambiguous: %d, want 409", rec.Code)
	}

	// A connection_id hint resolves it: Available reports which connection the results belong to.
	var avail struct {
		Items        []availableItem `json:"items"`
		ConnectionID uuid.UUID       `json:"connection_id"`
	}
	rec := call(t, e.app, tenantA, admin, http.MethodGet, "/?connection_id="+connA.ID.String(), "", nil, h.Available)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &avail) != nil || avail.ConnectionID != connA.ID || len(avail.Items) != 1 {
		t.Fatalf("available with hint = %d %s", rec.Code, rec.Body.String())
	}
	// A hint that is not one of the tenant's active connections at all (an unrelated, real connection id of
	// another tenant) is refused exactly like a connection this tenant never had.
	if rec := call(t, e.app, tenantA, admin, http.MethodGet, "/?connection_id="+uuid.NewString(), "", nil, h.Available); rec.Code != http.StatusConflict {
		t.Fatalf("available, unknown hint: %d, want 409", rec.Code)
	}
	if rec := call(t, e.app, tenantA, admin, http.MethodGet, "/?connection_id=not-a-uuid", "", nil, h.Available); rec.Code != http.StatusBadRequest {
		t.Fatalf("available, malformed hint: %d, want 400", rec.Code)
	}

	// List: each instance's enabled groups stay separate; the item itself also carries its connection id.
	var list struct {
		Items []groupItem `json:"items"`
	}
	rec = call(t, e.app, tenantA, admin, http.MethodGet, "/?connection_id="+connA.ID.String(), "", nil, h.List)
	if json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != g1 || list.Items[0].ConnectionID != connA.ID {
		t.Fatalf("list by connection 1 = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, e.app, tenantA, admin, http.MethodGet, "/?connection_id="+connA2.ID.String(), "", nil, h.List)
	if json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != g2 {
		t.Fatalf("list by connection 2 = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, e.app, tenantA, admin, http.MethodGet, "/", "", nil, h.List)
	if json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != 200 || len(list.Items) != 2 {
		t.Fatalf("list with no filter must still show both instances' groups = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, e.app, tenantA, admin, http.MethodGet, "/?connection_id=not-a-uuid", "", nil, h.List); rec.Code != http.StatusBadRequest {
		t.Fatalf("list, malformed connection_id: %d, want 400", rec.Code)
	}
}

// If the external disk stays unreachable for a long time, the group tables could grow without end.
// Past the cap only group storage pauses (counted, logged); stored data is never deleted to make room,
// the size is read at most once a minute, and the cap can be switched off.
func TestIntakePausesAtTheSizeCapAndResumes(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	e.group(tenantA, connA, jid1, "Oficial", true)
	events := channeladapters.NewPostgresWebhookEventStore(e.app)
	clock := time.Now()
	intake := NewIntake(e.app, events).WithMaxBytes(1) // everything is over a 1-byte cap
	intake.now = func() time.Time { return clock }
	send := func(key, id string) (bool, bool) {
		t.Helper()
		ing, dup, err := intake.ProcessGroupMessage(e.ctx, connA, key, "message.any", "d-"+key, groupMsg(connA, jid1, id, "oi", time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		return ing, dup
	}

	if ing, dup := send("k1", "m1"); ing || dup {
		t.Fatalf("over the cap the message must be dropped, got ingested=%v duplicate=%v", ing, dup)
	}
	if e.count(`SELECT count(*) FROM wa_group_messages WHERE tenant_id=$1`, tenantA) != 0 || e.count(`SELECT count(*) FROM channel_webhook_events WHERE provider_event_id='k1'`) != 0 {
		t.Fatal("a paused message must leave no trace (not even a dedup record), so WAHA's redelivery can still be stored later")
	}

	// The cap is lifted, but the last answer is reused for a minute (one size query per minute at most).
	intake.maxBytes = 1 << 40
	if ing, _ := send("k2", "m2"); ing {
		t.Fatal("within the minute the cached 'over' answer must still apply")
	}
	clock = clock.Add(2 * time.Minute)
	if ing, _ := send("k3", "m3"); !ing {
		t.Fatal("after the cap is no longer exceeded, storage must resume")
	}
	if e.count(`SELECT count(*) FROM wa_group_messages WHERE tenant_id=$1`, tenantA) != 1 {
		t.Fatal("exactly the resumed message must be stored")
	}
	// Disabled cap.
	off := NewIntake(e.app, events).WithMaxBytes(0)
	if ing, _, err := off.ProcessGroupMessage(e.ctx, connA, "k4", "message.any", "d-k4", groupMsg(connA, jid1, "m4", "oi", time.Now())); err != nil || !ing {
		t.Fatalf("a disabled cap must never pause: ingested=%v err=%v", ing, err)
	}
}

// ADR-0017 Wave 2: the author of a group message becomes a provider- and connection-qualified channel participant, a
// replay never duplicates it, and a reply resolves to a message of the SAME group only.
func TestIntakeRecordsExternalParticipantsAndResolvesRepliesInsideTheGroup(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	e.group(tenantA, connA, jid1, "Oficial", true)
	e.group(tenantA, connA, jid2, "Outro grupo", true)
	e.group(tenantB, connB, jid1, "Do B", true)
	intake := NewIntake(e.app, channeladapters.NewPostgresWebhookEventStore(e.app))
	now := time.Now().UTC().Truncate(time.Second)
	as := func(conn channeldomain.ChannelConnection, jid, id, author, name, reply string, at time.Time) channeldomain.InboundGroupMessage {
		m := groupMsg(conn, jid, id, "texto "+id, at)
		m.AuthorJID, m.AuthorName, m.ReplyToExternalID = author, name, reply
		return m
	}
	ingest := func(conn channeldomain.ChannelConnection, key string, m channeldomain.InboundGroupMessage) {
		t.Helper()
		if _, _, err := intake.ProcessGroupMessage(e.ctx, conn, key, "message.any", "d-"+key, m); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	// three participants in one group; the WAHA serialized id keeps the bare id in its third segment
	ingest(connA, "e1", as(connA, jid1, "false_"+jid1+"_3EB0AAA1_111@lid", "111@lid", "João", "", now))
	ingest(connA, "e2", as(connA, jid1, "false_"+jid1+"_3EB0AAA2_222@lid", "222@lid", "Maria", "3EB0AAA1", now.Add(time.Minute)))
	ingest(connA, "e3", as(connA, jid1, "false_"+jid1+"_3EB0AAA3_333@lid", "333@lid", "Pedro", "3EB0UNKNOWN", now.Add(2*time.Minute)))
	if n := e.count(`SELECT count(*) FROM channel_participants WHERE tenant_id=$1`, tenantA); n != 3 {
		t.Fatalf("participants = %d, want 3", n)
	}
	// replaying the same events and re-sending a known author creates nothing new
	ingest(connA, "e1", as(connA, jid1, "false_"+jid1+"_3EB0AAA1_111@lid", "111@lid", "João", "", now))
	ingest(connA, "e4", as(connA, jid1, "false_"+jid1+"_3EB0AAA4_111@lid", "111@lid", "João S.", "", now.Add(3*time.Minute)))
	if n := e.count(`SELECT count(*) FROM channel_participants WHERE tenant_id=$1`, tenantA); n != 3 {
		t.Fatalf("a replay or a known author created participants: %d", n)
	}
	var name string
	_ = e.seed.QueryRow(e.ctx, `SELECT display_name FROM channel_participants WHERE tenant_id=$1 AND external_participant_id='111@lid'`, tenantA).Scan(&name)
	if name != "João S." {
		t.Fatalf("display name = %q, the latest non-empty label wins", name)
	}
	// messages carry their sender; the reply resolves (bare id vs serialized id) to the quoted message
	if n := e.count(`SELECT count(*) FROM wa_group_messages WHERE tenant_id=$1 AND sender_channel_participant_id IS NOT NULL`, tenantA); n != 4 {
		t.Fatalf("messages with a sender = %d, want 4", n)
	}
	if n := e.count(`SELECT count(*) FROM wa_group_messages r JOIN wa_group_messages q ON q.id = r.reply_to_group_message_id AND q.tenant_id = r.tenant_id
	                 WHERE r.tenant_id=$1 AND r.provider_message_id LIKE '%3EB0AAA2%' AND q.provider_message_id LIKE '%3EB0AAA1%'`, tenantA); n != 1 {
		t.Fatalf("the reply must resolve to the quoted message, got %d", n)
	}
	// a reply to something we never stored keeps the provider id and links nothing
	if n := e.count(`SELECT count(*) FROM wa_group_messages WHERE tenant_id=$1 AND reply_to_external_message_id='3EB0UNKNOWN' AND reply_to_group_message_id IS NULL`, tenantA); n != 1 {
		t.Fatalf("an unresolved reply must keep its external id, got %d", n)
	}
	// the same quoted id in ANOTHER group must not resolve across groups
	ingest(connA, "e5", as(connA, jid2, "false_"+jid2+"_3EB0BBB1_111@lid", "111@lid", "João", "3EB0AAA1", now.Add(4*time.Minute)))
	if n := e.count(`SELECT count(*) FROM wa_group_messages WHERE tenant_id=$1 AND provider_message_id LIKE '%3EB0BBB1%' AND reply_to_group_message_id IS NOT NULL`, tenantA); n != 0 {
		t.Fatal("a reply must never resolve to a message of another group")
	}
	// the same author id in another tenant (and another connection) is a different participant
	ingest(connB, "b1", as(connB, jid1, "false_"+jid1+"_3EB0AAA1_111@lid", "111@lid", "João do B", "", now))
	var idA, idB uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM channel_participants WHERE tenant_id=$1 AND external_participant_id='111@lid'`, tenantA).Scan(&idA)
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM channel_participants WHERE tenant_id=$1 AND external_participant_id='111@lid'`, tenantB).Scan(&idB)
	if idA == uuid.Nil || idB == uuid.Nil || idA == idB {
		t.Fatalf("participants of different tenants must be distinct rows: %v %v", idA, idB)
	}
	// a message without a usable author is still stored, just without a participant
	ingest(connA, "e6", as(connA, jid1, "false_"+jid1+"_3EB0AAA6", "", "", "", now.Add(5*time.Minute)))
	if n := e.count(`SELECT count(*) FROM wa_group_messages WHERE tenant_id=$1 AND provider_message_id LIKE '%3EB0AAA6' AND sender_channel_participant_id IS NULL`, tenantA); n != 1 {
		t.Fatalf("a message with no author must be stored without a participant, got %d", n)
	}
}

// A group author who is a known contact shows under the team's alias; everyone else keeps the name declared on WhatsApp.
func TestGroupAuthorsShowTheTeamsAliasWhenTheParticipantIsAKnownContact(t *testing.T) {
	e := newEnv(t)
	tenant, conn := e.tenant()
	admin := e.member(tenant, "tenant_admin", "active")
	gid := e.group(tenant, conn, jid1, "Oficial", true)
	intake := NewIntake(e.app, channeladapters.NewPostgresWebhookEventStore(e.app))
	now := time.Now().UTC().Truncate(time.Second)
	for i, a := range []struct{ jid, name string }{{"111@lid", "João do WhatsApp"}, {"222@lid", "Maria"}} {
		m := groupMsg(conn, jid1, "gm-"+a.jid, "oi", now.Add(time.Duration(i)*time.Minute))
		m.AuthorJID, m.AuthorName = a.jid, a.name
		if _, _, err := intake.ProcessGroupMessage(e.ctx, conn, "k"+a.jid, "message.any", "d"+a.jid, m); err != nil {
			t.Fatal(err)
		}
	}
	// João is a known contact the team renamed; Maria is not a contact
	contact := uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,whatsapp_name,alias,phone_e164,status) VALUES($1,$2,'x','João do WhatsApp','João Silva (ACME)','+5592955551111','active')`, contact, tenant)
	e.exec(`UPDATE channel_participants SET contact_id=$2 WHERE tenant_id=$1 AND external_participant_id='111@lid'`, tenant, contact)
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{conn: conn.ID})
	rec := call(t, e.app, tenant, admin, http.MethodGet, "/", "", map[string]string{"group_id": gid.String()}, h.ListMessages)
	var page struct {
		Items []struct {
			AuthorName string `json:"author_name"`
		} `json:"items"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 2 {
		t.Fatalf("messages = %d %s", rec.Code, rec.Body.String())
	}
	got := map[string]bool{}
	for _, it := range page.Items {
		got[it.AuthorName] = true
	}
	if !got["João Silva (ACME)"] || !got["Maria"] || got["João do WhatsApp"] {
		t.Fatalf("authors = %v: the alias replaces the declared name for a known contact only", got)
	}
	// and the group list's last-message author (Maria wrote last: unchanged; make João last and check the alias)
	e.exec(`UPDATE wa_group_messages SET sent_at = now() + interval '1 hour' WHERE tenant_id=$1 AND author_jid='111@lid'`, tenant)
	rec = call(t, e.app, tenant, admin, http.MethodGet, "/", "", nil, h.List)
	var gl struct {
		Items []struct {
			LastMessage *struct {
				AuthorName string `json:"author_name"`
			} `json:"last_message"`
		} `json:"items"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &gl) != nil || len(gl.Items) == 0 || gl.Items[0].LastMessage == nil || gl.Items[0].LastMessage.AuthorName != "João Silva (ACME)" {
		t.Fatalf("group list = %d %s", rec.Code, rec.Body.String())
	}
}
