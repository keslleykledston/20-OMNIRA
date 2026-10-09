package adapters_test

// ADR-0038 phase 3 on a real PostgreSQL: a Hub person manages the channels / integrations of an instance through the REAL
// middleware, the REAL channel services and the caller's own RLS session (never a system session). What must hold:
//   - only a hub admin or a grant with can_manage, and only for the scopes the CONTRACT delegates (channels vs integrations);
//   - everything else is one uniform 404 (or 403 for a scope the contract does not delegate), never another company's data;
//   - the company's capability switch still blocks a NEW connection for a Hub manager (the entitlement row must stay readable);
//   - the database itself refuses what the application would never ask (policies of migration 103);
//   - secrets never come back in a response or an audit trail, and the audit says who acted and through which Hub.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	channelapplication "github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/entitlements"
	"github.com/omnira/omnira/internal/hub/access"
	"github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/hub/companies"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	toolconnectors "github.com/omnira/omnira/internal/tool/connectors"
)

type fakeSessions struct{}

func (fakeSessions) Status(context.Context, domain.ChannelConnection) (ports.SessionStatus, error) {
	return ports.SessionMissing, nil
}
func (fakeSessions) Create(context.Context, domain.ChannelConnection, string) error { return nil }
func (fakeSessions) Start(context.Context, domain.ChannelConnection) error          { return nil }
func (fakeSessions) Stop(context.Context, domain.ChannelConnection) error           { return nil }
func (fakeSessions) QR(context.Context, domain.ChannelConnection) (ports.QRImage, error) {
	return ports.QRImage{}, nil
}
func (fakeSessions) Account(context.Context, domain.ChannelConnection) (string, error) {
	return "", nil
}

type fakeProbe struct{}

func (fakeProbe) Probe(context.Context, map[string]string) error { return nil }

type manageAPI struct {
	w   *world
	srv *httptest.Server
}

