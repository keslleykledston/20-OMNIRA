package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	"github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/flowstest"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// harness mounts the real routes behind a test "middleware" that does what the product's does: a tenant session for the
// calling user plus a TenantContext. The user comes from the X-Test-User header; the tenant from the URL.
type harness struct {
	t   *testing.T
	env *flowstest.Env
	mux *http.ServeMux
}

func newHarness(t *testing.T) *harness {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	h := adapters.NewHandler(env.App, application.NewControlPlane(repo, repo, nil))
	mux := http.NewServeMux()
	h.Routes(mux, func(fn http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, uerr := uuid.Parse(r.Header.Get("X-Test-User"))
			tenant, terr := uuid.Parse(r.PathValue("tenant_id"))
			if uerr != nil || terr != nil {
				fn(w, r) // no TenantContext: the handler must answer 401
				return
			}
			_ = platformdb.WithTenantSession(r.Context(), env.App, user, false, func(sc context.Context) error {
				tc, _ := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
				fn(w, r.WithContext(tenancydomain.WithTenantContext(sc, tc)))
				return nil
			})
		})
	})
	return &harness{t: t, env: env, mux: mux}
}

// member adds a user with the given system role to a tenant.
func (hn *harness) member(tenant uuid.UUID, role string) uuid.UUID {
	hn.t.Helper()
	ctx := context.Background()
	user := uuid.New()
	var roleID uuid.UUID
	if err := hn.env.Seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, role).Scan(&roleID); err != nil {
		hn.t.Fatal(err)
	}
	if _, err := hn.env.Seed.Exec(ctx, `INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, user, user, user.String()+"@invalid"); err != nil {
		hn.t.Fatal(err)
	}
	if _, err := hn.env.Seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, user, roleID); err != nil {
		hn.t.Fatal(err)
	}
	hn.t.Cleanup(func() { _, _ = hn.env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) })
	return user
}

func (hn *harness) call(user uuid.UUID, tenant uuid.UUID, method, path string, body any) (int, map[string]any) {
	hn.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, fmt.Sprintf("/api/v1/tenants/%s%s", tenant, path), rd)
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	rec := httptest.NewRecorder()
	hn.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestHTTPRBACAndLifecycle(t *testing.T) {
	hn := newHarness(t)
	env := hn.env
	admin := env.UserA
	supervisor := hn.member(env.TenantA, "tenant_supervisor")
	agent := hn.member(env.TenantA, "tenant_agent")

	// authentication: no TenantContext => 401
	if code, _ := hn.call(uuid.Nil, env.TenantA, "GET", "/flows", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	// authorization matrix
	for _, c := range []struct {
		who          string
		user         uuid.UUID
		method, path string
		body         any
		want         int
	}{
		{"agent cannot even list", agent, "GET", "/flows", nil, 403},
		{"supervisor can list", supervisor, "GET", "/flows", nil, 200},
		{"supervisor cannot create", supervisor, "POST", "/flows", map[string]any{"slug": "x", "name": "X"}, 403},
		{"agent cannot read node catalog", agent, "GET", "/flow-node-types", nil, 403},
		{"supervisor reads node catalog", supervisor, "GET", "/flow-node-types", nil, 200},
	} {
		if code, _ := hn.call(c.user, env.TenantA, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s: got %d want %d", c.who, code, c.want)
		}
	}

	// admin lifecycle
	code, flow := hn.call(admin, env.TenantA, "POST", "/flows", map[string]any{"slug": "reception", "name": "Reception"})
	if code != 201 {
		t.Fatalf("create: %d %v", code, flow)
	}
	id := flow["id"].(string)
	if code, _ := hn.call(admin, env.TenantA, "POST", "/flows", map[string]any{"slug": "reception", "name": "Again"}); code != 409 {
		t.Fatalf("duplicate slug must be 409, got %d", code)
	}
	if code, _ := hn.call(admin, env.TenantA, "POST", "/flows", map[string]any{"slug": "BAD SLUG", "name": "x"}); code != 400 {
		t.Fatalf("bad slug must be 400, got %d", code)
	}
	if code, _ := hn.call(admin, env.TenantA, "POST", "/flows", "not an object"); code != 400 {
		t.Fatalf("bad body must be 400, got %d", code)
	}

	// An empty draft cannot be published: 422 with actionable issues.
	code, body := hn.call(admin, env.TenantA, "POST", "/flows/"+id+"/publish", map[string]any{"revision": 1})
	if code != 422 || len(body["issues"].([]any)) == 0 {
		t.Fatalf("empty draft publish: %d %v", code, body)
	}

	queue := uuid.New()
	if _, err := env.Seed.Exec(context.Background(), `INSERT INTO queues(id, tenant_id, name) VALUES($1,$2,'q')`, queue, env.TenantA); err != nil {
		t.Fatal(err)
	}
	def := json.RawMessage(fmt.Sprintf(`{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"q","type":"assign_queue","config":{"queue":"%s"}},{"id":"h","type":"human_handoff"}],
	  "edges":[{"id":"1","source":"start","sourcePort":"next","target":"q"},{"id":"2","source":"q","sourcePort":"next","target":"h"}]}`, queue))
	// supervisor (view, no edit) cannot save; admin can
	if code, _ := hn.call(supervisor, env.TenantA, "PUT", "/flows/"+id+"/draft", map[string]any{"revision": 1, "name": "Reception", "definition": def}); code != 403 {
		t.Fatalf("supervisor must not edit: %d", code)
	}
	code, saved := hn.call(admin, env.TenantA, "PUT", "/flows/"+id+"/draft", map[string]any{"revision": 1, "name": "Reception", "definition": def})
	if code != 200 {
		t.Fatalf("save: %d %v", code, saved)
	}
	// stale revision => 409
	if code, _ := hn.call(admin, env.TenantA, "PUT", "/flows/"+id+"/draft", map[string]any{"revision": 1, "name": "Reception", "definition": def}); code != 409 {
		t.Fatalf("stale save must be 409, got %d", code)
	}
	// supervisor cannot publish (edit and publish are distinct permissions), admin can
	if code, _ := hn.call(supervisor, env.TenantA, "POST", "/flows/"+id+"/publish", map[string]any{"revision": 2}); code != 403 {
		t.Fatalf("supervisor must not publish: %d", code)
	}
	if code, body := hn.call(admin, env.TenantA, "POST", "/flows/"+id+"/publish", map[string]any{"revision": 2, "note": "first"}); code != 201 {
		t.Fatalf("publish: %d %v", code, body)
	}
	code, got := hn.call(supervisor, env.TenantA, "GET", "/flows/"+id, nil)
	if code != 200 || got["active_version"].(float64) != 1 || got["status"] != "published" {
		t.Fatalf("get: %d %v", code, got)
	}
	if code, _ := hn.call(supervisor, env.TenantA, "POST", "/flows/"+id+"/archive", nil); code != 403 {
		t.Fatalf("supervisor must not archive: %d", code)
	}
	if code, _ := hn.call(admin, env.TenantA, "POST", "/flows/"+id+"/versions/1/activate", nil); code != 200 {
		t.Fatalf("activate: %d", code)
	}
	if code, _ := hn.call(admin, env.TenantA, "POST", "/flows/"+id+"/versions/9/activate", nil); code != 404 {
		t.Fatalf("unknown version must be 404, got %d", code)
	}
	if code, _ := hn.call(admin, env.TenantA, "POST", "/flows/"+id+"/archive", nil); code != 204 {
		t.Fatalf("archive: %d", code)
	}

	// Tenant isolation over HTTP: tenant B's admin cannot see, edit or publish tenant A's flow, and a tenant B session
	// pointed at tenant A's URL is not a member there (403: the permission check is membership-based).
	if code, _ := hn.call(env.UserB, env.TenantB, "GET", "/flows/"+id, nil); code != 404 {
		t.Fatalf("tenant B must not see tenant A's flow: %d", code)
	}
	if code, _ := hn.call(env.UserB, env.TenantB, "PUT", "/flows/"+id+"/draft", map[string]any{"revision": 1, "name": "x", "definition": def}); code != 404 {
		t.Fatalf("tenant B must not edit tenant A's flow: %d", code)
	}
	if code, _ := hn.call(env.UserB, env.TenantA, "GET", "/flows", nil); code != 403 {
		t.Fatalf("a non-member must be refused on another tenant's URL: %d", code)
	}
	code, list := hn.call(env.UserB, env.TenantB, "GET", "/flows", nil)
	if code != 200 || len(list["items"].([]any)) != 0 {
		t.Fatalf("tenant B list must be empty: %d %v", code, list)
	}
}

func TestHTTPLiveValidationAndNodeCatalog(t *testing.T) {
	hn := newHarness(t)
	env := hn.env
	code, body := hn.call(env.UserA, env.TenantA, "POST", "/flows/validate", map[string]any{"definition": json.RawMessage(`{"schema_version":1,"nodes":[],"edges":[]}`)})
	if code != 200 || body["valid"] != false {
		t.Fatalf("live validation: %d %v", code, body)
	}
	code, cat := hn.call(env.UserA, env.TenantA, "GET", "/flow-node-types", nil)
	items, _ := cat["items"].([]any)
	if code != 200 || len(items) != 17 {
		t.Fatalf("catalog: %d %d", code, len(items))
	}
}
