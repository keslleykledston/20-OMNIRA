package adapters_test

// Control plane (ADR-0038 phase 1) on a real PostgreSQL, through the real handler and the caller's own RLS session:
// who may manage companies, what creating a company does and does not grant, suspension, and the capability switches.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/entitlements"
	"github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/hub/companies"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type adminAPI struct {
	w   *world
	srv *httptest.Server
}

func newAdminAPI(t *testing.T, w *world) *adminAPI {
	t.Helper()
	h := adapters.NewHTTPHandler(w.app).WithAdminAPI(true)
	a := adapters.NewAdminHandler(w.app)
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
	mux.Handle("GET /api/v1/hubs/{hub_id}/companies", shim(session(http.HandlerFunc(a.ListCompanies))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/companies", shim(session(http.HandlerFunc(a.CreateCompany))))
	mux.Handle("PATCH /api/v1/hubs/{hub_id}/companies/{tenant_id}", shim(session(http.HandlerFunc(a.UpdateCompany))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/inbox", shim(session(http.HandlerFunc(h.ListInbox))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &adminAPI{w: w, srv: srv}
}

func (a *adminAPI) call(method, path string, user uuid.UUID, body any, hdr map[string]string) (int, string, http.Header) {
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
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

type companyJSON struct {
	ID             uuid.UUID       `json:"id"`
	LegalName      string          `json:"legal_name"`
	DisplayName    string          `json:"display_name"`
	Status         string          `json:"status"`
	ContractStatus string          `json:"contract_status"`
	Capabilities   map[string]bool `json:"capabilities"`
	Agents         int             `json:"agents"`
}

func (a *adminAPI) list(user, hub uuid.UUID) (int, []companyJSON) {
	code, body, _ := a.call("GET", "/api/v1/hubs/"+hub.String()+"/companies", user, nil, nil)
	var out struct {
		Items        []companyJSON             `json:"items"`
		Capabilities []entitlements.Capability `json:"capabilities"`
	}
	if code == 200 {
		a.w.must(json.Unmarshal([]byte(body), &out))
		if len(out.Capabilities) != len(entitlements.Registry) {
			a.w.t.Errorf("the capability catalog is part of the answer: %d", len(out.Capabilities))
		}
	}
	return code, out.Items
}

// operator makes a user a platform operator and an admin of the world's hub.
func (w *world) operator(name string, admin, operator bool) uuid.UUID {
	u := w.user(name)
	role := w.roleHubAgent
	if admin {
		role = w.roleHubAdmin
	}
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, u, role)
	if operator {
		w.exec(`INSERT INTO platform_operators (user_id, granted_by) VALUES ($1, 'test')`, u)
	}
	return u
}

func TestHubAdmin_OnlyAnOperatorWhoAdministersTheHubMayManageCompanies(t *testing.T) {
	w := newWorld(t)
	api := newAdminAPI(t, w)
	op := w.operator("op", true, true)
	agentOperator := w.operator("agentop", false, true) // operator, but only an agent of this hub
	adminOnly := w.operator("adminonly", true, false)   // hub admin, but not a platform operator
	outsider := w.user("outsider")
	otherHub := uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Other')`, otherHub)
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, otherHub, op, w.roleHubAdmin)

	hubPath := "/api/v1/hubs/" + w.hub.String() + "/companies"
	before := w.count(`SELECT count(*) FROM tenants`)
	attempts := map[string]uuid.UUID{"agent operator": agentOperator, "hub admin without operator": adminOnly, "outsider": outsider}
	for name, u := range attempts {
		t.Run(name+" gets one uniform 404 on every route", func(t *testing.T) {
			if code, _ := api.list(u, w.hub); code != 404 {
				t.Errorf("list: %d", code)
			}
			if code, _, _ := api.call("POST", hubPath, u, map[string]any{"legal_name": "Intruder"}, map[string]string{"Idempotency-Key": "key-" + name + "-001"}); code != 404 {
				t.Errorf("create: %d", code)
			}
			if code, _, _ := api.call("PATCH", hubPath+"/"+w.tenant["A"].String(), u, map[string]any{"status": "suspended"}, nil); code != 404 {
				t.Errorf("update: %d", code)
			}
		})
	}
	if w.count(`SELECT count(*) FROM tenants`) != before || w.count(`SELECT count(*) FROM tenants WHERE id = $1 AND status = 'active'`, w.tenant["A"]) != 1 {
		t.Fatal("a refused caller changed something")
	}
	t.Run("the operator's rights are per hub: an unknown hub is a 404 and another hub lists none of this hub's companies", func(t *testing.T) {
		if code, _ := api.list(op, uuid.New()); code != 404 {
			t.Errorf("unknown hub: %d", code)
		}
		if code, items := api.list(op, otherHub); code != 200 || len(items) != 0 {
			t.Errorf("another hub the operator administers: %d, %d companies (must be its own, empty)", code, len(items))
		}
		if code, _, _ := api.call("PATCH", "/api/v1/hubs/"+otherHub.String()+"/companies/"+w.tenant["A"].String(), op, map[string]any{"status": "suspended"}, nil); code != 404 {
			t.Errorf("a company of hub 1 changed through hub 2: %d", code)
		}
	})
	t.Run("a revoked operator loses everything at once", func(t *testing.T) {
		if code, _ := api.list(op, w.hub); code != 200 {
			t.Fatalf("operator: %d", code)
		}
		w.exec(`UPDATE platform_operators SET status = 'revoked', revoked_at = now() WHERE user_id = $1`, op)
		if code, _ := api.list(op, w.hub); code != 404 {
			t.Errorf("revoked operator: %d", code)
		}
		w.exec(`UPDATE platform_operators SET status = 'active', revoked_at = NULL WHERE user_id = $1`, op)
	})
	t.Run("a suspended hub stops its administrators", func(t *testing.T) {
		w.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, w.hub)
		if code, _ := api.list(op, w.hub); code != 404 {
			t.Errorf("suspended hub: %d", code)
		}
		w.exec(`UPDATE service_hubs SET status = 'active' WHERE id = $1`, w.hub)
	})
	t.Run("forged tenant selector is a 400, not an input", func(t *testing.T) {
		if code, _, _ := api.call("GET", hubPath+"?tenant_id="+w.tenant["A"].String(), op, nil, nil); code != 400 {
			t.Errorf("%d", code)
		}
	})
}

func TestHubAdmin_CreateCompany(t *testing.T) {
	w := newWorld(t)
	api := newAdminAPI(t, w)
	op := w.operator("op", true, true)
	alice := w.hubAgent("alice")
	w.grant(alice, "A")
	existing := w.user("future-admin")
	existingEmail := "first-admin-" + existing.String()[:8] + "@example.test"
	w.exec(`UPDATE users SET email = $2 WHERE id = $1`, existing, existingEmail)
	path := "/api/v1/hubs/" + w.hub.String() + "/companies"
	key := map[string]string{"Idempotency-Key": "create-0001"}

	t.Run("creates the tenant, a default queue and a contract with this hub - and grants nobody anything", func(t *testing.T) {
		code, body, _ := api.call("POST", path, op, map[string]any{"legal_name": "  Nova Empresa Ltda ", "trade_name": "Nova"}, key)
		if code != 201 {
			t.Fatalf("%d %s", code, body)
		}
		var co companyJSON
		w.must(json.Unmarshal([]byte(body), &co))
		if co.LegalName != "Nova Empresa Ltda" || co.Status != "active" || co.ContractStatus != "active" || co.DisplayName != "Nova" {
			t.Fatalf("%+v", co)
		}
		for _, c := range entitlements.Registry {
			if !co.Capabilities[c.Key] {
				t.Errorf("a new company starts with every capability on, %s is off", c.Key)
			}
		}
		if w.count(`SELECT count(*) FROM queues WHERE tenant_id = $1 AND is_default AND mode = 'manual'`, co.ID) != 1 {
			t.Error("no default queue")
		}
		if w.count(`SELECT count(*) FROM hub_tenant_service_contracts WHERE hub_id = $1 AND tenant_id = $2 AND status = 'active'`, w.hub, co.ID) != 1 {
			t.Error("no contract with the creating hub")
		}
		if w.count(`SELECT count(*) FROM effective_access_grants WHERE tenant_id = $1`, co.ID) != 0 || w.count(`SELECT count(*) FROM memberships WHERE tenant_id = $1`, co.ID) != 0 {
			t.Error("creating a company must not give anyone access to it")
		}
		if w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'platform.company.created'`, co.ID, op) != 1 {
			t.Error("creation is not audited under the operator")
		}
		// Neither the operator nor an existing agent can read its data through the Hub.
		if w.n(op, `SELECT count(*) FROM conversations WHERE tenant_id = $1`, co.ID) != 0 || w.n(alice, `SELECT count(*) FROM tenants WHERE id = $1`, co.ID) != 0 {
			t.Error("the new company is visible to someone with no grant")
		}
		if code, items := api.list(op, w.hub); code != 200 {
			t.Errorf("list: %d", code)
		} else {
			found := false
			for _, it := range items {
				found = found || it.ID == co.ID
			}
			if !found || len(items) != 4 {
				t.Errorf("the hub lists its 3 fixtures and the new company, got %d (found=%v)", len(items), found)
			}
		}
	})
	t.Run("retry with the same key returns the same company, once", func(t *testing.T) {
		n := w.count(`SELECT count(*) FROM tenants`)
		code, body, h := api.call("POST", path, op, map[string]any{"legal_name": "  Nova Empresa Ltda ", "trade_name": "Nova"}, key)
		if code != 200 || h.Get("Idempotent-Replayed") != "true" {
			t.Fatalf("%d %s %v", code, body, h)
		}
		if w.count(`SELECT count(*) FROM tenants`) != n {
			t.Error("a retry created a second company")
		}
		if code, _, _ := api.call("POST", path, op, map[string]any{"legal_name": "Outra Empresa"}, key); code != 422 {
			t.Errorf("same key, other body: %d", code)
		}
	})
	t.Run("validation, and nothing half-created on a refusal", func(t *testing.T) {
		n := w.count(`SELECT count(*) FROM tenants`)
		nq := w.count(`SELECT count(*) FROM queues`)
		cases := map[string]struct {
			body any
			hdr  map[string]string
			want int
		}{
			"no key":                {map[string]any{"legal_name": "Valid Name"}, nil, 400},
			"short key":             {map[string]any{"legal_name": "Valid Name"}, map[string]string{"Idempotency-Key": "short"}, 422},
			"unknown field":         {map[string]any{"legal_name": "Valid Name", "status": "suspended"}, map[string]string{"Idempotency-Key": "valid-key-0001"}, 400},
			"tenant id from client": {map[string]any{"legal_name": "Valid Name", "id": uuid.NewString()}, map[string]string{"Idempotency-Key": "valid-key-0002"}, 400},
			"name too short":        {map[string]any{"legal_name": "x"}, map[string]string{"Idempotency-Key": "valid-key-0003"}, 422},
			"admin does not exist":  {map[string]any{"legal_name": "Valid Name", "initial_admin_email": "nobody-at-all@example.test"}, map[string]string{"Idempotency-Key": "valid-key-0004"}, 422},
			"admin is not an email": {map[string]any{"legal_name": "Valid Name", "initial_admin_email": "nope"}, map[string]string{"Idempotency-Key": "valid-key-0005"}, 422},
		}
		for name, c := range cases {
			if code, body, _ := api.call("POST", path, op, c.body, c.hdr); code != c.want {
				t.Errorf("%s: %d %s, want %d", name, code, body, c.want)
			}
		}
		if w.count(`SELECT count(*) FROM tenants`) != n || w.count(`SELECT count(*) FROM queues`) != nq {
			t.Error("a refused creation left a tenant or a queue behind")
		}
	})
	t.Run("an existing user can be made the first administrator", func(t *testing.T) {
		code, body, _ := api.call("POST", path, op, map[string]any{"legal_name": "Com Admin Ltda", "initial_admin_email": strings.ToUpper(existingEmail)}, map[string]string{"Idempotency-Key": "admin-key-0001"})
		if code != 201 {
			t.Fatalf("%d %s", code, body)
		}
		var co companyJSON
		w.must(json.Unmarshal([]byte(body), &co))
		if w.count(`SELECT count(*) FROM memberships m JOIN roles r ON r.id = m.role_id WHERE m.tenant_id = $1 AND m.user_id = $2 AND r.key = 'tenant_admin' AND m.status = 'active'`, co.ID, existing) != 1 {
			t.Error("the first administrator was not made tenant_admin")
		}
	})
	t.Run("the hub list tells the screen who may manage companies", func(t *testing.T) {
		manage := func(u uuid.UUID) bool {
			_, hubs, _ := (&hubAPI{w: w, srv: api.srv}).myHubs(u)
			for _, h := range hubs {
				if h.ID == w.hub {
					return h.CanManageCompanies
				}
			}
			return false
		}
		if !manage(op) || manage(alice) {
			t.Error("can_manage_companies must be true for the operator-admin and false for an agent")
		}
	})
}

func TestHubAdmin_SuspendReactivateAndCapabilities(t *testing.T) {
	w := newWorld(t)
	api := newAdminAPI(t, w)
	op := w.operator("op", true, true)
	alice := w.hubAgent("alice")
	w.grant(alice, "A")
	w.grant(alice, "B")
	d := uuid.New() // a company that exists but has NO contract with this hub
	w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, 'Not ours', 'active')`, d)
	base := "/api/v1/hubs/" + w.hub.String() + "/companies/"

	t.Run("a company that is not the hub's is a 404 and is left alone", func(t *testing.T) {
		if code, _, _ := api.call("PATCH", base+d.String(), op, map[string]any{"status": "suspended"}, nil); code != 404 {
			t.Errorf("%d", code)
		}
		if code, _, _ := api.call("PATCH", base+uuid.NewString(), op, map[string]any{"status": "suspended"}, nil); code != 404 {
			t.Errorf("unknown: %d", code)
		}
		if w.count(`SELECT count(*) FROM tenants WHERE id = $1 AND status = 'active'`, d) != 1 {
			t.Error("a company outside the hub was changed")
		}
	})
	t.Run("validation", func(t *testing.T) {
		for name, b := range map[string]map[string]any{
			"empty":              {},
			"bad status":         {"status": "deleted"},
			"unknown capability": {"capabilities": map[string]bool{"root_access": true}},
			"unknown field":      {"legal_name": "renamed"},
		} {
			code, _, _ := api.call("PATCH", base+w.tenant["A"].String(), op, b, nil)
			if (name == "unknown field" && code != 400) || (name != "unknown field" && code != 422) {
				t.Errorf("%s: %d", name, code)
			}
		}
	})
	t.Run("suspending hides the company from the Hub and reactivating brings it back, with its grants", func(t *testing.T) {
		agent := &hubAPI{w: w, srv: api.srv}
		count := func() int { _, l := agent.list(alice, ""); return len(l.Items) }
		both := count()
		code, body, _ := api.call("PATCH", base+w.tenant["A"].String(), op, map[string]any{"status": "suspended"}, nil)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		if got := count(); got >= both {
			t.Errorf("a suspended company is still served: %d -> %d", both, got)
		}
		if w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'platform.company.status_changed' AND metadata->>'to' = 'suspended'`, w.tenant["A"], op) != 1 {
			t.Error("suspension not audited")
		}
		// idempotent: suspending again changes and audits nothing
		api.call("PATCH", base+w.tenant["A"].String(), op, map[string]any{"status": "suspended"}, nil)
		if w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND action = 'platform.company.status_changed'`, w.tenant["A"]) != 1 {
			t.Error("a repeated suspension was audited again")
		}
		// the operator still lists it (to reactivate it)
		_, items := api.list(op, w.hub)
		seen := false
		for _, it := range items {
			if it.ID == w.tenant["A"] {
				seen = it.Status == "suspended"
			}
		}
		if !seen {
			t.Error("a suspended company must stay listed for the operator")
		}
		if code, _, _ := api.call("PATCH", base+w.tenant["A"].String(), op, map[string]any{"status": "active"}, nil); code != 200 {
			t.Fatal("reactivate failed")
		}
		if got := count(); got != both {
			t.Errorf("reactivated: %d, want %d", got, both)
		}
		if w.count(`SELECT count(*) FROM effective_access_grants WHERE tenant_id = $1 AND status = 'active'`, w.tenant["A"]) != 1 {
			t.Error("suspension must not delete grants")
		}
	})
	t.Run("a company that is neither active nor suspended is not switched from here", func(t *testing.T) {
		w.exec(`UPDATE tenants SET status = 'inactive' WHERE id = $1`, w.tenant["C"])
		if code, _, _ := api.call("PATCH", base+w.tenant["C"].String(), op, map[string]any{"status": "active"}, nil); code != 422 {
			t.Errorf("%d", code)
		}
	})
	t.Run("capability switches: default on, one company at a time, audited once per real change, enforced by the server gate", func(t *testing.T) {
		checker := entitlements.NewChecker(w.app)
		member := w.user("member-of-B")
		w.directMember(member, "B")
		memberA := w.user("member-of-A")
		w.directMember(memberA, "A")
		enabled := func(u uuid.UUID, tenantKey, capability string) bool {
			var ok bool
			w.must(platformdb.WithTenantSession(w.ctx, w.app, u, false, func(c context.Context) error {
				var err error
				ok, err = checker.Enabled(c, w.tenant[tenantKey], capability)
				return err
			}))
			return ok
		}
		if !enabled(memberA, "A", entitlements.WhatsAppChannel) {
			t.Fatal("capabilities default to on: existing companies must keep everything")
		}
		code, body, _ := api.call("PATCH", base+w.tenant["A"].String(), op, map[string]any{"capabilities": map[string]bool{entitlements.WhatsAppChannel: false, entitlements.ERPCRM: true}}, nil)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var co companyJSON
		w.must(json.Unmarshal([]byte(body), &co))
		if co.Capabilities[entitlements.WhatsAppChannel] || !co.Capabilities[entitlements.ERPCRM] || !co.Capabilities[entitlements.OutboundAttachments] {
			t.Errorf("effective switches: %+v", co.Capabilities)
		}
		if enabled(memberA, "A", entitlements.WhatsAppChannel) {
			t.Error("the gate still lets the company use a capability that was switched off")
		}
		if !enabled(memberA, "A", entitlements.ERPCRM) || !enabled(member, "B", entitlements.WhatsAppChannel) {
			t.Error("switching one capability of one company must not touch the others")
		}
		if enabled(memberA, "A", "no_such_capability") {
			t.Error("an unknown capability must never be enabled")
		}
		// ERPCRM true was already the effective value: only the real change is audited.
		if w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND action = 'platform.company.capability_changed'`, w.tenant["A"]) != 1 {
			t.Error("exactly one capability change should be audited")
		}
		// the Require middleware answers 403 for the company whose switch is off, and passes the others
		mw := checker.Require(entitlements.WhatsAppChannel)
		run := func(u uuid.UUID, tenantKey string) int {
			code := 0
			w.must(platformdb.WithTenantSession(w.ctx, w.app, u, false, func(c context.Context) error {
				tc, err := tenancydomain.NewTenantContext(w.tenant[tenantKey], u, tenancydomain.AccessSourceDirect)
				if err != nil {
					return err
				}
				req := httptest.NewRequest("POST", "/x", nil).WithContext(tenancydomain.WithTenantContext(c, tc))
				rec := httptest.NewRecorder()
				mw(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(204) })).ServeHTTP(rec, req)
				code = rec.Code
				return nil
			}))
			return code
		}
		if run(memberA, "A") != 403 || run(member, "B") != 204 {
			t.Error("Require: the switched-off company must get 403 and the other 204")
		}
		// a member of the company can READ the switches but cannot WRITE them
		err := platformdb.WithTenantSession(w.ctx, w.app, memberA, false, func(c context.Context) error {
			tag, e := platformdb.QuerierFromContext(c, w.app).Exec(c, `UPDATE tenant_entitlements SET enabled = true WHERE tenant_id = $1`, w.tenant["A"])
			if e == nil && tag.RowsAffected() != 0 {
				return errors.New("a company member re-enabled their own capability")
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
		// ...nor insert a decision of their own, and a member of another company cannot even read this company's switches
		// (a refused statement aborts the transaction, so the refusal itself is the expected error)
		if err := platformdb.WithTenantSession(w.ctx, w.app, memberA, false, func(c context.Context) error {
			_, e := platformdb.QuerierFromContext(c, w.app).Exec(c, `INSERT INTO tenant_entitlements (tenant_id, capability, enabled) VALUES ($1, 'outbound_attachments', false)`, w.tenant["A"])
			return e
		}); err == nil {
			t.Error("a company member inserted an entitlement decision")
		}
		if got := w.n(member, `SELECT count(*) FROM tenant_entitlements WHERE tenant_id = $1`, w.tenant["A"]); got != 0 {
			t.Errorf("a member of company B reads %d of company A's switches", got)
		}
		if got := w.n(memberA, `SELECT count(*) FROM tenant_entitlements WHERE tenant_id = $1`, w.tenant["A"]); got == 0 {
			t.Error("a member must be able to read their own company's switches")
		}
		// and it is reversible
		api.call("PATCH", base+w.tenant["A"].String(), op, map[string]any{"capabilities": map[string]bool{entitlements.WhatsAppChannel: true}}, nil)
		if !enabled(memberA, "A", entitlements.WhatsAppChannel) {
			t.Error("re-enabling did not work")
		}
	})
}

// Defence in depth: the HTTP layer already refuses a caller who is not an operator-admin of the hub, but the service
// must refuse on its own as well, because it runs in a system session where nothing else would stop it.
func TestHubAdmin_TheServiceRefusesOnItsOwn(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op", true, true)
	agentOperator := w.operator("agentop", false, true)
	adminOnly := w.operator("adminonly", true, false)
	outsider := w.user("outsider")
	svc := companies.New(w.app)
	before := w.count(`SELECT count(*) FROM tenants`)
	for name, u := range map[string]uuid.UUID{"agent operator": agentOperator, "hub admin without operator": adminOnly, "outsider": outsider, "nobody": uuid.Nil} {
		if _, err := svc.List(w.ctx, u, w.hub); !errors.Is(err, companies.ErrForbidden) {
			t.Errorf("%s: List: %v", name, err)
		}
		if _, _, err := svc.Create(w.ctx, u, w.hub, companies.CreateInput{LegalName: "Intruder Ltda"}, "service-key-0001"); !errors.Is(err, companies.ErrForbidden) {
			t.Errorf("%s: Create: %v", name, err)
		}
		if _, err := svc.Update(w.ctx, u, w.hub, w.tenant["A"], companies.UpdateInput{Status: "suspended"}); !errors.Is(err, companies.ErrForbidden) {
			t.Errorf("%s: Update: %v", name, err)
		}
	}
	if w.count(`SELECT count(*) FROM tenants`) != before || w.count(`SELECT count(*) FROM tenants WHERE id = $1 AND status = 'active'`, w.tenant["A"]) != 1 {
		t.Error("the service changed something for a caller it should have refused")
	}
	// the right caller, but a company that is not the hub's
	d := uuid.New()
	w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, 'Not ours', 'active')`, d)
	if _, err := svc.Update(w.ctx, op, w.hub, d, companies.UpdateInput{Status: "suspended"}); !errors.Is(err, companies.ErrForbidden) {
		t.Errorf("a company outside the hub: %v", err)
	}
	if _, _, err := svc.Create(w.ctx, op, w.hub, companies.CreateInput{LegalName: "A"}, "service-key-0002"); !errors.Is(err, companies.ErrInvalid) {
		t.Errorf("invalid name: %v", err)
	}
	if _, _, err := svc.Create(w.ctx, op, w.hub, companies.CreateInput{LegalName: "Valid Name"}, "short"); !errors.Is(err, companies.ErrInvalid) {
		t.Errorf("short key: %v", err)
	}
}