func newManageAPI(t *testing.T, w *world) *manageAPI {
	t.Helper()
	cipher, err := channelcrypto.NewAESGCM([]byte("0123456789abcdef0123456789abcdef"))
	w.must(err)
	creds := channeladapters.NewPostgresCredentialStore(w.app, cipher)
	conns := channeladapters.NewPostgresChannelConnectionRepository(w.app)
	perms := channeladapters.NewPostgresPermissionChecker(w.app)
	audit := channeladapters.NewChannelAuditRecorder(auditadapters.NewPostgresAuditEventRepository(w.app))

	registry := channelapplication.NewMapProviderRegistry()
	mgmt := channelapplication.NewConnectionManagementService(registry, perms).WithEntitlements(entitlements.NewChecker(w.app).Gate)
	w.must(registry.RegisterDescriptor(waha.Descriptor(true, ""), nil))
	mgmt.Register("waha", channelapplication.NewWahaConnectionService(conns, creds, fakeSessions{}, perms, audit, "https://omnira.example"))
	crm := toolconnectors.K3GCRMDescriptor(true, "")
	w.must(registry.RegisterDescriptor(crm, nil))
	mgmt.Register(crm.ID, channelapplication.NewERPConnectionService(crm, conns, creds, perms, audit, fakeProbe{}))
	h := channeladapters.NewManagementHandler(mgmt)

	shim := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if raw := r.Header.Get("X-Test-User"); raw != "" {
				id, err := uuid.Parse(raw)
				if err != nil {
					t.Errorf("bad test user %q", raw)
				}
				r = r.WithContext(contextWithPrincipal(r, id))
			}
			next.ServeHTTP(rw, r)
		})
	}
	manage := adapters.ManageSession(w.app)
	wrap := func(fn http.HandlerFunc) http.Handler { return shim(manage(fn)) }
	base := "/api/v1/hubs/{hub_id}/instances/{tenant_id}/channels/connections"
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hubs/{hub_id}/instances/{tenant_id}/channels/providers", wrap(h.Providers))
	mux.Handle("POST "+base, wrap(h.Create))
	mux.Handle("GET "+base, wrap(h.List))
	mux.Handle("GET "+base+"/{connection_id}", wrap(h.Get))
	mux.Handle("POST "+base+"/{connection_id}/session/start", wrap(h.StartSession))
	hh := adapters.NewHTTPHandler(w.app).WithAdminAPI(true)
	session := tenancyadapters.UserSessionMiddleware(w.app)
	mux.Handle("GET /api/v1/hubs", shim(session(http.HandlerFunc(hh.ListMyHubs))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/managed", shim(session(http.HandlerFunc(hh.ListManaged))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &manageAPI{w: w, srv: srv}
}

func (a *manageAPI) call(method string, user uuid.UUID, tenantKey, suffix string, body any) (int, string) {
	a.w.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		a.w.must(err)
		rd = bytes.NewReader(raw)
	}
	url := a.srv.URL + "/api/v1/hubs/" + a.w.hub.String() + "/instances/" + a.w.tenant[tenantKey].String() + "/channels/" + suffix
	req, err := http.NewRequest(method, url, rd)
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

func (a *manageAPI) raw(method, path string, user uuid.UUID) (int, string) {
	a.w.t.Helper()
	req, err := http.NewRequest(method, a.srv.URL+path, nil)
	a.w.must(err)
	req.Header.Set("X-Test-User", user.String())
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (w *world) scopes(tenantKey string, scopes ...string) {
	if scopes == nil {
		scopes = []string{}
	}
	w.exec(`UPDATE hub_tenant_service_contracts SET management_scopes = $2 WHERE id = $1`, w.contract[tenantKey], scopes)
}

func (w *world) canManage(grant uuid.UUID) {
	w.exec(`UPDATE effective_access_grants SET can_manage = true WHERE id = $1`, grant)
}

var (
	createWAHA = map[string]any{"provider": "waha", "risk_acknowledged": true}
	createCRM  = map[string]any{"provider": "k3g_crm", "inputs": map[string]string{"base_url": "https://crm.example.com", "token": "super-secret-token-123"}}
)

func TestHubManagerThroughTheRealStack(t *testing.T) {
	w := newWorld(t)
	api := newManageAPI(t, w)
	admin := w.hubAdmin("admin")
	w.scopes("A", "channels", "integrations")

	// a hub admin manages the instance their hub's contract delegates, and the connection really belongs to THAT instance
	code, body := api.call("POST", admin, "A", "connections", createWAHA)
	if code != http.StatusCreated {
		t.Fatalf("hub admin creates a WhatsApp line: %d %s", code, body)
	}
	if n := w.count(`SELECT count(*) FROM channel_connections WHERE tenant_id = $1 AND provider = 'waha'`, w.tenant["A"]); n != 1 {
		t.Fatalf("one line expected in tenant A, got %d", n)
	}
	if code, body = api.call("GET", admin, "A", "connections", nil); code != http.StatusOK || !strings.Contains(body, `"waha"`) {
		t.Fatalf("list: %d %s", code, body)
	}

	// the integration (ERP/CRM) scope: created, and the secret is NOT in the response nor in the audit trail
	code, body = api.call("POST", admin, "A", "connections", createCRM)
	if code != http.StatusCreated {
		t.Fatalf("hub admin creates a CRM connection: %d %s", code, body)
	}
	if strings.Contains(body, "super-secret-token-123") {
		t.Fatal("the secret came back in the response")
	}
	_, list := api.call("GET", admin, "A", "connections", nil)
	if strings.Contains(list, "super-secret-token-123") || strings.Contains(list, "webhook_hmac_key") {
		t.Fatal("a secret leaked into the list")
	}
	var meta string
	w.must(w.owner.QueryRow(w.ctx, `SELECT metadata::text FROM audit_events WHERE tenant_id=$1 AND action='channel.connection_created' AND actor_id=$2 ORDER BY created_at DESC LIMIT 1`,
		w.tenant["A"], admin).Scan(&meta))
	if !strings.Contains(meta, `"via": "hub"`) && !strings.Contains(meta, `"via":"hub"`) {
		t.Fatalf("the audit does not say it came through the hub: %s", meta)
	}
	if !strings.Contains(meta, w.hub.String()) || strings.Contains(meta, "super-secret-token-123") {
		t.Fatalf("the audit must name the hub and never carry the secret: %s", meta)
	}

	// another company of the same hub whose contract delegates NOTHING: one uniform 404, for every verb
	for _, call := range []struct{ m, s string }{{"GET", "connections"}, {"POST", "connections"}, {"GET", "providers"}} {
		if code, _ := api.call(call.m, admin, "B", call.s, createWAHA); code != http.StatusNotFound {
			t.Errorf("%s %s on an instance with no delegated scope = %d, want 404", call.m, call.s, code)
		}
	}
	if n := w.count(`SELECT count(*) FROM channel_connections WHERE tenant_id = $1`, w.tenant["B"]); n != 0 {
		t.Fatalf("a refused create must leave nothing behind, found %d", n)
	}
}

func TestHubManagerScopesAreSeparate(t *testing.T) {
	w := newWorld(t)
	api := newManageAPI(t, w)
	admin := w.hubAdmin("admin")

	w.scopes("A", "channels") // WhatsApp yes, ERP/CRM no
	if code, body := api.call("POST", admin, "A", "connections", createWAHA); code != http.StatusCreated {
		t.Fatalf("channels scope must allow a WhatsApp line: %d %s", code, body)
	}
	if code, body := api.call("POST", admin, "A", "connections", createCRM); code != http.StatusForbidden {
		t.Fatalf("channels scope must NOT reach an ERP/CRM connection: %d %s", code, body)
	}
	if n := w.count(`SELECT count(*) FROM channel_connections WHERE tenant_id=$1 AND channel='erp'`, w.tenant["A"]); n != 0 {
		t.Fatalf("a forbidden ERP create left %d row(s)", n)
	}

	w.scopes("A", "integrations") // now the other way round
	if code, _ := api.call("POST", admin, "A", "connections", createCRM); code != http.StatusCreated {
		t.Fatal("integrations scope must allow an ERP/CRM connection")
	}
	if code, _ := api.call("POST", admin, "A", "connections", createWAHA); code != http.StatusForbidden {
		t.Fatal("integrations scope must NOT reach a WhatsApp line")
	}
	// the list does not mix scopes either: the WhatsApp line created earlier is hidden by RLS from an integrations-only manager
	_, list := api.call("GET", admin, "A", "connections", nil)
	if strings.Contains(list, `"waha"`) {
		t.Fatalf("an integrations-only manager must not see WhatsApp lines: %s", list)
	}
}

func TestHubManagerNeedsTheRightGrant(t *testing.T) {
	w := newWorld(t)
	api := newManageAPI(t, w)
	w.scopes("A", "channels")

	agent := w.hubAgent("agent")
	g := w.grantReply(agent, "A") // can read and reply, but NOT manage
	if code, _ := api.call("GET", agent, "A", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("reply is not manage: %d", code)
	}
	w.canManage(g)
	if code, body := api.call("POST", agent, "A", "connections", createWAHA); code != http.StatusCreated {
		t.Fatalf("can_manage grant on a contract that delegates channels: %d %s", code, body)
	}
	// the same person has no grant on B: B is invisible, even though B's contract also delegates
	w.scopes("B", "channels")
	if code, _ := api.call("GET", agent, "B", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("a manager of A must not reach B: %d", code)
	}

	// expiry and revocation take effect on the very next request
	w.exec(`UPDATE effective_access_grants SET valid_until = now() - interval '1 second', valid_from = now() - interval '1 day' WHERE id = $1`, g)
	if code, _ := api.call("GET", agent, "A", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("an expired grant must stop managing: %d", code)
	}
	w.exec(`UPDATE effective_access_grants SET valid_until = NULL, status = 'revoked' WHERE id = $1`, g)
	if code, _ := api.call("GET", agent, "A", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("a revoked grant must stop managing: %d", code)
	}
	w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g)
	if code, _ := api.call("GET", agent, "A", "connections", nil); code != http.StatusOK {
		t.Fatalf("an active grant manages again: %d", code)
	}
	// an account that is no longer active stops managing at once, over HTTP too
	w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, agent)
	if code, _ := api.call("GET", agent, "A", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("an inactive account must not manage: %d", code)
	}
	w.exec(`UPDATE users SET status = 'active' WHERE id = $1`, agent)
	// the contract stops delegating: nothing is manageable any more
	w.scopes("A")
	if code, _ := api.call("GET", agent, "A", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("a contract that delegates nothing: %d", code)
	}
}

