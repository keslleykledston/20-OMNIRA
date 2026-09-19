package adapters_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

type fakeSessions struct {
	mu      sync.Mutex
	status  ports.SessionStatus
	err     error
	calls   []string
	baseURL string
}

func (f *fakeSessions) rec(c string) { f.calls = append(f.calls, c) }
func (f *fakeSessions) Status(context.Context, domain.ChannelConnection) (ports.SessionStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.err
}
func (f *fakeSessions) Create(_ context.Context, _ domain.ChannelConnection, base string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("create")
	f.baseURL = base
	f.status = ports.SessionStopped
	return f.err
}
func (f *fakeSessions) Start(context.Context, domain.ChannelConnection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("start")
	f.status = ports.SessionNeedsQR
	return f.err
}
func (f *fakeSessions) Stop(context.Context, domain.ChannelConnection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("stop")
	f.status = ports.SessionStopped
	return f.err
}
func (f *fakeSessions) QR(context.Context, domain.ChannelConnection) (ports.QRImage, error) {
	return ports.QRImage{MIMEType: "image/png", Data: "QRDATA"}, nil
}
func (f *fakeSessions) Account(context.Context, domain.ChannelConnection) (string, error) {
	return "5511988887777", nil
}
func (f *fakeSessions) set(s ports.SessionStatus, err error) {
	f.mu.Lock()
	f.status, f.err = s, err
	f.mu.Unlock()
}
func (f *fakeSessions) called() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

type connEnv struct {
	t                                            *testing.T
	seed, app                                    *pgxpool.Pool
	mux                                          *http.ServeMux
	fake                                         *fakeSessions
	store                                        ports.CredentialStore
	tenantA, tenantB                             uuid.UUID
	adminA, adminB, supervisorA, agentA, revoked uuid.UUID
}

