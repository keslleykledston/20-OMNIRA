package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
)

// Real Postgres, runtime role under RLS, a real tenant session per call.
// Tenant A is the caller; Tenant B owns data that must stay untouched.

func queuesEnv(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, *QueuesHandler) {
	t.Helper()
	seed, app := teamSeedPool(t), teamAppPool(t)
	return seed, app, NewQueuesHandler(app, auditadapters.NewPostgresAuditEventRepository(app))
}

func callQueues(t *testing.T, app *pgxpool.Pool, tenant, actor uuid.UUID, method string, fn http.HandlerFunc, path map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rec *httptest.ResponseRecorder
	if err := asActor(t, app, tenant, actor, func(ctx context.Context) error {
		rec = doRequest(t, ctx, method, fn, path, []byte(body))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rec
}

func seedQueue(t *testing.T, seed *pgxpool.Pool, tenantID uuid.UUID, name, mode string, isDefault bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := seed.Exec(context.Background(),
		`INSERT INTO queues (id, tenant_id, name, mode, is_default) VALUES ($1,$2,$3,$4,$5)`, id, tenantID, name, mode, isDefault); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	return id
}

func defaultsIn(t *testing.T, seed *pgxpool.Pool, tenantID uuid.UUID) int {
	t.Helper()
	var n int
	if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM queues WHERE tenant_id=$1 AND is_default`, tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func auditCount(t *testing.T, seed *pgxpool.Pool, tenantID uuid.UUID, action string) int {
	t.Helper()
	var n int
	if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenantID, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func decodeQueue(t *testing.T, rec *httptest.ResponseRecorder) queueItem {
	t.Helper()
	var it queueItem
	if err := json.Unmarshal(rec.Body.Bytes(), &it); err != nil {
		t.Fatalf("decode queue: %v (%s)", err, rec.Body.String())
	}
	return it
}

func TestQueuesListIsScopedAndCountsAreReal(t *testing.T) {
	seed, app, h := queuesEnv(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	op1 := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	op2 := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	def := seedQueue(t, seed, a.tenantID, "Suporte", "round_robin", true)
	other := seedQueue(t, seed, a.tenantID, "Comercial", "manual", false)
	seedQueue(t, seed, b.tenantID, "Fila do B", "manual", true)

	for _, m := range []struct {
		user      uuid.UUID
		available bool
	}{{op1, true}, {op2, false}} {
		if _, err := seed.Exec(context.Background(),
			`INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity) VALUES ($1,$2,$3,true,$4,1)`,
			a.tenantID, def, m.user, m.available); err != nil {
			t.Fatal(err)
		}
	}
	contact := uuid.New()
	if _, err := seed.Exec(context.Background(), `INSERT INTO contacts (id, tenant_id, display_name, phone_e164, status) VALUES ($1,$2,'C','+5511900009001','active')`, contact, a.tenantID); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"open", "open", "closed"} {
		if _, err := seed.Exec(context.Background(), `INSERT INTO conversations (id, tenant_id, contact_id, queue_id, status) VALUES ($1,$2,$3,$4,$5)`, uuid.New(), a.tenantID, contact, def, st); err != nil {
			t.Fatal(err)
		}
	}

	rec := callQueues(t, app, a.tenantID, admin, http.MethodGet, h.List, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []queueItem `json:"items"`
		Count int         `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 || len(body.Items) != 2 {
		t.Fatalf("tenant A must see exactly its 2 queues, got %d: %s", len(body.Items), rec.Body.String())
	}
	if body.Items[0].ID != def || !body.Items[0].IsDefault || body.Items[1].ID != other {
		t.Fatalf("default queue must come first: %+v", body.Items)
	}
	d := body.Items[0]
	if d.MemberCount != 2 || d.AvailableCount != 1 || d.OpenConversationCount != 2 {
		t.Fatalf("counts = members %d, available %d, open conversations %d; want 2, 1, 2", d.MemberCount, d.AvailableCount, d.OpenConversationCount)
	}
	if strings.Contains(rec.Body.String(), "tenant_id") || strings.Contains(rec.Body.String(), "Fila do B") {
		t.Fatalf("response leaks tenant data: %s", rec.Body.String())
	}
}

func TestQueuesRequireAgentPermissions(t *testing.T) {
	seed, app, h := queuesEnv(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	supervisor := seedTeamMember(t, seed, a.tenantID, "tenant_supervisor", "active")
	agent := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	revoked := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "revoked")
	q := seedQueue(t, seed, a.tenantID, "Fila", "manual", true)
	pv := map[string]string{"queue_id": q.String()}

	for _, who := range []struct {
		name  string
		actor uuid.UUID
		want  int
	}{{"admin", admin, http.StatusOK}, {"supervisor", supervisor, http.StatusOK}, {"agent", agent, http.StatusForbidden}, {"revoked admin", revoked, http.StatusForbidden}} {
		if got := callQueues(t, app, a.tenantID, who.actor, http.MethodGet, h.List, nil, "").Code; got != who.want {
			t.Fatalf("%s list = %d, want %d", who.name, got, who.want)
		}
	}
	for _, actor := range []uuid.UUID{agent, revoked} {
		if got := callQueues(t, app, a.tenantID, actor, http.MethodPost, h.Create, nil, `{"name":"X"}`).Code; got != http.StatusForbidden {
			t.Fatalf("create without agent.manage = %d, want 403", got)
		}
		if got := callQueues(t, app, a.tenantID, actor, http.MethodPatch, h.Update, pv, `{"name":"Y"}`).Code; got != http.StatusForbidden {
			t.Fatalf("update without agent.manage = %d, want 403", got)
		}
		if got := callQueues(t, app, a.tenantID, actor, http.MethodDelete, h.Delete, pv, "").Code; got != http.StatusForbidden {
			t.Fatalf("delete without agent.manage = %d, want 403", got)
		}
	}
	if got := callQueues(t, app, a.tenantID, supervisor, http.MethodPost, h.Create, nil, `{"name":"Do supervisor"}`).Code; got != http.StatusCreated {
		t.Fatalf("supervisor create = %d, want 201", got)
	}
}

func TestCreateQueueValidationAndDuplicates(t *testing.T) {
	seed, app, h := queuesEnv(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	adminB := seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")

	post := func(body string) *httptest.ResponseRecorder {
		return callQueues(t, app, a.tenantID, admin, http.MethodPost, h.Create, nil, body)
	}
	for name, body := range map[string]string{
		"empty name":    `{"name":""}`,
		"blank name":    `{"name":"   "}`,
		"61 characters": `{"name":"` + strings.Repeat("a", 61) + `"}`,
		"unknown mode":  `{"name":"X","mode":"broadcast"}`,
		"not json":      `nope`,
		"missing name":  `{}`,
	} {
		if got := post(body).Code; got != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", name, got)
		}
	}

	rec := post(`{"name":"  Suporte  "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	it := decodeQueue(t, rec)
	if it.Name != "Suporte" || it.Mode != "manual" || it.IsDefault || it.MemberCount != 0 {
		t.Fatalf("created queue = %+v (name trimmed, mode manual by default, not default)", it)
	}
	if got := post(`{"name":"Suporte"}`).Code; got != http.StatusConflict {
		t.Fatalf("duplicate name = %d, want 409", got)
	}
	// The same name is free in another tenant.
	if got := callQueues(t, app, b.tenantID, adminB, http.MethodPost, h.Create, nil, `{"name":"Suporte"}`).Code; got != http.StatusCreated {
		t.Fatalf("same name in tenant B = %d, want 201", got)
	}
	if n := auditCount(t, seed, a.tenantID, "queue.created"); n != 1 {
		t.Fatalf("queue.created audit events = %d, want 1 (only the successful create)", n)
	}
}

func TestCreateQueueAsDefaultSwitchesTheDefault(t *testing.T) {
	seed, app, h := queuesEnv(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	old := seedQueue(t, seed, a.tenantID, "Antiga", "manual", true)

	rec := callQueues(t, app, a.tenantID, admin, http.MethodPost, h.Create, nil, `{"name":"Nova","mode":"round_robin","is_default":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	created := decodeQueue(t, rec)
	if !created.IsDefault || created.Mode != "round_robin" {
		t.Fatalf("created = %+v", created)
	}
	if n := defaultsIn(t, seed, a.tenantID); n != 1 {
		t.Fatalf("defaults in tenant = %d, want exactly 1", n)
	}
	var stillDefault bool
	if err := seed.QueryRow(context.Background(), `SELECT is_default FROM queues WHERE id=$1`, old).Scan(&stillDefault); err != nil || stillDefault {
		t.Fatalf("previous default must be cleared: %v %v", stillDefault, err)
	}
}

func TestUpdateQueue(t *testing.T) {
	seed, app, h := queuesEnv(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	first := seedQueue(t, seed, a.tenantID, "Primeira", "manual", true)
	second := seedQueue(t, seed, a.tenantID, "Segunda", "manual", false)
	patch := func(id uuid.UUID, body string) *httptest.ResponseRecorder {
		return callQueues(t, app, a.tenantID, admin, http.MethodPatch, h.Update, map[string]string{"queue_id": id.String()}, body)
	}

	if got := patch(second, `{}`).Code; got != http.StatusBadRequest {
		t.Fatalf("empty body = %d, want 400", got)
	}
	if got := patch(second, `{"name":""}`).Code; got != http.StatusBadRequest {
		t.Fatalf("empty name = %d, want 400", got)
	}
	if got := patch(second, `{"mode":"x"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("bad mode = %d, want 400", got)
	}
	if got := patch(uuid.New(), `{"name":"Zzz"}`).Code; got != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", got)
	}
	if got := callQueues(t, app, a.tenantID, admin, http.MethodPatch, h.Update, map[string]string{"queue_id": "not-a-uuid"}, `{"name":"Z"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("bad uuid = %d, want 400", got)
	}

	rec := patch(second, `{"name":" Comercial ","mode":"round_robin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename+mode = %d %s", rec.Code, rec.Body.String())
	}
	if it := decodeQueue(t, rec); it.Name != "Comercial" || it.Mode != "round_robin" || it.IsDefault {
		t.Fatalf("updated = %+v", it)
	}
	if got := patch(second, `{"name":"Primeira"}`).Code; got != http.StatusConflict {
		t.Fatalf("rename to an existing name = %d, want 409", got)
	}

	// Switching the default moves it atomically; there is always exactly one.
	rec = patch(second, `{"is_default":true}`)
	if rec.Code != http.StatusOK || !decodeQueue(t, rec).IsDefault {
		t.Fatalf("make default = %d %s", rec.Code, rec.Body.String())
	}
	if n := defaultsIn(t, seed, a.tenantID); n != 1 {
		t.Fatalf("defaults in tenant = %d, want exactly 1", n)
	}
	var firstIsDefault bool
	if err := seed.QueryRow(context.Background(), `SELECT is_default FROM queues WHERE id=$1`, first).Scan(&firstIsDefault); err != nil || firstIsDefault {
		t.Fatalf("old default must be cleared: %v %v", firstIsDefault, err)
	}
	// Unsetting the default is refused: with no default, new conversations are not routed.
	if got := patch(second, `{"is_default":false}`).Code; got != http.StatusConflict {
		t.Fatalf("unset default = %d, want 409", got)
	}
	if n := defaultsIn(t, seed, a.tenantID); n != 1 {
		t.Fatalf("a refused unset must leave the default alone, defaults = %d", n)
	}
	// Setting a non-default queue to is_default=false is a no-op, not an error.
	if got := patch(first, `{"is_default":false}`).Code; got != http.StatusOK {
		t.Fatalf("is_default=false on a non-default queue = %d, want 200", got)
	}
	if auditCount(t, seed, a.tenantID, "queue.default_changed") != 1 || auditCount(t, seed, a.tenantID, "queue.updated") < 1 {
		t.Fatalf("expected one queue.default_changed and at least one queue.updated audit event")
	}
}

func TestDeleteQueue(t *testing.T) {
	seed, app, h := queuesEnv(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	member := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	def := seedQueue(t, seed, a.tenantID, "Padrão", "manual", true)
	busy := seedQueue(t, seed, a.tenantID, "Com conversa", "manual", false)
	empty := seedQueue(t, seed, a.tenantID, "Vazia", "manual", false)
	del := func(id uuid.UUID) int {
		return callQueues(t, app, a.tenantID, admin, http.MethodDelete, h.Delete, map[string]string{"queue_id": id.String()}, "").Code
	}

	contact := uuid.New()
	if _, err := seed.Exec(context.Background(), `INSERT INTO contacts (id, tenant_id, display_name, phone_e164, status) VALUES ($1,$2,'C','+5511900009002','active')`, contact, a.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(context.Background(), `INSERT INTO conversations (id, tenant_id, contact_id, queue_id, status) VALUES ($1,$2,$3,$4,'closed')`, uuid.New(), a.tenantID, contact, busy); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(context.Background(), `INSERT INTO queue_members (tenant_id, queue_id, user_id) VALUES ($1,$2,$3)`, a.tenantID, empty, member); err != nil {
		t.Fatal(err)
	}

	if got := del(def); got != http.StatusConflict {
		t.Fatalf("delete default = %d, want 409", got)
	}
	if got := del(busy); got != http.StatusConflict {
		t.Fatalf("delete queue with conversations = %d, want 409", got)
	}
	if got := del(uuid.New()); got != http.StatusNotFound {
		t.Fatalf("delete unknown = %d, want 404", got)
	}
	if got := callQueues(t, app, a.tenantID, admin, http.MethodDelete, h.Delete, map[string]string{"queue_id": "x"}, "").Code; got != http.StatusBadRequest {
		t.Fatalf("delete bad uuid = %d, want 400", got)
	}
	if got := del(empty); got != http.StatusNoContent {
		t.Fatalf("delete a queue without conversations = %d, want 204", got)
	}
	var members int
	if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM queue_members WHERE queue_id=$1`, empty).Scan(&members); err != nil || members != 0 {
		t.Fatalf("members of the deleted queue must go with it: %d %v", members, err)
	}
	if auditCount(t, seed, a.tenantID, "queue.deleted") != 1 {
		t.Fatalf("expected one queue.deleted audit event")
	}
	for _, kept := range []uuid.UUID{def, busy} {
		var n int
		if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM queues WHERE id=$1`, kept).Scan(&n); err != nil || n != 1 {
			t.Fatalf("refused deletes must keep the queue: %d %v", n, err)
		}
	}
}

// Knowing another tenant's queue id must not be enough to read, change or delete it.
func TestQueuesOfAnotherTenantAreUntouchable(t *testing.T) {
	seed, app, h := queuesEnv(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	foreign := seedQueue(t, seed, b.tenantID, "Do B", "manual", true)
	pv := map[string]string{"queue_id": foreign.String()}

	if got := callQueues(t, app, a.tenantID, adminA, http.MethodPatch, h.Update, pv, `{"name":"Invadida","mode":"round_robin"}`).Code; got != http.StatusNotFound {
		t.Fatalf("patch foreign queue = %d, want 404", got)
	}
	if got := callQueues(t, app, a.tenantID, adminA, http.MethodPatch, h.Update, pv, `{"is_default":true}`).Code; got != http.StatusNotFound {
		t.Fatalf("make foreign queue default = %d, want 404", got)
	}
	if got := callQueues(t, app, a.tenantID, adminA, http.MethodDelete, h.Delete, pv, "").Code; got != http.StatusNotFound {
		t.Fatalf("delete foreign queue = %d, want 404", got)
	}
	var name, mode string
	var isDefault bool
	if err := seed.QueryRow(context.Background(), `SELECT name, mode, is_default FROM queues WHERE id=$1`, foreign).Scan(&name, &mode, &isDefault); err != nil {
		t.Fatal(err)
	}
	if name != "Do B" || mode != "manual" || !isDefault {
		t.Fatalf("tenant B queue changed: %s %s %v", name, mode, isDefault)
	}
	// Tenant B's default must not be cleared as a side effect of A creating its own default.
	if got := callQueues(t, app, a.tenantID, adminA, http.MethodPost, h.Create, nil, `{"name":"A1","is_default":true}`).Code; got != http.StatusCreated {
		t.Fatalf("A create default = %d", got)
	}
	if n := defaultsIn(t, seed, b.tenantID); n != 1 {
		t.Fatalf("tenant B lost its default queue: %d", n)
	}
}