func TestHubManagerStopsWhenTheCompanyOrHubStops(t *testing.T) {
	w := newWorld(t)
	api := newManageAPI(t, w)
	admin := w.hubAdmin("admin")
	w.scopes("A", "channels")
	if code, _ := api.call("GET", admin, "A", "connections", nil); code != http.StatusOK {
		t.Fatalf("baseline: %d", code)
	}
	w.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, w.tenant["A"])
	if code, _ := api.call("GET", admin, "A", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("a suspended company is not managed through the hub: %d", code)
	}
	w.exec(`UPDATE tenants SET status = 'active' WHERE id = $1`, w.tenant["A"])
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = $1`, w.contract["A"])
	if code, _ := api.call("GET", admin, "A", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("a suspended contract: %d", code)
	}
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'active' WHERE id = $1`, w.contract["A"])
	w.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, w.hub)
	if code, _ := api.call("GET", admin, "A", "connections", nil); code != http.StatusNotFound {
		t.Fatalf("a suspended hub: %d", code)
	}
}

func TestHubManagerStillObeysTheCompanyCapabilitySwitch(t *testing.T) {
	w := newWorld(t)
	api := newManageAPI(t, w)
	admin := w.hubAdmin("admin")
	w.scopes("A", "channels", "integrations")
	// ADR-0038: the operator switched WhatsApp off for this company. A Hub manager must be blocked too: this only works
	// because the entitlement row stays READABLE to them (policy tenant_entitlements_hub_manage_read); otherwise "no row" means ON.
	w.exec(`INSERT INTO tenant_entitlements (tenant_id, capability, enabled) VALUES ($1, 'whatsapp_channel', false)`, w.tenant["A"])
	if code, body := api.call("POST", admin, "A", "connections", createWAHA); code != http.StatusForbidden {
		t.Fatalf("a switched-off capability must block a hub manager: %d %s", code, body)
	}
	if n := w.count(`SELECT count(*) FROM channel_connections WHERE tenant_id=$1`, w.tenant["A"]); n != 0 {
		t.Fatalf("blocked create left %d row(s)", n)
	}
	if code, _ := api.call("POST", admin, "A", "connections", createCRM); code != http.StatusCreated {
		t.Fatal("the other capability (ERP/CRM) is still on")
	}
}