func newConnEnv(t *testing.T, publicURL string) *connEnv {
	t.Helper()
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	e := &connEnv{t: t, seed: seed, app: app, fake: &fakeSessions{status: ports.SessionMissing}, tenantA: uuid.New(), tenantB: uuid.New()}
	users := []*uuid.UUID{&e.adminA, &e.adminB, &e.supervisorA, &e.agentA, &e.revoked}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, u := range users {
		*u = uuid.New()
		exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, *u, *u, u.String()+"@invalid")
	}
	for _, tn := range []uuid.UUID{e.tenantA, e.tenantB} {
		exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, e.tenantA, e.tenantB)
		for _, u := range users {
			_, _ = seed.Exec(bg, `DELETE FROM users WHERE id=$1`, *u)
		}
		seed.Close()
		app.Close()
	})
	role := func(key string) (id uuid.UUID) {
		if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return
	}
	member := func(tn, u uuid.UUID, r, status string) {
		exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4)`, tn, u, role(r), status)
	}
	member(e.tenantA, e.adminA, "tenant_admin", "active")
	member(e.tenantA, e.supervisorA, "tenant_supervisor", "active")
	member(e.tenantA, e.agentA, "tenant_agent", "active")
	member(e.tenantA, e.revoked, "tenant_admin", "revoked")
	member(e.tenantB, e.adminB, "tenant_admin", "active")

	cipher, err := channelcrypto.NewAESGCM([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	e.store = adapters.NewPostgresCredentialStore(app, cipher)
	svc := application.NewWahaConnectionService(adapters.NewPostgresChannelConnectionRepository(app), e.store, e.fake,
		adapters.NewPostgresPermissionChecker(app), adapters.NewChannelAuditRecorder(auditadapters.NewPostgresAuditEventRepository(app)), publicURL)
	h := adapters.NewConnectionHandler(svc)
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	mw := tenancyadapters.AuthorizationMiddleware(app, authz)
	base := "/api/v1/tenants/{tenant_id}/channels/waha/connections"
	e.mux = http.NewServeMux()
	e.mux.Handle("POST "+base, mw(http.HandlerFunc(h.Create)))
	e.mux.Handle("GET "+base, mw(http.HandlerFunc(h.List)))
	e.mux.Handle("GET "+base+"/{connection_id}", mw(http.HandlerFunc(h.Get)))
	e.mux.Handle("POST "+base+"/{connection_id}/session/start", mw(http.HandlerFunc(h.StartSession)))
	e.mux.Handle("POST "+base+"/{connection_id}/session/stop", mw(http.HandlerFunc(h.StopSession)))
	e.mux.Handle("GET "+base+"/{connection_id}/qr", mw(http.HandlerFunc(h.QR)))
	return e
}

type cres struct {
	code int
	body string
	m    map[string]any
}

func (e *connEnv) do(user, tenant uuid.UUID, method, path, body string) cres {
	req := httptest.NewRequest(method, "/api/v1/tenants/"+tenant.String()+"/channels/waha/connections"+path, strings.NewReader(body))
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	r := cres{code: rec.Code, body: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &r.m)
	return r
}

func (e *connEnv) count(sql string, args ...any) (n int) {
	if err := e.seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return
}

func code(t *testing.T, r cres, want int, msg string) {
	t.Helper()
	if r.code != want {
		t.Fatalf("%s: code=%d body=%q want %d", msg, r.code, r.body, want)
	}
}

func TestWahaConnectionLifecycle(t *testing.T) {
	e := newConnEnv(t, "https://omnira.example.com/")
	code(t, e.do(e.adminA, e.tenantA, "POST", "", `{}`), 422, "create without risk ack")
	code(t, e.do(e.adminA, e.tenantA, "POST", "", `{"risk_acknowledged":false}`), 422, "create with false ack")
	r := e.do(e.adminA, e.tenantA, "POST", "", `{"risk_acknowledged":true,"tenant_id":"`+e.tenantB.String()+`"}`)
	code(t, r, 201, "create")
	id := r.m["id"].(string)
	if r.m["provider"] != "waha" || r.m["provider_kind"] != "unofficial" || r.m["status"] != "pending" || r.m["risk_acknowledged_at"] == nil {
		t.Fatalf("view: %v", r.m)
	}
	for _, leaked := range []string{"secret_ref", "hmac", "webhook_hmac_key"} {
		if strings.Contains(r.body, leaked) {
			t.Fatalf("response leaks %q: %s", leaked, r.body)
		}
	}
	// Persistence: tenant from the token (not the forged body), risk ack by the actor, encrypted key.
	if n := e.count(`SELECT count(*) FROM channel_connections WHERE id=$1 AND tenant_id=$2 AND provider='waha' AND provider_kind='unofficial' AND status='pending' AND risk_acknowledged_by=$3 AND provider_session_ref='omnira_'||id::text AND secret_ref IS NOT NULL`, id, e.tenantA, e.adminA); n != 1 {
		t.Fatalf("connection row mismatch (%d)", n)
	}
	var secretRef string
	if err := e.seed.QueryRow(context.Background(), `SELECT secret_ref::text FROM channel_connections WHERE id=$1`, id).Scan(&secretRef); err != nil {
		t.Fatal(err)
	}
	var cipherBytes []byte
	if err := e.seed.QueryRow(context.Background(), `SELECT ciphertext FROM channel_credentials WHERE id=$1`, secretRef).Scan(&cipherBytes); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipherBytes), "webhook_hmac_key") {
		t.Fatal("credential stored in plaintext")
	}
	err := platformdb.WithTenantSession(context.Background(), e.app, e.adminA, false, func(sc context.Context) error {
		cred, err := e.store.Resolve(sc, secretRef)
		if err != nil {
			return err
		}
		if len(cred.Fields["webhook_hmac_key"]) != 64 {
			t.Fatalf("hmac key not generated (len=%d)", len(cred.Fields["webhook_hmac_key"]))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='channel.connection_created' AND resource_id=$2 AND actor_id=$3`, e.tenantA, id, e.adminA); n != 1 {
		t.Fatalf("creation audit=%d", n)
	}

	// Start: session missing -> create with this connection's public base + start; needs QR.
	r = e.do(e.adminA, e.tenantA, "POST", "/"+id+"/session/start", "")
	code(t, r, 200, "start")
	if e.fake.called() != "create,start" || e.fake.baseURL != "https://omnira.example.com" {
		t.Fatalf("calls=%s base=%q", e.fake.called(), e.fake.baseURL)
	}
	if r.m["status"] != "pending" || r.m["session_status"] != "needs_qr" {
		t.Fatalf("after start: %v", r.m)
	}
	r = e.do(e.adminA, e.tenantA, "GET", "/"+id+"/qr", "")
	code(t, r, 200, "qr")
	if r.m["data"] != "QRDATA" {
		t.Fatalf("qr: %v", r.m)
	}
	// Paired: status refresh activates the connection and records the account.
	e.fake.set(ports.SessionWorking, nil)
	r = e.do(e.adminA, e.tenantA, "GET", "/"+id, "")
	code(t, r, 200, "get working")
	if r.m["status"] != "active" || r.m["external_account_id"] != "5511988887777" {
		t.Fatalf("working: %v", r.m)
	}
	if n := e.count(`SELECT count(*) FROM channel_connections WHERE id=$1 AND status='active' AND external_account_id='5511988887777'`, id); n != 1 {
		t.Fatal("status/account not persisted")
	}
	code(t, e.do(e.adminA, e.tenantA, "GET", "/"+id+"/qr", ""), 409, "qr when working")
	// Idempotent start while working: nothing re-created/started.
	before := e.fake.called()
	code(t, e.do(e.adminA, e.tenantA, "POST", "/"+id+"/session/start", ""), 200, "start again")
	if e.fake.called() != before {
		t.Fatalf("idempotent start touched provider: %s -> %s", before, e.fake.called())
	}
	// Stop.
	r = e.do(e.adminA, e.tenantA, "POST", "/"+id+"/session/stop", "")
	code(t, r, 200, "stop")
	if r.m["status"] != "disconnected" {
		t.Fatalf("stop: %v", r.m)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action IN ('channel.session_started','channel.session_stopped')`, id); n != 3 {
		t.Fatalf("session audits=%d, want 3", n)
	}
	r = e.do(e.adminA, e.tenantA, "GET", "", "")
	code(t, r, 200, "list")
	if items, _ := r.m["items"].([]any); len(items) != 1 {
		t.Fatalf("list: %s", r.body)
	}
}

func TestWahaConnectionRBACAndIsolation(t *testing.T) {
	e := newConnEnv(t, "https://omnira.example.com")
	id := e.do(e.adminA, e.tenantA, "POST", "", `{"risk_acknowledged":true}`).m["id"].(string)
	idB := e.do(e.adminB, e.tenantB, "POST", "", `{"risk_acknowledged":true}`).m["id"].(string)

	// Only channel.manage (tenant_admin) may operate connections.
	for name, u := range map[string]uuid.UUID{"supervisor": e.supervisorA, "agent": e.agentA} {
		for _, c := range []struct{ m, p string }{{"POST", ""}, {"GET", ""}, {"GET", "/" + id}, {"POST", "/" + id + "/session/start"}, {"POST", "/" + id + "/session/stop"}, {"GET", "/" + id + "/qr"}} {
			code(t, e.do(u, e.tenantA, c.m, c.p, `{"risk_acknowledged":true}`), 403, name+" "+c.m+" "+c.p)
		}
	}
	if n := e.count(`SELECT count(*) FROM channel_connections WHERE tenant_id=$1`, e.tenantA); n != 1 {
		t.Fatalf("forbidden create still inserted rows: %d", n)
	}
	// Revoked membership / foreign tenant path: tenant invisible.
	code(t, e.do(e.revoked, e.tenantA, "GET", "/"+id, ""), 404, "revoked admin")
	code(t, e.do(e.adminA, e.tenantB, "GET", "/"+idB, ""), 404, "A admin on B path")
	// Knowing another tenant's connection UUID is useless (no enumeration oracle).
	a := e.do(e.adminA, e.tenantA, "GET", "/"+idB, "")
	b := e.do(e.adminA, e.tenantA, "GET", "/"+uuid.NewString(), "")
	if a.code != 404 || b.code != 404 || a.body != b.body {
		t.Fatalf("oracle: %d %q vs %d %q", a.code, a.body, b.code, b.body)
	}
	for _, c := range []struct{ m, p string }{{"POST", "/" + idB + "/session/start"}, {"POST", "/" + idB + "/session/stop"}, {"GET", "/" + idB + "/qr"}} {
		code(t, e.do(e.adminA, e.tenantA, c.m, c.p, ""), 404, "cross-tenant "+c.m+" "+c.p)
	}
	if e.fake.called() != "" {
		t.Fatalf("cross-tenant/forbidden calls reached the provider: %s", e.fake.called())
	}
	// Lists never include the other tenant.
	r := e.do(e.adminA, e.tenantA, "GET", "", "")
	if items, _ := r.m["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Fatalf("list leaks: %s", r.body)
	}
}

func TestWahaConnectionProviderErrorsAndConfig(t *testing.T) {
	e := newConnEnv(t, "")
	id := e.do(e.adminA, e.tenantA, "POST", "", `{"risk_acknowledged":true}`).m["id"].(string)
	// Missing public URL: cannot register the webhook, provider untouched.
	code(t, e.do(e.adminA, e.tenantA, "POST", "/"+id+"/session/start", ""), 503, "no public url")
	if e.fake.called() != "" {
		t.Fatal("provider called without a webhook URL")
	}
	e2 := newConnEnv(t, "https://x.example")
	id2 := e2.do(e2.adminA, e2.tenantA, "POST", "", `{"risk_acknowledged":true}`).m["id"].(string)
	e2.fake.set(ports.SessionMissing, ports.ErrTransient)
	code(t, e2.do(e2.adminA, e2.tenantA, "POST", "/"+id2+"/session/start", ""), 502, "transient provider error")
	e2.fake.set(ports.SessionMissing, ports.ErrAuthentication)
	code(t, e2.do(e2.adminA, e2.tenantA, "GET", "/"+id2, ""), 502, "provider auth error")
}
