package adapters_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ADR-0044 x ADR-0040: sectors, calendars, shifts, on-call, overflow and SLA are administration of the company. They are mounted exactly
// like server.go does (AuthorizationMiddleware, NOT Delegable), so a request acting for a Hub never reaches them: 404 before the handler,
// even for a person who is both a Hub operator with a grant naming these very keys and an administrator member of the company.
func TestRoutingAdministrationIsUnreachableActingForAHub(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	tenantSession := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	h := sectorsadapters.NewHandler(w.app, auditadapters.NewPostgresAuditEventRepository(w.app))
	var reached atomic.Int32
	count := func(fn http.HandlerFunc) http.Handler {
		return tenantSession(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { reached.Add(1); fn(rw, r) }))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/sectors", count(h.ListSectors))
	mux.Handle("POST /api/v1/tenants/{tenant_id}/sectors", count(h.CreateSector))
	mux.Handle("GET /api/v1/tenants/{tenant_id}/shifts", count(h.ListShifts))
	mux.Handle("PUT /api/v1/tenants/{tenant_id}/settings/default-calendar", count(h.SetDefaultCalendar))
	mux.Handle("GET /api/v1/tenants/{tenant_id}/sla-policies", count(h.ListSLAPolicies))
	chain := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if u := r.Header.Get("X-Test-User"); u != "" {
			id, _ := uuid.Parse(u)
			r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: id}))
		}
		mux.ServeHTTP(rw, r)
	})

	do := func(user uuid.UUID, method, path, acting, body string) int {
		req := httptest.NewRequest(method, "/api/v1/tenants/"+w.tenant["A"].String()+path, strings.NewReader(body))
		req.Header.Set("X-Test-User", user.String())
		if acting != "" {
			req.Header.Set(tenancydomain.ActingAsHeader, acting)
		}
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec.Code
	}

	// the worst case: a Hub operator whose contract ceiling and grant name the routing keys, who is ALSO an administrator member
	op := w.hubAgent("router")
	g := w.grant(op, "A")
	keys := []string{"conversation.read", "agent.read", "agent.manage", "routing.override"}
	w.ceiling("A", keys...)
	w.grantKeys(g, keys...)
	w.exec(`INSERT INTO memberships (tenant_id, user_id, role_id) VALUES ($1, $2, (SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1))`, w.tenant["A"], op)
	hub := "hub:" + w.hub.String()

	// non-vacuous: the same person acting as a plain member (no header) administers the company
	if code := do(op, "GET", "/sectors", "", ""); code != http.StatusOK {
		t.Fatalf("baseline: the administrator reads sectors as a member: %d", code)
	}
	before := reached.Load()
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/sectors", ""},
		{"POST", "/sectors", `{"name":"Invadido"}`},
		{"GET", "/shifts", ""},
		{"PUT", "/settings/default-calendar", `{}`},
		{"GET", "/sla-policies", ""},
	} {
		if code := do(op, rt.method, rt.path, hub, rt.body); code != http.StatusNotFound {
			t.Errorf("%s %s acting for the hub = %d, want 404 (not delegable)", rt.method, rt.path, code)
		}
	}
	if reached.Load() != before {
		t.Fatalf("a handler ran for a request acting for a hub")
	}
	if n := w.n(op, `SELECT count(*) FROM sectors WHERE name='Invadido'`); n != 0 {
		t.Fatalf("the refused create wrote %d rows", n)
	}
}