func TestHubManagerCannotReachAnotherInstanceByConnectionID(t *testing.T) {
	w := newWorld(t)
	api := newManageAPI(t, w)
	admin := w.hubAdmin("admin")
	w.scopes("A", "channels")
	w.scopes("B", "channels")
	_, body := api.call("POST", admin, "B", "connections", createWAHA)
	var created struct {
		ID string `json:"id"`
	}
	w.must(json.Unmarshal([]byte(body), &created))
	// B's line, asked for through A's path: not found, not a leak and not a cross-tenant operation
	for _, suffix := range []string{"connections/" + created.ID} {
		if code, _ := api.call("GET", admin, "A", suffix, nil); code != http.StatusNotFound {
			t.Errorf("A's path must not resolve B's connection (%s): %d", suffix, code)
		}
	}
	if code, _ := api.call("POST", admin, "A", "connections/"+created.ID+"/session/start", nil); code != http.StatusNotFound {
		t.Errorf("A's path must not start B's session: %d", code)
	}
}

// The database is the second barrier: asked directly (as the person, never as system), it admits exactly what the policies say.
func TestHubManagerRowLevelSecurityOnTheChannelTables(t *testing.T) {
	w := newWorld(t)
	manager := w.hubAgent("manager")
	g := w.grant(manager, "A")
	w.canManage(g)
	reader := w.hubAgent("reader")
	w.grantReply(reader, "A")
	w.scopes("A", "channels")
	w.scopes("B", "channels")
	line := func(tenant string) {
		id := uuid.New()
		w.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
		        VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, id, w.tenant[tenant], id.String())
		w.exec(`INSERT INTO channel_credentials(tenant_id,connection_id,ciphertext) VALUES($1,$2,'\x00')`, w.tenant[tenant], id)
	}
	line("A")
	line("B")
	erp := uuid.New()
	w.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'erp','k3g_crm','official',$3,'active','[]')`, erp, w.tenant["A"], erp.String())

	if n := w.n(manager, `SELECT count(*) FROM channel_connections`); n != 1 {
		t.Errorf("a manager of A with scope channels must see ONLY A's WhatsApp line (not B's, not A's ERP): saw %d", n)
	}
	if n := w.n(manager, `SELECT count(*) FROM channel_credentials`); n != 1 {
		t.Errorf("credentials follow the connection: saw %d", n)
	}
	if n := w.n(reader, `SELECT count(*) FROM channel_connections`); n != 0 {
		t.Errorf("a grant that may only reply must see no channel row: saw %d", n)
	}
	if n := w.n(manager, `SELECT count(*) FROM tenant_entitlements`); n < 0 {
		t.Error("unreachable")
	}
	// writes: into A yes (the scope matches), into B no, an ERP row no (scope integrations is not delegated)
	ins := func(user uuid.UUID, tenant, channel string) error {
		id := uuid.New()
		_, err := w.write(user, `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
		        VALUES($1,$2,$3,'waha','unofficial',$4,'pending','[]')`, id, w.tenant[tenant], channel, id.String())
		return err
	}
	if err := ins(manager, "A", "whatsapp"); err != nil {
		t.Errorf("insert into A: %v", err)
	}
	if err := ins(manager, "B", "whatsapp"); err == nil {
		t.Error("insert into B must be refused by RLS")
	}
	if err := ins(manager, "A", "erp"); err == nil {
		t.Error("an ERP row needs the integrations scope")
	}
	if err := ins(reader, "A", "whatsapp"); err == nil {
		t.Error("a reply-only grant must not insert")
	}
	// credentials follow their connection: not into another instance's connection, not into one of a scope that is not delegated
	var bLine uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM channel_connections WHERE tenant_id = $1 AND channel = 'whatsapp' LIMIT 1`, w.tenant["B"]).Scan(&bLine))
	w.exec(`DELETE FROM channel_credentials WHERE connection_id = $1`, bLine) // so only RLS (not the unique key) can refuse the next insert
	if _, err := w.write(manager, `INSERT INTO channel_credentials(tenant_id,connection_id,ciphertext) VALUES($1,$2,'\x01')`, w.tenant["B"], bLine); err == nil {
		t.Error("a credential for another instance's connection must be refused by RLS")
	}
	if _, err := w.write(manager, `INSERT INTO channel_credentials(tenant_id,connection_id,ciphertext) VALUES($1,$2,'\x01')`, w.tenant["A"], erp); err == nil {
		t.Error("a credential for an ERP connection needs the integrations scope")
	}
	// an update may not MOVE a row out of what the manager is allowed to manage (another instance, another scope)
	if _, err := w.write(manager, `UPDATE channel_connections SET tenant_id = $1 WHERE tenant_id = $2 AND channel = 'whatsapp'`, w.tenant["B"], w.tenant["A"]); err == nil {
		t.Error("moving a connection into another instance must be refused by RLS")
	}
	if _, err := w.write(manager, `UPDATE channel_connections SET channel = 'erp' WHERE tenant_id = $1 AND channel = 'whatsapp'`, w.tenant["A"]); err == nil {
		t.Error("turning a WhatsApp line into an ERP connection needs the integrations scope")
	}
	if n, _ := w.write(manager, `UPDATE channel_connections SET status='revoked' WHERE tenant_id=$1`, w.tenant["B"]); n != 0 {
		t.Errorf("update in B touched %d row(s)", n)
	}
	if n, _ := w.write(manager, `DELETE FROM channel_connections WHERE tenant_id=$1`, w.tenant["B"]); n != 0 {
		t.Errorf("delete in B removed %d row(s)", n)
	}

	// the database itself (not the middleware, whose own queries are also filtered by RLS) stops at a grant that is no longer live
	for name, break_ := range map[string]string{
		"revoked":   `status = 'revoked'`,
		"suspended": `status = 'suspended'`,
		"expired":   `valid_until = now() - interval '1 second'`,
	} {
		w.exec(`UPDATE effective_access_grants SET `+break_+` WHERE id = $1`, g)
		if n := w.n(manager, `SELECT count(*) FROM channel_connections`); n != 0 {
			t.Errorf("a %s grant must manage nothing at the database level, saw %d row(s)", name, n)
		}
		if _, err := w.write(manager, `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
		        VALUES(gen_random_uuid(),$1,'whatsapp','waha','unofficial',gen_random_uuid()::text,'pending','[]')`, w.tenant["A"]); err == nil {
			t.Errorf("a %s grant must not insert", name)
		}
		w.exec(`UPDATE effective_access_grants SET status = 'active', valid_until = NULL WHERE id = $1`, g)
	}
	if n := w.n(manager, `SELECT count(*) FROM channel_connections`); n < 1 {
		t.Error("restored grant must manage again")
	}

	// an account that is not active manages nothing, whatever rows it still has (Codex review)
	w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, manager)
	if n := w.n(manager, `SELECT count(*) FROM channel_connections`); n != 0 {
		t.Errorf("an inactive account must manage nothing at the database level, saw %d row(s)", n)
	}
	w.exec(`UPDATE users SET status = 'active' WHERE id = $1`, manager)

	// and it stops at a company, a contract or a hub that is no longer live (again asked of the database directly)
	for name, step := range map[string][2]string{
		"suspended company":  {`UPDATE tenants SET status = 'suspended' WHERE id = $1`, `UPDATE tenants SET status = 'active' WHERE id = $1`},
		"suspended contract": {`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE tenant_id = $1`, `UPDATE hub_tenant_service_contracts SET status = 'active' WHERE tenant_id = $1`},
		"expired contract":   {`UPDATE hub_tenant_service_contracts SET valid_until = now() - interval '1 second' WHERE tenant_id = $1`, `UPDATE hub_tenant_service_contracts SET valid_until = NULL WHERE tenant_id = $1`},
	} {
		w.exec(step[0], w.tenant["A"])
		if n := w.n(manager, `SELECT count(*) FROM channel_connections`); n != 0 {
			t.Errorf("a %s must manage nothing at the database level, saw %d row(s)", name, n)
		}
		w.exec(step[1], w.tenant["A"])
	}
	w.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, w.hub)
	if n := w.n(manager, `SELECT count(*) FROM channel_connections`); n != 0 {
		t.Errorf("a suspended hub must manage nothing, saw %d row(s)", n)
	}
	w.exec(`UPDATE service_hubs SET status = 'active' WHERE id = $1`, w.hub)
	// the hub argument is honoured: the same person asked about ANOTHER hub's id is a plain no
	otherHub := uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Other')`, otherHub)
	var viaOther, viaOwn bool
	w.must(platformdb.WithTenantSession(w.ctx, w.app, manager, false, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, w.app)
		if err := q.QueryRow(c, `SELECT has_hub_manage_access($1, $2, 'channels', $3)`, w.tenant["A"], manager, otherHub).Scan(&viaOther); err != nil {
			return err
		}
		return q.QueryRow(c, `SELECT has_hub_manage_access($1, $2, 'channels', $3)`, w.tenant["A"], manager, w.hub).Scan(&viaOwn)
	}))
	if viaOther || !viaOwn {
		t.Errorf("hub argument not honoured: other=%v own=%v", viaOther, viaOwn)
	}

	// a manager of another hub is a stranger to A (the policy asks about the contract's hub and the person's membership in it)
	stranger := w.user("stranger")
	if n := w.n(stranger, `SELECT count(*) FROM channel_connections`); n != 0 {
		t.Errorf("a person with no hub saw %d row(s)", n)
	}
	// nobody can use the function to ask about somebody else
	var asked bool
	err := platformdb.WithTenantSession(w.ctx, w.app, stranger, false, func(c context.Context) error {
		return platformdb.QuerierFromContext(c, w.app).QueryRow(c, `SELECT has_hub_manage_access($1, $2, 'channels')`, w.tenant["A"], manager).Scan(&asked)
	})
	w.must(err)
	if asked {
		t.Error("has_hub_manage_access answered YES about another person")
	}
}

