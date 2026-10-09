package adapters_test

// Work pools through the real handler (ADR-0038 phase 4): only an admin of the hub, uniform 404 for everybody else, and what a call
// really changes in the database.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/adapters"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
)

type poolsAPI struct {
	w   *world
	srv *httptest.Server
}

func newPoolsAPI(t *testing.T, w *world) *poolsAPI {
	t.Helper()
	h := adapters.NewPoolsHandler(w.app)
	shim := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if raw := r.Header.Get("X-Test-User"); raw != "" {
				id, _ := uuid.Parse(raw)
				r = r.WithContext(contextWithPrincipal(r, id))
			}
			next.ServeHTTP(rw, r)
		})
	}
	session := tenancyadapters.UserSessionMiddleware(w.app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hubs/{hub_id}/pools", shim(session(http.HandlerFunc(h.List))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/pools", shim(session(http.HandlerFunc(h.Create))))
	mux.Handle("PATCH /api/v1/hubs/{hub_id}/pools/{pool_id}", shim(session(http.HandlerFunc(h.Update))))
	mux.Handle("DELETE /api/v1/hubs/{hub_id}/pools/{pool_id}", shim(session(http.HandlerFunc(h.Delete))))
	mux.Handle("PUT /api/v1/hubs/{hub_id}/pools/{pool_id}/members", shim(session(http.HandlerFunc(h.SetMembers))))
	mux.Handle("PUT /api/v1/hubs/{hub_id}/pools/{pool_id}/instances", shim(session(http.HandlerFunc(h.SetInstances))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &poolsAPI{w: w, srv: srv}
}

func (a *poolsAPI) call(method, path string, user uuid.UUID, body any) (int, string) {
	a.w.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		a.w.must(err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.srv.URL+"/api/v1/hubs/"+a.w.hub.String()+path, rd)
	a.w.must(err)
	req.Header.Set("Content-Type", "application/json")
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestPoolsAPI_OnlyAnAdminOfTheHubAndWhatItChanges(t *testing.T) {
	w := newWorld(t)
	api := newPoolsAPI(t, w)
	admin := w.hubAdmin("admin")
	agent := w.hubAgent("agent")
	member := w.hubAgent("member")

	for name, u := range map[string]uuid.UUID{"agent": agent, "anonymous": uuid.Nil} {
		want := 404
		if u == uuid.Nil {
			want = 401
		}
		for _, c := range []struct {
			m, p string
			body any
		}{
			{"GET", "/pools", nil},
			{"POST", "/pools", map[string]any{"name": "x", "distribution": "manual"}},
			{"PATCH", "/pools/" + uuid.NewString(), map[string]any{"name": "x"}},
			{"DELETE", "/pools/" + uuid.NewString(), nil},
			{"PUT", "/pools/" + uuid.NewString() + "/members", map[string]any{"members": []any{}}},
			{"PUT", "/pools/" + uuid.NewString() + "/instances", map[string]any{"instances": []any{}}},
		} {
			if code, _ := api.call(c.m, c.p, u, c.body); code != want {
				t.Errorf("%s %s %s: %d, want %d", name, c.m, c.p, code, want)
			}
		}
	}
	if n := w.count(`SELECT count(*) FROM work_pools WHERE hub_id = $1`, w.hub); n != 0 {
		t.Fatalf("a refused call created %d pool(s)", n)
	}

	code, body := api.call("POST", "/pools", admin, map[string]any{"name": "Suporte", "distribution": "round_robin"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var p struct {
		ID uuid.UUID `json:"id"`
	}
	w.must(json.Unmarshal([]byte(body), &p))
	if code, _ := api.call("POST", "/pools", admin, map[string]any{"name": " ", "distribution": "manual"}); code != http.StatusUnprocessableEntity {
		t.Errorf("an empty name: %d", code)
	}
	if code, _ := api.call("POST", "/pools", admin, map[string]any{"name": "Y", "distribution": "chaos"}); code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown distribution: %d", code)
	}
	if code, _ := api.call("PUT", "/pools/"+p.ID.String()+"/members", admin, map[string]any{"members": []map[string]any{{"user_id": member, "max_open": 3}}}); code != http.StatusNoContent {
		t.Fatalf("members: %d", code)
	}
	if code, _ := api.call("PUT", "/pools/"+p.ID.String()+"/members", admin, map[string]any{"members": []map[string]any{{"user_id": uuid.New()}}}); code != http.StatusNotFound {
		t.Errorf("a person outside the hub: %d", code)
	}
	if code, _ := api.call("PUT", "/pools/"+p.ID.String()+"/instances", admin, map[string]any{"instances": []map[string]any{{"tenant_id": w.tenant["A"]}}}); code != http.StatusNoContent {
		t.Fatalf("instances: %d", code)
	}
	q, err := w.pool2(admin)
	w.must(err)
	if code, _ := api.call("PUT", "/pools/"+q.String()+"/instances", admin, map[string]any{"instances": []map[string]any{{"tenant_id": w.tenant["A"]}}}); code != http.StatusConflict {
		t.Errorf("a second pool for the same instance: %d", code)
	}
	if code, body := api.call("GET", "/pools", admin, nil); code != http.StatusOK || len(body) < 10 {
		t.Errorf("list: %d %s", code, body)
	}
	name := "Suporte N1"
	if code, _ := api.call("PATCH", "/pools/"+p.ID.String(), admin, map[string]any{"name": name}); code != http.StatusNoContent {
		t.Errorf("rename: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM work_pools WHERE id = $1 AND name = $2 AND distribution = 'round_robin'`, p.ID, name); n != 1 {
		t.Error("the rename must keep the distribution")
	}
	if code, _ := api.call("DELETE", "/pools/"+p.ID.String(), admin, nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if n := w.count(`SELECT count(*) FROM work_pool_instances WHERE work_pool_id = $1`, p.ID); n != 0 {
		t.Error("deleting a pool deletes its instances")
	}
	if n := w.auditActor("hub.pool.created", admin); n != 1 {
		t.Errorf("creations must be audited with the actor, got %d", n)
	}
}

func (w *world) pool2(admin uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := w.owner.QueryRow(w.ctx, `INSERT INTO work_pools (hub_id, name) VALUES ($1, 'Segunda') RETURNING id`, w.hub).Scan(&id)
	return id, err
}
