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
	conn = channeldomain.ChannelConnection{ID: uuid.New(), TenantID: tenant, Channel: channeldomain.ChannelWhatsApp, Provider: channeldomain.ProviderWAHA, ProviderKind: channeldomain.ProviderKindUnofficial, Status: channeldomain.ConnectionStatusActive}
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, conn.ID, tenant, conn.ID.String())
	return tenant, conn
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

type fakeDir struct {
	conn   uuid.UUID
	groups []channeldomain.ProviderGroup
	err    error
	calls  int
}

func (d *fakeDir) List(_ context.Context, _ uuid.UUID) (uuid.UUID, []channeldomain.ProviderGroup, error) {
	d.calls++
	return d.conn, d.groups, d.err
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