func TestOperatorDelegatesScopes_OnlyKnownOnesAndOnlyAnOperator(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op", true, true)
	adminOnly := w.operator("adminonly", true, false)
	svc := companies.New(w.app)
	set := func(who uuid.UUID, scopes ...string) error {
		if scopes == nil {
			scopes = []string{}
		}
		_, err := svc.Update(w.ctx, who, w.hub, w.tenant["A"], companies.UpdateInput{ManagementScopes: &scopes})
		return err
	}
	if err := set(op, "integrations", "channels", "channels"); err != nil {
		t.Fatal(err)
	}
	var got []string
	w.must(w.owner.QueryRow(w.ctx, `SELECT management_scopes FROM hub_tenant_service_contracts WHERE id = $1`, w.contract["A"]).Scan(&got))
	if len(got) != 2 || got[0] != "channels" || got[1] != "integrations" {
		t.Fatalf("scopes must be stored sorted and de-duplicated: %v", got)
	}
	if n := w.auditActor("platform.company.management_scopes_changed", op); n != 1 {
		t.Errorf("the change must be audited with the operator as actor, got %d", n)
	}
	for _, bad := range []string{"team", "queues", "settings", "everything", ""} {
		if err := set(op, bad); err == nil {
			t.Errorf("scope %q must be refused (reserved or unknown)", bad)
		}
	}
	if err := set(adminOnly, "channels"); err == nil {
		t.Error("a hub admin who is not a platform operator must not delegate management")
	}
	if err := set(op); err != nil { // [] withdraws every delegation
		t.Fatal(err)
	}
	w.must(w.owner.QueryRow(w.ctx, `SELECT management_scopes FROM hub_tenant_service_contracts WHERE id = $1`, w.contract["A"]).Scan(&got))
	if len(got) != 0 {
		t.Fatalf("withdrawn: %v", got)
	}
}

