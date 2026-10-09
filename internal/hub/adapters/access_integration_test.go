package adapters_test

// Access panel (ADR-0039) on a real PostgreSQL, through the real handler and the caller's own RLS session: only an admin
// of the hub reaches it, what each call really changes (and what the database then lets an agent read), and that the
// actor is on the audit trail.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/access"
	"github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
)

type accessAPI struct {
	w   *world
	srv *httptest.Server
	svc *access.Service
}

func newAccessAPI(t *testing.T, w *world) *accessAPI {
	t.Helper()
	h := adapters.NewHTTPHandler(w.app).WithAccessAPI(true)
	a, err := adapters.NewAccessHandler(w.app)
	w.must(err)
	svc, err := access.New(w.app)
	w.must(err)
	shim := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if raw := r.Header.Get("X-Test-User"); raw != "" {
				id, err := uuid.Parse(raw)
				if err != nil {
					t.Errorf("bad test user header %q", raw)
				}
				r = r.WithContext(contextWithPrincipal(r, id))
			}
			next.ServeHTTP(rw, r)
		})
	}
	session := tenancyadapters.UserSessionMiddleware(w.app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hubs", shim(session(http.HandlerFunc(h.ListMyHubs))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/access", shim(session(http.HandlerFunc(a.Overview))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/access/agents", shim(session(http.HandlerFunc(a.AddAgent))))
	mux.Handle("DELETE /api/v1/hubs/{hub_id}/access/agents/{user_id}", shim(session(http.HandlerFunc(a.RemoveAgent))))
	mux.Handle("PUT /api/v1/hubs/{hub_id}/access/agents/{user_id}/instances/{tenant_id}", shim(session(http.HandlerFunc(a.SetAccess))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/access/instances/{tenant_id}/admins", shim(session(http.HandlerFunc(a.AddInstanceAdmin))))
	mux.Handle("DELETE /api/v1/hubs/{hub_id}/access/instances/{tenant_id}/admins/{user_id}", shim(session(http.HandlerFunc(a.RemoveInstanceAdmin))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &accessAPI{w: w, srv: srv, svc: svc}
}

func (a *accessAPI) call(method, path string, user uuid.UUID, body any) (int, string) {
	a.w.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		a.w.must(err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.srv.URL+path, rd)
	a.w.must(err)
	req.Header.Set("Content-Type", "application/json")
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (a *accessAPI) base() string { return "/api/v1/hubs/" + a.w.hub.String() + "/access" }

func (a *accessAPI) overview(user uuid.UUID) (int, access.Overview) {
	code, body := a.call("GET", a.base(), user, nil)
	var out access.Overview
	if code == 200 {
		a.w.must(json.Unmarshal([]byte(body), &out))
	}
	return code, out
}

func (a *accessAPI) set(user, agent uuid.UUID, tenantKey, mode string, until *time.Time) int {
	code, _ := a.call("PUT", a.base()+"/agents/"+agent.String()+"/instances/"+a.w.tenant[tenantKey].String(), user, map[string]any{"mode": mode, "valid_until": until})
	return code
}

func (w *world) email(user uuid.UUID, email string) {
	w.exec(`UPDATE users SET email = $2 WHERE id = $1`, user, email)
}

func (w *world) hubAdmin(name string) uuid.UUID {
	u := w.user(name)
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, u, w.roleHubAdmin)
	return u
}

func (w *world) auditActor(action string, actor uuid.UUID) int {
	return w.count(`SELECT count(*) FROM audit_events WHERE action = $1 AND actor_id = $2`, action, actor)
}

func TestAccess_OnlyAnAdminOfTheHubReachesThePanel(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	agent := w.hubAgent("agent")
	w.grantReply(agent, "A")
	tenantAdmin := w.user("tenantadmin") // administers company A in its own right: that is not the Hub's panel
	w.exec(`INSERT INTO memberships (tenant_id, user_id, role_id) VALUES ($1, $2, $3)`, w.tenant["A"], tenantAdmin, w.role("tenant_admin"))
	otherHub := uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Other')`, otherHub)
	otherAdmin := w.user("otheradmin")
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, otherHub, otherAdmin, w.roleHubAdmin)
	outsider := w.user("outsider")
	target := w.user("target")
	w.email(target, "target@example.com")

	if code, _ := api.overview(admin); code != 200 {
		t.Fatalf("the hub admin is refused: %d", code)
	}
	snapshot := func() int {
		return w.count(`SELECT (SELECT count(*) FROM hub_memberships) + (SELECT count(*) FROM effective_access_grants) + (SELECT count(*) FROM memberships)`)
	}
	before := snapshot()
	for name, u := range map[string]uuid.UUID{"hub agent": agent, "tenant admin": tenantAdmin, "admin of another hub": otherAdmin, "outsider": outsider, "anonymous": uuid.Nil} {
		want := 404
		if u == uuid.Nil {
			want = 401
		}
		calls := []struct {
			method, path string
			body         any
		}{
			{"GET", api.base(), nil},
			{"POST", api.base() + "/agents", map[string]any{"email": "target@example.com"}},
			{"DELETE", api.base() + "/agents/" + agent.String(), nil},
			{"PUT", api.base() + "/agents/" + target.String() + "/instances/" + w.tenant["A"].String(), map[string]any{"mode": "reply"}},
			{"POST", api.base() + "/instances/" + w.tenant["A"].String() + "/admins", map[string]any{"email": "target@example.com"}},
			{"DELETE", api.base() + "/instances/" + w.tenant["A"].String() + "/admins/" + tenantAdmin.String(), nil},
		}
		for _, c := range calls {
			if code, _ := api.call(c.method, c.path, u, c.body); code != want {
				t.Errorf("%s %s %s: %d, want %d", name, c.method, c.path, code, want)
			}
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("a refused call changed the database (%d -> %d rows)", before, after)
	}
	// the service refuses on its own too: a handler bug must not be enough
	if _, err := api.svc.Overview(w.ctx, agent, w.hub); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("service overview for an agent: %v", err)
	}
	if err := api.svc.SetAccess(w.ctx, agent, w.hub, target, w.tenant["A"], "reply", nil); err == nil {
		t.Errorf("service SetAccess for a non-admin was accepted")
	}
	if err := api.svc.SetAccess(w.ctx, otherAdmin, w.hub, target, w.tenant["A"], "reply", nil); err == nil {
		t.Errorf("service SetAccess for the admin of ANOTHER hub was accepted")
	}
	if after := snapshot(); after != before {
		t.Fatalf("a refused service call changed the database")
	}
}

func TestAccess_TheMatrixCellChangesWhatTheDatabaseLetsTheAgentDo(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	agent := w.hubAgent("agent")

	if got := w.reads(agent, "A"); got.conversations != 0 {
		t.Fatalf("a hub agent with no grant reads %d conversation(s)", got.conversations)
	}
	if code := api.set(admin, agent, "A", "read", nil); code != 204 {
		t.Fatalf("read: %d", code)
	}
	if w.reads(agent, "A").conversations == 0 {
		t.Fatalf("read access did not open the company")
	}
	if n := w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1 AND tenant_id = $2 AND can_reply`, agent, w.tenant["A"]); n != 0 {
		t.Fatalf("read must not carry can_reply")
	}
	if code := api.set(admin, agent, "A", "reply", nil); code != 204 {
		t.Fatalf("reply: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1 AND tenant_id = $2 AND can_reply AND status = 'active'`, agent, w.tenant["A"]); n != 1 {
		t.Fatalf("reply did not set can_reply")
	}
	// B stays closed: one cell is one company
	if w.reads(agent, "B").conversations != 0 {
		t.Fatalf("a cell on A opened B")
	}
	// a second company makes the person multi-instance, and only the admin of the hub can do that
	if code := api.set(admin, agent, "B", "read", nil); code != 204 {
		t.Fatalf("B: %d", code)
	}
	code, ov := api.overview(admin)
	if code != 200 {
		t.Fatalf("overview: %d", code)
	}
	var seen bool
	for _, a := range ov.Agents {
		if a.UserID == agent {
			seen = true
			if a.Instances != 2 || len(a.Grants) != 2 {
				t.Errorf("the agent works in 2 companies, overview says %d (%d grants)", a.Instances, len(a.Grants))
			}
		}
	}
	if !seen {
		t.Fatalf("the agent is missing from the overview")
	}
	// revoking and bringing it back is explicit and keeps ONE row
	if code := api.set(admin, agent, "A", "none", nil); code != 204 {
		t.Fatalf("none: %d", code)
	}
	if w.reads(agent, "A").conversations != 0 {
		t.Fatalf("'none' still lets the agent read")
	}
	if code := api.set(admin, agent, "A", "none", nil); code != 204 {
		t.Fatalf("none twice must be a no-op, got %d", code)
	}
	if code := api.set(admin, agent, "A", "read", nil); code != 204 {
		t.Fatalf("explicit return: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1 AND tenant_id = $2`, agent, w.tenant["A"]); n != 1 {
		t.Fatalf("the cell must stay one row, got %d", n)
	}
	// validity: the future is fine, the past is not
	past := time.Now().Add(-time.Hour)
	if code := api.set(admin, agent, "C", "read", &past); code != 422 {
		t.Fatalf("a validity in the past: %d", code)
	}
	soon := time.Now().Add(time.Hour)
	if code := api.set(admin, agent, "C", "read", &soon); code != 204 {
		t.Fatalf("a future validity: %d", code)
	}
	if code := api.set(admin, agent, "A", "owner", nil); code != 422 {
		t.Fatalf("an unknown mode: %d", code)
	}
	// a company that is not in this hub is not a cell
	foreign := uuid.New()
	w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, 'Foreign', 'active')`, foreign)
	if code, _ := api.call("PUT", api.base()+"/agents/"+agent.String()+"/instances/"+foreign.String(), admin, map[string]any{"mode": "read"}); code != 404 {
		t.Fatalf("a company outside the hub: %d", code)
	}
	// somebody who is not an agent of this hub cannot be given a cell
	stranger := w.user("stranger")
	if code := api.set(admin, stranger, "A", "read", nil); code != 422 {
		t.Fatalf("a non-member: %d", code)
	}
	// the actor is on the trail, not an anonymous operator
	for _, action := range []string{"hub.grant.granted", "hub.grant.revoked"} {
		if w.auditActor(action, admin) == 0 {
			t.Errorf("%s has no audit event attributed to the admin", action)
		}
	}
}

func TestAccess_AddingAnAgentGrantsNothingAndHubAdminsAreNotAButton(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	otherAdmin := w.hubAdmin("admin2")
	person := w.user("person")
	w.email(person, "pessoa@example.com")
	inactive := w.user("inactive")
	w.email(inactive, "inativa@example.com")
	w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, inactive)

	// unknown and inactive look the same: the screen is not an oracle
	c1, b1 := api.call("POST", api.base()+"/agents", admin, map[string]any{"email": "ninguem@example.com"})
	c2, b2 := api.call("POST", api.base()+"/agents", admin, map[string]any{"email": "inativa@example.com"})
	if c1 != 422 || c2 != 422 || b1 != b2 {
		t.Fatalf("unknown vs inactive must be indistinguishable: %d %q / %d %q", c1, b1, c2, b2)
	}
	if code, _ := api.call("POST", api.base()+"/agents", admin, map[string]any{"email": "pessoa@example.com"}); code != 200 {
		t.Fatalf("adding an existing account: %d", code)
	}
	if code, _ := api.call("POST", api.base()+"/agents", admin, map[string]any{"email": "PESSOA@example.com"}); code != 200 {
		t.Fatalf("adding again (any case) is idempotent: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, w.hub, person); n != 1 {
		t.Fatalf("one membership expected, got %d", n)
	}
	if w.reads(person, "A").conversations != 0 {
		t.Fatalf("joining the hub opened a company")
	}
	if w.auditActor("hub.member.added", admin) != 1 {
		t.Fatalf("the member addition is not attributed to the admin")
	}
	// hub admins are managed by hubctl only
	if code, _ := api.call("DELETE", api.base()+"/agents/"+otherAdmin.String(), admin, nil); code != 404 {
		t.Fatalf("removing a hub admin from the panel: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, w.hub, otherAdmin); n != 1 {
		t.Fatalf("the hub admin was removed")
	}
	// removing an agent takes the grants with it
	w.exec(`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id) VALUES ($1, $2, $3, $4)`, w.hub, person, w.tenant["A"], w.contract["A"])
	if code, _ := api.call("DELETE", api.base()+"/agents/"+person.String(), admin, nil); code != 204 {
		t.Fatalf("removing an agent: %d", code)
	}
	if w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1`, person) != 0 || w.reads(person, "A").conversations != 0 {
		t.Fatalf("a removed agent still has access")
	}
}

func TestAccess_InstanceAdministrators(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	first, second := w.user("first"), w.user("second")
	w.email(first, "primeiro@example.com")
	w.email(second, "segundo@example.com")
	tenantAdminRole := w.role("tenant_admin")

	path := api.base() + "/instances/" + w.tenant["A"].String() + "/admins"
	if code, _ := api.call("POST", path, admin, map[string]any{"email": "primeiro@example.com"}); code != 200 {
		t.Fatalf("add first admin: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND role_id = $3 AND status = 'active'`, w.tenant["A"], first, tenantAdminRole); n != 1 {
		t.Fatalf("membership not created as tenant_admin")
	}
	if code, _ := api.call("POST", path, admin, map[string]any{"email": "primeiro@example.com"}); code != 200 {
		t.Fatalf("idempotent add: %d", code)
	}
	if w.auditActor("hub.instance.admin_added", admin) != 1 {
		t.Fatalf("the addition should be audited once, with the admin as actor")
	}
	// the last administrator cannot be taken away
	if code, _ := api.call("DELETE", path+"/"+first.String(), admin, nil); code != 422 {
		t.Fatalf("removing the only administrator: %d", code)
	}
	// with a second one it can; the person stays in the company as an agent
	if code, _ := api.call("POST", path, admin, map[string]any{"email": "segundo@example.com"}); code != 200 {
		t.Fatalf("add second: %d", code)
	}
	if code, _ := api.call("DELETE", path+"/"+first.String(), admin, nil); code != 204 {
		t.Fatalf("remove one of two: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM memberships m JOIN roles r ON r.id = m.role_id WHERE m.tenant_id = $1 AND m.user_id = $2 AND m.status = 'active' AND r.key = 'tenant_agent'`, w.tenant["A"], first); n != 1 {
		t.Fatalf("the removed administrator should remain as an agent of the company")
	}
	if code, _ := api.call("DELETE", path+"/"+first.String(), admin, nil); code != 404 {
		t.Fatalf("removing someone who is no longer an administrator: %d", code)
	}
	// a company outside the hub is out of reach
	foreign := uuid.New()
	w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, 'Foreign', 'active')`, foreign)
	if code, _ := api.call("POST", api.base()+"/instances/"+foreign.String()+"/admins", admin, map[string]any{"email": "primeiro@example.com"}); code != 404 {
		t.Fatalf("a company outside the hub: %d", code)
	}
	// and it shows in the overview
	code, ov := api.overview(admin)
	if code != 200 {
		t.Fatalf("overview: %d", code)
	}
	for _, in := range ov.Instances {
		if in.TenantID == w.tenant["A"] && (len(in.Admins) != 1 || in.Admins[0].UserID != second) {
			t.Errorf("overview admins of A: %+v", in.Admins)
		}
	}
}

func TestAccess_HubListAdvertisesThePanelOnlyToHubAdmins(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin, agent := w.hubAdmin("admin"), w.hubAgent("agent")
	for user, want := range map[uuid.UUID]bool{admin: true, agent: false} {
		code, body := api.call("GET", "/api/v1/hubs", user, nil)
		if code != 200 {
			t.Fatalf("hubs: %d", code)
		}
		var out struct {
			Items []struct {
				ID              uuid.UUID `json:"id"`
				CanManageAccess bool      `json:"can_manage_access"`
			} `json:"items"`
		}
		w.must(json.Unmarshal([]byte(body), &out))
		if len(out.Items) != 1 || out.Items[0].CanManageAccess != want {
			t.Errorf("can_manage_access for %s = %+v, want %v", user, out.Items, want)
		}
	}
}

func TestAccess_AnInactiveAccountCannotUseThePanelEvenWithAValidSession(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	if code, _ := api.overview(admin); code != 200 {
		t.Fatalf("active admin: %d", code)
	}
	w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, admin)
	if code, _ := api.overview(admin); code != 404 {
		t.Fatalf("a deactivated hub admin still reads the panel: %d", code)
	}
	target := w.user("target")
	w.email(target, "alvo@example.com")
	if code, _ := api.call("POST", api.base()+"/agents", admin, map[string]any{"email": "alvo@example.com"}); code != 404 {
		t.Fatalf("a deactivated hub admin still writes: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM hub_memberships WHERE user_id = $1`, target); n != 0 {
		t.Fatalf("a refused write changed the database")
	}
}

// Codex: a hubctl promotion racing the panel must never be undone by it (demoted by the upsert, or deleted under its feet).
func TestAccess_APromotionByHubctlIsNeverUndoneByThePanel(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	ctl, err := provisioning.New(w.app, "hubctl-test")
	w.must(err)
	person := w.user("person")
	w.email(person, "pessoa@example.com")

	for round := 0; round < 25; round++ {
		w.exec(`DELETE FROM hub_memberships WHERE user_id = $1`, person)
		var wg sync.WaitGroup
		var ctlErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, _ = api.svc.AddAgent(w.ctx, admin, w.hub, "pessoa@example.com") }()
		go func() { defer wg.Done(); <-start; ctlErr = ctl.AddMember(w.ctx, w.hub, person, provisioning.RoleAdmin) }()
		close(start)
		wg.Wait()
		w.must(ctlErr)
		if n := w.count(`SELECT count(*) FROM hub_memberships hm JOIN roles r ON r.id = hm.role_id WHERE hm.user_id = $1 AND r.key = 'hub_admin'`, person); n != 1 {
			t.Fatalf("round %d: the promotion to hub admin did not survive the panel", round)
		}
	}
	// the same for removal
	for round := 0; round < 25; round++ {
		w.exec(`DELETE FROM hub_memberships WHERE user_id = $1`, person)
		w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, person, w.roleHubAgent)
		var wg sync.WaitGroup
		var ctlErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _ = api.svc.RemoveAgent(w.ctx, admin, w.hub, person) }()
		go func() { defer wg.Done(); <-start; ctlErr = ctl.AddMember(w.ctx, w.hub, person, provisioning.RoleAdmin) }()
		close(start)
		wg.Wait()
		w.must(ctlErr)
		if n := w.count(`SELECT count(*) FROM hub_memberships hm JOIN roles r ON r.id = hm.role_id WHERE hm.user_id = $1 AND r.key = 'hub_admin'`, person); n != 1 {
			t.Fatalf("round %d: the panel deleted a hub admin that hubctl had just promoted", round)
		}
	}
}