func TestManageSwitchNeedsALiveGrantAndIsNeverRevived(t *testing.T) {
	w := newWorld(t)
	admin := w.hubAdmin("admin")
	svc, err := access.New(w.app)
	w.must(err)
	agent := w.hubAgent("agent")
	flag := func() bool {
		var b bool
		w.must(w.owner.QueryRow(w.ctx, `SELECT can_manage FROM effective_access_grants WHERE hub_id=$1 AND user_id=$2 AND tenant_id=$3`, w.hub, agent, w.tenant["A"]).Scan(&b))
		return b
	}
	if err := svc.SetManage(w.ctx, admin, w.hub, agent, w.tenant["A"], true); err == nil {
		t.Fatal("management without any grant must be refused")
	}
	if err := svc.SetAccess(w.ctx, admin, w.hub, agent, w.tenant["A"], "read", nil); err != nil {
		t.Fatal(err)
	}
	if flag() {
		t.Fatal("a new grant must NOT carry management")
	}
	if err := svc.SetManage(w.ctx, admin, w.hub, agent, w.tenant["A"], true); err != nil {
		t.Fatal(err)
	}
	if !flag() {
		t.Fatal("the switch did not stick")
	}
	if err := svc.SetAccess(w.ctx, admin, w.hub, agent, w.tenant["A"], "reply", nil); err != nil || !flag() {
		t.Fatalf("changing the mode of a LIVE grant must keep the switch (err=%v flag=%v)", err, flag())
	}
	if err := svc.SetAccess(w.ctx, admin, w.hub, agent, w.tenant["A"], "none", nil); err != nil {
		t.Fatal(err)
	}
	if flag() {
		t.Fatal("revoking must clear the management switch")
	}
	if err := svc.SetManage(w.ctx, admin, w.hub, agent, w.tenant["A"], true); err == nil {
		t.Fatal("a revoked grant cannot be given management")
	}
	if err := svc.SetAccess(w.ctx, admin, w.hub, agent, w.tenant["A"], "read", nil); err != nil {
		t.Fatal(err)
	}
	if flag() {
		t.Fatal("renewing access must NEVER revive a delegation of management")
	}
	// a grant that is not live for any OTHER reason (here: suspended by hand) and still carries the old switch: renewing it must not revive it
	w.exec(`UPDATE effective_access_grants SET status = 'suspended', can_manage = true WHERE hub_id=$1 AND user_id=$2 AND tenant_id=$3`, w.hub, agent, w.tenant["A"])
	if err := svc.SetAccess(w.ctx, admin, w.hub, agent, w.tenant["A"], "read", nil); err != nil {
		t.Fatal(err)
	}
	if flag() {
		t.Fatal("renewing a suspended grant must come back WITHOUT management, even if the old row still said yes")
	}
	// the people who may touch the switch: only an admin of the hub
	if err := svc.SetManage(w.ctx, agent, w.hub, agent, w.tenant["A"], true); err == nil {
		t.Fatal("an agent must not switch management on for themselves")
	}
	_ = provisioning.RoleAgent
}

func TestManagedInstancesListAndFlagAreAskedOfTheDatabase(t *testing.T) {
	w := newWorld(t)
	api := newManageAPI(t, w)
	admin := w.hubAdmin("admin")
	manager := w.hubAgent("manager")
	w.canManage(w.grant(manager, "A"))
	reader := w.hubAgent("reader")
	w.grantReply(reader, "A")
	w.scopes("A", "channels", "integrations")
	w.scopes("B", "integrations")
	hubPath := "/api/v1/hubs/" + w.hub.String() + "/managed"

	_, body := api.raw("GET", hubPath, admin)
	if !strings.Contains(body, w.tenant["A"].String()) || !strings.Contains(body, w.tenant["B"].String()) {
		t.Fatalf("a hub admin manages every instance whose contract delegates something: %s", body)
	}
	_, body = api.raw("GET", hubPath, manager)
	if !strings.Contains(body, w.tenant["A"].String()) || strings.Contains(body, w.tenant["B"].String()) {
		t.Fatalf("a manager sees only the instance of their grant: %s", body)
	}
	_, body = api.raw("GET", hubPath, reader)
	if strings.Contains(body, w.tenant["A"].String()) {
		t.Fatalf("a reply-only grant manages nothing: %s", body)
	}
	for who, want := range map[string]struct {
		u  uuid.UUID
		is bool
	}{"admin": {admin, true}, "manager": {manager, true}, "reader": {reader, false}} {
		_, mine := api.raw("GET", "/api/v1/hubs", want.u)
		var out struct {
			Items []struct {
				Can bool `json:"can_manage_instances"`
			} `json:"items"`
		}
		w.must(json.Unmarshal([]byte(mine), &out))
		if len(out.Items) != 1 || out.Items[0].Can != want.is {
			t.Errorf("%s: can_manage_instances = %v, want %v (%s)", who, out.Items, want.is, mine)
		}
	}
}

// The hub in the path is a CLAIM: a company is managed only through the hub its own contract is with, by that hub's people.
func TestHubManagerCannotUseAnotherHubsPath(t *testing.T) {
	w := newWorld(t)
	api := newManageAPI(t, w)
	admin := w.hubAdmin("admin")
	w.scopes("A", "channels")

	otherHub := uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Other hub')`, otherHub)
	otherAdmin := w.user("otheradmin")
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, otherHub, otherAdmin, w.roleHubAdmin)
	// the other hub has its OWN contract with company B, which delegates channels; none with A
	otherContract := uuid.New()
	w.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from, management_scopes) VALUES ($1, $2, $3, now() - interval '1 day', ARRAY['channels'])`,
		otherContract, otherHub, w.tenant["B"])

	path := func(hub uuid.UUID, tenant string) string {
		return "/api/v1/hubs/" + hub.String() + "/instances/" + w.tenant[tenant].String() + "/channels/connections"
	}
	// the admin of the OTHER hub asks about company A (whose contract is with the first hub), through either hub's path
	for _, p := range []string{path(otherHub, "A"), path(w.hub, "A")} {
		if code, _ := api.raw("GET", p, otherAdmin); code != http.StatusNotFound {
			t.Errorf("another hub's admin on %s = %d, want 404", p, code)
		}
	}
	// the first hub's admin cannot borrow the other hub's path for company B, nor reach B through their own hub
	for _, p := range []string{path(otherHub, "B"), path(w.hub, "B")} {
		if code, _ := api.raw("GET", p, admin); code != http.StatusNotFound {
			t.Errorf("hub admin on %s = %d, want 404", p, code)
		}
	}
	// and the legitimate pairs still work
	if code, _ := api.raw("GET", path(w.hub, "A"), admin); code != http.StatusOK {
		t.Errorf("baseline admin/A: %d", code)
	}
	if code, _ := api.raw("GET", path(otherHub, "B"), otherAdmin); code != http.StatusOK {
		t.Errorf("baseline other admin/B: %d", code)
	}
}

// ADR-0038 + Codex review: while a management request is in progress EVERYTHING its authority depends on is held (company, hub, contract,
// the person's membership and account, and their grant): a suspension or a revocation waits for the request - including the call to the
// channel provider - instead of landing in the middle of it. A hold-based test: the handler parks inside the middleware while each change
// is attempted; every one must be blocked, and every one must go through once the request is released.
func TestHubManagerHoldsEverythingTheAuthorizationDependsOn(t *testing.T) {
	// A person who manages through a GRANT, and a hub ADMIN who manages through the role (no grant row at all): each is pinned by the rows
	// its own authority rests on, so each is proven separately (the grant's foreign key to the membership would otherwise hide a missing pin).
	for _, persona := range []string{"agent with a can_manage grant", "hub admin without any grant"} {
		persona := persona
		t.Run(persona, func(t *testing.T) {
			w := newWorld(t)
			var who, grant uuid.UUID
			if persona == "agent with a can_manage grant" {
				who = w.hubAgent("agent")
				grant = w.grant(who, "A")
				w.canManage(grant)
			} else {
				who = w.hubAdmin("admin")
			}
			w.scopes("A", "channels")
			holdAndChange(t, w, who, grant)
		})
	}
}

func holdAndChange(t *testing.T, w *world, who, grant uuid.UUID) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release) }) }

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hubs/{hub_id}/instances/{tenant_id}/probe", func() http.Handler {
		shim := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				id, _ := uuid.Parse(r.Header.Get("X-Test-User"))
				next.ServeHTTP(rw, r.WithContext(contextWithPrincipal(r, id)))
			})
		}
		return shim(adapters.ManageSession(w.app)(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			rw.WriteHeader(http.StatusNoContent)
		})))
	}())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// Cleanups run last-in first-out: registered AFTER srv.Close, this releases the parked request BEFORE the server waits for it,
	// so a failing assertion can never leave the request (and its transaction) parked, nor hang the test run.
	t.Cleanup(releaseOnce)

	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/hubs/"+w.hub.String()+"/instances/"+w.tenant["A"].String()+"/probe", nil)
		req.Header.Set("X-Test-User", who.String())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- -1
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never got past the middleware")
	}

	type change struct {
		what, sql string
		args      []any
	}
	changes := []change{
		{"suspending the company", `UPDATE tenants SET status = 'suspended' WHERE id = $1`, []any{w.tenant["A"]}},
		{"pausing the hub", `UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, []any{w.hub}},
		{"suspending the contract", `UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE tenant_id = $1`, []any{w.tenant["A"]}},
		{"removing the person from the hub", `DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, []any{w.hub, who}},
		{"changing the person's role in the hub", `UPDATE hub_memberships SET role_id = $3 WHERE hub_id = $1 AND user_id = $2`, []any{w.hub, who, w.roleHubAgent}},
		{"deactivating the account", `UPDATE users SET status = 'inactive' WHERE id = $1`, []any{who}},
	}
	if grant != uuid.Nil {
		changes = append(changes, change{"revoking the grant", `UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, []any{grant}})
	}
	for _, c := range changes {
		ctx, cancel := context.WithTimeout(w.ctx, 600*time.Millisecond)
		_, err := w.owner.Exec(ctx, c.sql, c.args...)
		cancel()
		if err == nil {
			t.Errorf("%s did not wait for the management request in progress", c.what)
		}
	}
	releaseOnce()
	if code := <-done; code != http.StatusNoContent {
		t.Fatalf("the request: %d", code)
	}
	for _, c := range changes { // now nothing holds them
		w.exec(c.sql, c.args...)
	}
}

// A `hub_manage` context answers to exactly TWO permissions (channel.manage -> scope channels, integration.manage -> scope integrations),
// only for the person it was built for. Every other tenant permission is a flat no, whatever the contract delegates.
func TestHubManageContextGetsOnlyTheTwoManagementPermissions(t *testing.T) {
	w := newWorld(t)
	admin := w.hubAdmin("admin")
	other := w.hubAdmin("other")
	w.scopes("A", "channels", "integrations")
	perms := channeladapters.NewPostgresPermissionChecker(w.app)
	tc, err := tenancydomain.NewHubManageTenantContext(w.tenant["A"], admin, w.hub, w.contract["A"], nil, "")
	w.must(err)
	ask := func(user uuid.UUID, permission string) bool {
		var ok bool
		w.must(platformdb.WithTenantSession(w.ctx, w.app, user, false, func(c context.Context) error {
			var err error
			ok, err = perms.HasPermission(tenancydomain.WithTenantContext(c, tc), user, permission)
			return err
		}))
		return ok
	}
	if !ask(admin, "channel.manage") || !ask(admin, "integration.manage") {
		t.Fatal("the two management permissions must be granted to the person the context was built for")
	}
	for _, p := range []string{"conversation.claim", "conversation.manage", "membership.manage", "group.read", "flow.manage", ""} {
		if ask(admin, p) {
			t.Errorf("permission %q must NEVER be granted through a hub management context", p)
		}
	}
	if ask(other, "channel.manage") {
		t.Error("the context was built for somebody else: another actor must not be answered YES")
	}
	// the scope really is the one asked: withdraw integrations and only channels remains
	w.scopes("A", "channels")
	if !ask(admin, "channel.manage") || ask(admin, "integration.manage") {
		t.Error("each permission must follow its own scope")
	}
}

// Which contexts may reach a channel management service at all. The Hub's READ/REPLY context (source `hub`) must stay out: it proves the
// person may answer conversations, not that they may configure the company's channels.
func TestHubManageAdmissionOnlyDirectOrHubManageContextsReachTheChannelServices(t *testing.T) {
	a, u, h, c, g := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	direct, _ := tenancydomain.NewTenantContext(a, u, tenancydomain.AccessSourceDirect)
	system, _ := tenancydomain.NewTenantContext(a, uuid.Nil, tenancydomain.AccessSourceSystem)
	hubRead, err := tenancydomain.NewHubTenantContext(a, u, h, c, g, "")
	if err != nil {
		t.Fatal(err)
	}
	hubManage, _ := tenancydomain.NewHubManageTenantContext(a, u, h, c, nil, "")
	noContract := &tenancydomain.TenantContext{TenantID: a, ActorID: u, Source: tenancydomain.AccessSourceHubManage, HubID: &h}
	noHub := &tenancydomain.TenantContext{TenantID: a, ActorID: u, Source: tenancydomain.AccessSourceHubManage, ServiceContractID: &c}
	for name, tc := range map[string]*tenancydomain.TenantContext{"direct": direct, "hub_manage": hubManage} {
		if !tc.MayManageAsTenant() {
			t.Errorf("%s must be admitted", name)
		}
	}
	for name, tc := range map[string]*tenancydomain.TenantContext{
		"hub read/reply": hubRead, "system": system, "hub_manage without a contract": noContract, "hub_manage without a hub": noHub, "nil": nil,
		"empty source": {TenantID: a, ActorID: u},
	} {
		if tc.MayManageAsTenant() {
			t.Errorf("%s must NOT reach the channel services", name)
		}
	}
}
