package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	"github.com/omnira/omnira/internal/conversationcontrol"
	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/domain"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/testhelpers"
)

// ADR-0046 H1: identity of an external application. Real PostgreSQL as the application role (omnira_app, FORCE RLS, signed session context).

type env struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool
	app  *pgxpool.Pool
	mux  *http.ServeMux
	ext  *http.ServeMux
	// addr, when set, is the remote address of every external call; otherwise each call comes from its own address, so the
	// failed-authentication throttle of one test case cannot leak into another
	addr string
	seq  int
	n    int // a counter for unique phone numbers and names in the fixtures
	// adminH is the administration handler mounted on mux (the webhook administration shares it)
	adminH  *adapters.AdminHandler
	session func(http.Handler) http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	e := &env{t: t, ctx: ctx, seed: seed, app: app}

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	tenantSession := tenancyadapters.AuthorizationMiddleware(app, authz)
	e.session = tenantSession
	admin := adapters.NewAdminHandler(app, auditadapters.NewPostgresAuditEventRepository(app)).WithDrainer(conversationcontrol.Service{}.DrainInstallation).WithScopeDrainer(conversationcontrol.Service{}.DrainOutOfScope)
	e.adminH = admin
	e.mux = http.NewServeMux()
	base := "/api/v1/tenants/{tenant_id}/integrations/apps"
	route := func(p string, h http.HandlerFunc) { e.mux.Handle(p, tenantSession(h)) }
	route("GET "+base, admin.List)
	route("POST "+base, admin.Create)
	route("GET "+base+"/{app_id}", admin.Get)
	route("PATCH "+base+"/{app_id}", admin.Update)
	route("POST "+base+"/{app_id}/suspend", admin.Transition(adapters.Suspend, auditdomain.ActionExternalAppSuspended))
	route("POST "+base+"/{app_id}/reactivate", admin.Transition(adapters.Reactivate, auditdomain.ActionExternalAppReactivated))
	route("POST "+base+"/{app_id}/revoke", admin.Transition(adapters.Revoke, auditdomain.ActionExternalAppRevoked))
	route("POST "+base+"/{app_id}/credentials", admin.IssueCredential)
	route("DELETE "+base+"/{app_id}/credentials/{credential_id}", admin.RevokeCredential)

	mw := adapters.NewMiddleware(adapters.NewPostgresLookup(app))
	e.ext = http.NewServeMux()
	e.ext.Handle("GET /api/v1/external/me", mw.Authenticated(adapters.NewMeHandler(app, adapters.Features{})))
	e.ext.Handle("POST /api/v1/external/needs-send", mw.Authenticated(adapters.RequireScope(domain.ScopeMessageSend, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))))
	return e
}

func (e *env) exec(q string, a ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(e.ctx, q, a...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) tenant() uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, id, "T-"+id.String())
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	return id
}

func (e *env) member(tenant uuid.UUID, roleKey string) uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u.String(), u.String()+"@invalid")
	var role uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, roleKey).Scan(&role); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, u, role)
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	return u
}

func (e *env) queue(tenant uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,$3,'manual')`, id, tenant, "q-"+id.String()[:8])
	return id
}

func (e *env) admin(user, tenant uuid.UUID, method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/v1/tenants/"+tenant.String()+"/integrations/apps"+path, rd)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

type created struct {
	Installation struct {
		ID     uuid.UUID `json:"id"`
		Name   string    `json:"name"`
		Status string    `json:"status"`
	} `json:"installation"`
	Credential struct {
		ID    uuid.UUID `json:"id"`
		KeyID string    `json:"key_id"`
		Token string    `json:"token"`
	} `json:"credential"`
}

func (e *env) install(admin, tenant uuid.UUID, name string, scopes ...string) created {
	e.t.Helper()
	code, body := e.admin(admin, tenant, http.MethodPost, "", map[string]any{"name": name, "scopes": scopes})
	if code != http.StatusCreated {
		e.t.Fatalf("create %s = %d %s", name, code, body)
	}
	var c created
	if err := json.Unmarshal(body, &c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) callExt(method, path, token string, hdr map[string]string) (int, []byte, http.Header) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	e.seq++
	req.RemoteAddr = e.addr
	if req.RemoteAddr == "" {
		req.RemoteAddr = "198.51.100." + strconv.Itoa(e.seq%250+1) + ":" + strconv.Itoa(4000+e.seq/250)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ext.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), rec.Header()
}

// ---------------------------------------------------------------------------------------------------------------------

func TestAdministrationIsPermissionedTenantScopedAndNeverReturnsTheSecretAgain(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.tenant(), e.tenant(), e.tenant()
	adminA, supA, agentA := e.member(a, "tenant_admin"), e.member(a, "tenant_supervisor"), e.member(a, "tenant_agent")
	adminB := e.member(b, "tenant_admin")
	_ = c
	foreignQueue := e.queue(b)

	// the administrator installs; the secret is in this response only
	app := e.install(adminA, a, "CRM Acme", "conversation.read", "message.read")
	if !strings.HasPrefix(app.Credential.Token, domain.TokenPrefix) || app.Credential.KeyID == "" {
		t.Fatalf("credential = %+v", app.Credential)
	}
	// listing and reading show the public record, never the token or any digest
	for _, path := range []string{"", "/" + app.Installation.ID.String()} {
		code, body := e.admin(adminA, a, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Fatalf("GET %q = %d %s", path, code, body)
		}
		s := string(body)
		secret := app.Credential.Token[strings.Index(app.Credential.Token, ".")+1:]
		if strings.Contains(s, secret) || strings.Contains(s, "secret_digest") || strings.Contains(s, "\"token\"") || strings.Contains(s, "tenant_id") {
			t.Fatalf("GET %q leaks: %s", path, s)
		}
		if !strings.Contains(s, app.Credential.KeyID) {
			t.Fatalf("GET %q lacks the key id: %s", path, s)
		}
	}
	// supervisor reads, cannot change; agent cannot even read
	if code, _ := e.admin(supA, a, http.MethodGet, "", nil); code != http.StatusOK {
		t.Errorf("supervisor list = %d", code)
	}
	if code, _ := e.admin(supA, a, http.MethodPost, "", map[string]any{"name": "x", "scopes": []string{"conversation.read"}}); code != http.StatusForbidden {
		t.Errorf("supervisor create = %d, want 403", code)
	}
	if code, _ := e.admin(agentA, a, http.MethodGet, "", nil); code != http.StatusForbidden {
		t.Errorf("agent list = %d, want 403", code)
	}
	// another company: sees nothing of A, cannot change A's, and A's id is a 404 (not a 403)
	if code, body := e.admin(adminB, b, http.MethodGet, "", nil); code != http.StatusOK || !strings.Contains(string(body), `"count":0`) {
		t.Errorf("B list = %d %s", code, body)
	}
	id := app.Installation.ID.String()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/" + id}, {http.MethodPatch, "/" + id}, {http.MethodPost, "/" + id + "/suspend"}, {http.MethodPost, "/" + id + "/revoke"},
		{http.MethodPost, "/" + id + "/credentials"}, {http.MethodDelete, "/" + id + "/credentials/" + app.Credential.ID.String()},
	} {
		var body any
		if c.method == http.MethodPatch {
			body = map[string]any{"name": "pwn"}
		}
		if code, resp := e.admin(adminB, b, c.method, c.path, body); code != http.StatusNotFound {
			t.Errorf("B %s %s = %d %s, want 404", c.method, c.path, code, resp)
		}
	}
	// B's administrator reaching A's path through A's tenant id: no membership there
	if code, _ := e.admin(adminB, a, http.MethodGet, "", nil); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("B admin on A's tenant path = %d", code)
	}
	// validation: unknown field (tenant_id), unknown scope, duplicate name, a queue of another company
	if code, _ := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "Y", "scopes": []string{"conversation.read"}, "tenant_id": b.String()}); code != http.StatusBadRequest {
		t.Errorf("create with tenant_id = %d, want 400", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "Y", "scopes": []string{"conversation.read", "root"}}); code != http.StatusBadRequest {
		t.Errorf("unknown scope = %d, want 400", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "crm acme", "scopes": []string{"conversation.read"}}); code != http.StatusConflict {
		t.Errorf("duplicate name (case-insensitive) = %d, want 409", code)
	}
	if code, body := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "Z", "scopes": []string{"conversation.read"}, "queue_ids": []string{foreignQueue.String()}}); code != http.StatusBadRequest {
		t.Errorf("allow-list with another company's queue = %d %s, want 400", code, body)
	}
	// the audit trail names the action and the key id, never a token
	var meta string
	if err := e.seed.QueryRow(e.ctx, `SELECT metadata::text FROM audit_events WHERE tenant_id=$1 AND action='external_app.created' ORDER BY created_at DESC LIMIT 1`, a).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(meta, "omn_ext_") || !strings.Contains(meta, app.Credential.KeyID) {
		t.Errorf("audit metadata = %s", meta)
	}
}

func TestCredentialRotationKeepsAtMostTwoLiveAndRevokingOneLeavesTheOther(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	adminA := e.member(a, "tenant_admin")
	app := e.install(adminA, a, "CRM", "conversation.read")
	id := app.Installation.ID.String()
	code, body := e.admin(adminA, a, http.MethodPost, "/"+id+"/credentials", map[string]any{"label": "next"})
	if code != http.StatusCreated {
		t.Fatalf("second credential = %d %s", code, body)
	}
	var next struct{ Token string }
	_ = json.Unmarshal(body, &next)
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+id+"/credentials", nil); code != http.StatusConflict {
		t.Fatalf("third live credential = %d, want 409", code)
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/me", app.Credential.Token, nil); code != http.StatusOK {
		t.Fatalf("old credential = %d", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodDelete, "/"+id+"/credentials/"+app.Credential.ID.String(), nil); code != http.StatusNoContent {
		t.Fatalf("revoke old = %d", code)
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/me", app.Credential.Token, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked credential = %d, want 401", code)
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/me", next.Token, nil); code != http.StatusOK {
		t.Fatalf("new credential = %d", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodDelete, "/"+id+"/credentials/"+uuid.NewString(), nil); code != http.StatusNotFound {
		t.Fatalf("unknown credential = %d, want 404", code)
	}
}

func TestExternalAuthenticationMatrix(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a, "tenant_admin"), e.member(b, "tenant_admin")
	appA := e.install(adminA, a, "CRM A", "conversation.read")
	appB := e.install(adminB, b, "CRM B", "conversation.read", "message.send")
	idA := appA.Installation.ID.String()

	me := func(token string, hdr map[string]string) (int, map[string]any) {
		code, body, _ := e.callExt(http.MethodGet, "/api/v1/external/me", token, hdr)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		return code, m
	}
	// each token resolves to ITS OWN tenant, and nothing else decides it
	code, m := me(appA.Credential.Token, nil)
	if code != http.StatusOK || m["tenant"].(map[string]any)["id"] != a.String() {
		t.Fatalf("A /me = %d %v", code, m)
	}
	if code, m = me(appB.Credential.Token, nil); code != http.StatusOK || m["tenant"].(map[string]any)["id"] != b.String() {
		t.Fatalf("B /me = %d %v", code, m)
	}
	// the tenant cannot be chosen: query, header, acting-as are refused even with a valid token
	for name, c := range map[string]struct {
		path string
		hdr  map[string]string
	}{
		"query tenant_id":  {"/api/v1/external/me?tenant_id=" + b.String(), nil},
		"X-Tenant-ID":      {"/api/v1/external/me", map[string]string{"X-Tenant-ID": b.String()}},
		"X-Omnira-Acting":  {"/api/v1/external/me", map[string]string{"X-Omnira-Acting-As": "hub:" + uuid.NewString()}},
		"query tenant_ID2": {"/api/v1/external/me?tenant_id=", nil},
	} {
		if code, _, _ := e.callExt(http.MethodGet, c.path, appA.Credential.Token, c.hdr); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	// bad tokens: all the same 401, with the challenge header and a problem document
	_, secret, _ := domain.ParseToken(appA.Credential.Token)
	flipped := strings.Replace(appA.Credential.Token, secret[:3], "AAA", 1)
	if secret[:3] == "AAA" {
		flipped = strings.Replace(appA.Credential.Token, secret[:3], "BBB", 1)
	}
	for name, tok := range map[string]string{"none": "", "garbage": "abc", "wrong secret": flipped, "unknown key": "omn_ext_" + strings.Repeat("z", 16) + "." + secret} {
		code, body, hdr := e.callExt(http.MethodGet, "/api/v1/external/me", tok, nil)
		if code != http.StatusUnauthorized || !strings.HasPrefix(hdr.Get("Content-Type"), "application/problem+json") || hdr.Get("WWW-Authenticate") == "" {
			t.Errorf("%s = %d %s %v", name, code, body, hdr)
		}
	}
	// lifecycle of the installation
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+idA+"/suspend", nil); code != http.StatusOK {
		t.Fatalf("suspend = %d", code)
	}
	if code, _ := me(appA.Credential.Token, nil); code != http.StatusUnauthorized {
		t.Errorf("suspended installation = %d, want 401", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+idA+"/reactivate", nil); code != http.StatusOK {
		t.Fatalf("reactivate = %d", code)
	}
	if code, _ := me(appA.Credential.Token, nil); code != http.StatusOK {
		t.Errorf("reactivated installation = %d, want 200", code)
	}
	// an expired credential (the owner pool bends time; the CHECK needs created_at before expires_at)
	e.exec(`UPDATE external_app_credentials SET created_at = now() - interval '2 days', expires_at = now() - interval '1 minute' WHERE id=$1`, appA.Credential.ID)
	if code, _ := me(appA.Credential.Token, nil); code != http.StatusUnauthorized {
		t.Errorf("expired credential = %d, want 401", code)
	}
	e.exec(`UPDATE external_app_credentials SET expires_at = now() + interval '1 day' WHERE id=$1`, appA.Credential.ID)
	if code, _ := me(appA.Credential.Token, nil); code != http.StatusOK {
		t.Errorf("credential made valid again = %d", code)
	}
	// a suspended tenant stops its applications
	e.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, a)
	if code, _ := me(appA.Credential.Token, nil); code != http.StatusUnauthorized {
		t.Errorf("suspended tenant = %d, want 401", code)
	}
	e.exec(`UPDATE tenants SET status='active' WHERE id=$1`, a)
	// scope check is 403, not 401
	if code, _, _ := e.callExt(http.MethodPost, "/api/v1/external/needs-send", appA.Credential.Token, nil); code != http.StatusForbidden {
		t.Errorf("missing scope = %d, want 403", code)
	}
	if code, _, _ := e.callExt(http.MethodPost, "/api/v1/external/needs-send", appB.Credential.Token, nil); code != http.StatusNoContent {
		t.Errorf("granted scope = %d, want 204", code)
	}
	// revoked is final
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+idA+"/revoke", nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	if code, _ := me(appA.Credential.Token, nil); code != http.StatusUnauthorized {
		t.Errorf("revoked installation = %d, want 401", code)
	}
	var live int // revoking the installation ends its credentials too, not only the lookup's view of the status
	if err := e.seed.QueryRow(e.ctx, `SELECT count(*) FROM external_app_credentials WHERE installation_id=$1 AND revoked_at IS NULL`, appA.Installation.ID).Scan(&live); err != nil || live != 0 {
		t.Errorf("credentials still live after the installation was revoked: %d (%v)", live, err)
	}
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+idA+"/reactivate", nil); code != http.StatusConflict {
		t.Errorf("reactivating a revoked installation = %d, want 409", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+idA+"/revoke", nil); code != http.StatusConflict {
		t.Errorf("revoking twice = %d, want 409", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodPatch, "/"+idA, map[string]any{"name": "again"}); code != http.StatusConflict {
		t.Errorf("editing a revoked installation = %d, want 409", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+idA+"/suspend", nil); code != http.StatusConflict {
		t.Errorf("suspending a revoked installation = %d, want 409", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+idA+"/credentials", nil); code != http.StatusConflict {
		t.Errorf("credential for a revoked installation = %d, want 409", code)
	}
	// and the other company's application is untouched by all of it
	if code, _ := me(appB.Credential.Token, nil); code != http.StatusOK {
		t.Errorf("B after A's revocation = %d", code)
	}
}

func TestFailedAuthenticationIsThrottledButValidCallsAreNot(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	app := e.install(e.member(a, "tenant_admin"), a, "CRM", "conversation.read")
	e.addr = "203.0.113.9:5000"
	bad := "omn_ext_" + strings.Repeat("a", 16) + "." + strings.Repeat("B", 43)
	got429 := false
	for i := 0; i < 80; i++ {
		if code, _, hdr := e.callExt(http.MethodGet, "/api/v1/external/me", bad, nil); code == http.StatusTooManyRequests {
			got429 = hdr.Get("Retry-After") != ""
			break
		}
	}
	if !got429 {
		t.Fatal("80 bad tokens from one address were never throttled (429 with Retry-After)")
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/me", app.Credential.Token, nil); code != http.StatusOK {
		t.Fatalf("a valid token from the throttled address = %d, want 200", code)
	}
}

func TestRecheckIsSerializedWithRevocation(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	adminA := e.member(a, "tenant_admin")
	app := e.install(adminA, a, "CRM", "conversation.read", "message.send")
	_, secret, _ := domain.ParseToken(app.Credential.Token)
	p := &domain.Principal{CredentialID: app.Credential.ID, InstallationID: app.Installation.ID, TenantID: a, Scopes: []domain.Scope{domain.ScopeMessageSend}}
	_ = secret

	// a write that already took its second look holds the rows; revoking waits for it and then wins
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	freeTheWrite := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(freeTheWrite) // a failing assertion must never leave the writing transaction open (the pool would never close)
	go func() {
		done <- platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
			if err := adapters.RecheckPrincipal(ctx, platformdb.QuerierFromContext(ctx, e.app), p); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	revoked := make(chan int, 1)
	go func() {
		code, _ := e.admin(adminA, a, http.MethodDelete, "/"+app.Installation.ID.String()+"/credentials/"+app.Credential.ID.String(), nil)
		revoked <- code
	}()
	select {
	case code := <-revoked:
		t.Fatalf("the credential was revoked (%d) while a write that had passed its recheck was still open", code)
	case <-time.After(500 * time.Millisecond):
	}
	freeTheWrite()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if code := <-revoked; code != http.StatusNoContent {
		t.Fatalf("revocation after the write committed = %d", code)
	}
	// revoked first: the next write is refused
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
		return adapters.RecheckPrincipal(ctx, platformdb.QuerierFromContext(ctx, e.app), p)
	})
	if !errors.Is(err, adapters.ErrInstallationInactive) {
		t.Fatalf("recheck after revocation = %v, want ErrInstallationInactive", err)
	}
	// the same for the installation itself (suspend), on a fresh application
	app2 := e.install(adminA, a, "CRM 2", "message.send")
	p2 := &domain.Principal{CredentialID: app2.Credential.ID, InstallationID: app2.Installation.ID, TenantID: a}
	if code, _ := e.admin(adminA, a, http.MethodPost, "/"+app2.Installation.ID.String()+"/suspend", nil); code != http.StatusOK {
		t.Fatalf("suspend = %d", code)
	}
	err = platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
		return adapters.RecheckPrincipal(ctx, platformdb.QuerierFromContext(ctx, e.app), p2)
	})
	if !errors.Is(err, adapters.ErrInstallationInactive) {
		t.Fatalf("recheck of a suspended installation = %v", err)
	}
	// a principal naming ANOTHER tenant's installation is refused as well (the function is bound to all three ids)
	// (a LIVE installation: reusing the revoked one above would be refused for that reason and prove nothing about the tenant binding)
	app3 := e.install(adminA, a, "CRM 3", "message.send")
	b := e.tenant()
	err = platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
		return adapters.RecheckPrincipal(ctx, platformdb.QuerierFromContext(ctx, e.app), &domain.Principal{CredentialID: app3.Credential.ID, InstallationID: app3.Installation.ID, TenantID: a})
	})
	if err != nil {
		t.Fatalf("control: the live installation must pass its own recheck: %v", err)
	}
	err = platformdb.WithSystemTenantSession(e.ctx, e.app, b, func(ctx context.Context) error {
		return adapters.RecheckPrincipal(ctx, platformdb.QuerierFromContext(ctx, e.app), &domain.Principal{CredentialID: app3.Credential.ID, InstallationID: app3.Installation.ID, TenantID: b})
	})
	if !errors.Is(err, adapters.ErrInstallationInactive) {
		t.Fatalf("cross-tenant recheck = %v", err)
	}
}

func TestTheApplicationRoleCannotReachTheDigestNorOtherTenantsRows(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	appA := e.install(e.member(a, "tenant_admin"), a, "CRM A", "conversation.read")
	e.install(e.member(b, "tenant_admin"), b, "CRM B", "conversation.read")

	// the digest column has no privilege for the application role, even in a system session
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
		_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, `SELECT secret_digest FROM external_app_credentials`)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("SELECT secret_digest = %v, want permission denied (42501)", err)
	}
	// a system session sees NO rows of these tables (no system branch in the policies): a forgotten tenant predicate cannot leak or change anything
	for _, table := range []string{"external_app_installations", "external_app_credentials", "external_app_installation_queues", "external_app_installation_channels"} {
		var n int
		col := "*"
		if table == "external_app_credentials" {
			col = "id"
		}
		err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
			return platformdb.QuerierFromContext(ctx, e.app).QueryRow(ctx, `SELECT count(`+col+`) FROM `+table).Scan(&n)
		})
		if err != nil || n != 0 {
			t.Errorf("system session reads %d rows of %s (%v), want 0", n, table, err)
		}
	}
	err = platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
		tag, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, `UPDATE external_app_installations SET name = 'pwned'`)
		if err == nil && tag.RowsAffected() != 0 {
			t.Errorf("a system session changed %d installations", tag.RowsAffected())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.seed.QueryRow(e.ctx, `SELECT count(*) FROM external_app_installations WHERE name='pwned'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pwned rows = %d (%v)", n, err)
	}
	// the touch function is bound to the credential's own installation and tenant
	if _, err := e.app.Exec(e.ctx, `SELECT external_credential_touch($1,$2,$3)`, b, appA.Installation.ID, appA.Credential.ID); err != nil {
		t.Fatal(err)
	}
	var used *time.Time
	if err := e.seed.QueryRow(e.ctx, `SELECT last_used_at FROM external_app_credentials WHERE id=$1`, appA.Credential.ID).Scan(&used); err != nil || used != nil {
		t.Fatalf("touch with another tenant's id changed last_used_at: %v %v", used, err)
	}
	if _, err := e.app.Exec(e.ctx, `SELECT external_credential_touch($1,$2,$3)`, a, appA.Installation.ID, appA.Credential.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.seed.QueryRow(e.ctx, `SELECT last_used_at FROM external_app_credentials WHERE id=$1`, appA.Credential.ID).Scan(&used); err != nil || used == nil {
		t.Fatalf("touch with the right ids did not record use: %v %v", used, err)
	}
	// the allow-list door is bound to tenant + installation too
	var qs, cs []string
	if err := e.app.QueryRow(e.ctx, `SELECT queue_ids, channel_ids FROM external_installation_allowlists($1,$2)`, b, appA.Installation.ID).Scan(&qs, &cs); err == nil {
		t.Fatalf("allowlists of A's installation under tenant B returned a row: %v %v", qs, cs)
	}
}

func TestAllowListsAreLimitedToTheCompanyAndShownToTheApplication(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	adminA := e.member(a, "tenant_admin")
	q1, q2 := e.queue(a), e.queue(a)
	code, body := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "CRM", "scopes": []string{"conversation.read"}, "queue_ids": []string{q1.String(), q2.String()}})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var c created
	_ = json.Unmarshal(body, &c)
	code, out, _ := e.callExt(http.MethodGet, "/api/v1/external/me", c.Credential.Token, nil)
	if code != http.StatusOK {
		t.Fatalf("me = %d %s", code, out)
	}
	var m struct {
		Allowlist struct {
			QueueIDs   []string `json:"queue_ids"`
			ChannelIDs []string `json:"channel_ids"`
		}
		Features map[string]bool
	}
	_ = json.Unmarshal(out, &m)
	if len(m.Allowlist.QueueIDs) != 2 || len(m.Allowlist.ChannelIDs) != 0 {
		t.Fatalf("allowlist = %+v", m.Allowlist)
	}
	if m.Features["read_api"] || m.Features["write_api"] || m.Features["events_api"] {
		t.Fatalf("features claim more than is wired: %v", m.Features)
	}
	// replacing and clearing
	if code, _ := e.admin(adminA, a, http.MethodPatch, "/"+c.Installation.ID.String(), map[string]any{"queue_ids": []string{q1.String()}}); code != http.StatusOK {
		t.Fatalf("patch = %d", code)
	}
	if code, _ := e.admin(adminA, a, http.MethodPatch, "/"+c.Installation.ID.String(), map[string]any{"queue_ids": []string{}}); code != http.StatusOK {
		t.Fatalf("clear = %d", code)
	}
	_, out, _ = e.callExt(http.MethodGet, "/api/v1/external/me", c.Credential.Token, nil)
	_ = json.Unmarshal(out, &m)
	if len(m.Allowlist.QueueIDs) != 0 {
		t.Fatalf("allowlist after clear = %v", m.Allowlist.QueueIDs)
	}
}

// The administration is for a person: it must never be reachable through the delegated (Hub) door. server.go lists the delegable routes and
// TestTheDelegableRoutesAndTheirKeysAreExactlyTheReviewedOnes pins that list; this test adds the positive check on the registration itself.
func TestExternalRoutesAreNeverDelegable(t *testing.T) {
	src, err := os.ReadFile("../../platform/httpserver/server.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start := strings.Index(s, "func (s *Server) RegisterExternalHandlers")
	if start < 0 {
		t.Fatal("RegisterExternalHandlers not found")
	}
	end := strings.Index(s[start:], "\nfunc (s *Server) RegisterInvitationHandlers")
	if end < 0 {
		end = len(s) - start
	}
	body := s[start : start+end]
	if regexp.MustCompile(`Delegable`).MatchString(body) {
		t.Fatal("RegisterExternalHandlers uses Delegable: the integration administration must not be reachable by a Hub agent")
	}
	if !strings.Contains(body, "/api/v1/external/me") {
		t.Fatal("the /external/me route is not registered by RegisterExternalHandlers")
	}
}

// The per-installation ceiling is enforced (429 + Retry-After) and is independent between installations.
func TestEachInstallationHasItsOwnRateLimit(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	code, body := e.admin(admin, a, http.MethodPost, "", map[string]any{"name": "slow", "scopes": []string{"conversation.read"}, "rate_limit_per_min": 10})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var slow created
	_ = json.Unmarshal(body, &slow)
	fast := e.install(admin, a, "fast", "conversation.read")
	limited := false
	for i := 0; i < 20; i++ {
		c, _, hdr := e.callExt(http.MethodGet, "/api/v1/external/me", slow.Credential.Token, nil)
		if c == http.StatusTooManyRequests {
			limited = hdr.Get("Retry-After") != ""
			break
		}
	}
	if !limited {
		t.Fatal("20 quick calls with a 10/min installation were never limited")
	}
	if c, _, _ := e.callExt(http.MethodGet, "/api/v1/external/me", fast.Credential.Token, nil); c != http.StatusOK {
		t.Fatalf("another installation was limited too: %d", c)
	}
}

// Each layer proves itself: the SQL function that backs authentication refuses by itself an inactive installation, an expired or revoked
// credential and a suspended company (the Go lookup repeats the status check, so the end-to-end tests alone would not notice the SQL weakening).
func TestTheLookupFunctionFiltersByItself(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	app := e.install(admin, a, "CRM", "conversation.read")
	rows := func() int {
		var n int
		if err := e.app.QueryRow(e.ctx, `SELECT count(*) FROM external_credential_lookup($1)`, app.Credential.KeyID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if rows() != 1 {
		t.Fatal("an active credential must be found")
	}
	e.exec(`UPDATE external_app_installations SET status='suspended', suspended_at=now() WHERE id=$1`, app.Installation.ID)
	if rows() != 0 {
		t.Error("lookup returned a credential of a suspended installation")
	}
	e.exec(`UPDATE external_app_installations SET status='active', suspended_at=NULL WHERE id=$1`, app.Installation.ID)
	e.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, a)
	if rows() != 0 {
		t.Error("lookup returned a credential of a suspended company")
	}
	e.exec(`UPDATE tenants SET status='active' WHERE id=$1`, a)
	e.exec(`UPDATE external_app_credentials SET created_at = now() - interval '2 days', expires_at = now() - interval '1 minute' WHERE id=$1`, app.Credential.ID)
	if rows() != 0 {
		t.Error("lookup returned an expired credential")
	}
	e.exec(`UPDATE external_app_credentials SET expires_at = now() + interval '1 day', revoked_at = now() WHERE id=$1`, app.Credential.ID)
	if rows() != 0 {
		t.Error("lookup returned a revoked credential")
	}
}

// Codex H1 (HIGH): suspending the COMPANY is serialized with the recheck of a write (the tenant row is locked too), and a lookup that passed before
// the suspension still ends in 401 at /external/me (the allow-list door refuses a suspended company).
func TestSuspendingTheCompanyIsSerializedWithAWriteAndStopsMe(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	app := e.install(admin, a, "CRM", "message.send")
	p := &domain.Principal{CredentialID: app.Credential.ID, InstallationID: app.Installation.ID, TenantID: a}

	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	go func() {
		done <- platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
			if err := adapters.RecheckPrincipal(ctx, platformdb.QuerierFromContext(ctx, e.app), p); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	suspended := make(chan error, 1)
	go func() {
		_, err := e.seed.Exec(e.ctx, `UPDATE tenants SET status='suspended' WHERE id=$1`, a)
		suspended <- err
	}()
	select {
	case err := <-suspended:
		t.Fatalf("the company was suspended (%v) while a write that had passed its recheck was still open", err)
	case <-time.After(500 * time.Millisecond):
	}
	free()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-suspended; err != nil {
		t.Fatal(err)
	}
	// after the suspension: the recheck refuses, and so does /external/me even though the credential itself is untouched
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
		return adapters.RecheckPrincipal(ctx, platformdb.QuerierFromContext(ctx, e.app), p)
	})
	if !errors.Is(err, adapters.ErrInstallationInactive) {
		t.Fatalf("recheck for a suspended company = %v", err)
	}
	var qs, cs []string
	if err := e.app.QueryRow(e.ctx, `SELECT queue_ids, channel_ids FROM external_installation_allowlists($1,$2)`, a, app.Installation.ID).Scan(&qs, &cs); err == nil {
		t.Fatal("the allow-list door answered for a suspended company")
	}
	// a request whose lookup passed BEFORE the suspension must still end in 401: simulate by calling the handler's inner door with the principal
	e.exec(`UPDATE tenants SET status='active' WHERE id=$1`, a)
	_, out, _ := e.callExt(http.MethodGet, "/api/v1/external/me", app.Credential.Token, nil)
	if !strings.Contains(string(out), a.String()) {
		t.Fatalf("control: me for an active company = %s", out)
	}
}

// Codex H1 (HIGH): expiry is judged with the real clock at the recheck, not with the start of the transaction.
func TestRecheckJudgesExpiryWithTheRealClockNotTheTransactionStart(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	app := e.install(e.member(a, "tenant_admin"), a, "CRM", "message.send")
	p := &domain.Principal{CredentialID: app.Credential.ID, InstallationID: app.Installation.ID, TenantID: a}
	// the credential will expire in 2 seconds; the transaction starts BEFORE that and rechecks after it
	e.exec(`UPDATE external_app_credentials SET created_at = now() - interval '1 day', expires_at = clock_timestamp() + interval '2 seconds' WHERE id=$1`, app.Credential.ID)
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, e.app)
		if err := adapters.RecheckPrincipal(ctx, q, p); err != nil {
			t.Fatalf("control: the credential is still valid at the start: %v", err)
		}
		time.Sleep(2500 * time.Millisecond)
		if err := adapters.RecheckPrincipal(ctx, q, p); !errors.Is(err, adapters.ErrInstallationInactive) {
			t.Errorf("recheck after the expiry, inside the same transaction = %v, want ErrInstallationInactive", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Codex H1 (MEDIUM): behind the project's nginx every request comes from the proxy; the throttle keys on X-Real-IP (set by nginx), not on the proxy.
func TestFailedAuthenticationThrottleSeesTheRealClientBehindTheProxy(t *testing.T) {
	e := newEnv(t)
	bad := "omn_ext_" + strings.Repeat("a", 16) + "." + strings.Repeat("B", 43)
	e.addr = "10.20.0.5:40000" // the proxy (a private peer)
	flood := func(realIP string) int {
		last := 0
		for i := 0; i < 60; i++ {
			code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/me", bad, map[string]string{"X-Real-IP": realIP})
			last = code
			if code == http.StatusTooManyRequests {
				return code
			}
		}
		return last
	}
	if flood("203.0.113.50") != http.StatusTooManyRequests {
		t.Fatal("one client failing 60 times behind the proxy was never throttled")
	}
	// another client behind the same proxy is NOT collateral damage
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/me", bad, map[string]string{"X-Real-IP": "203.0.113.77"}); code != http.StatusUnauthorized {
		t.Fatalf("another client behind the same proxy got %d, want 401", code)
	}
	// a public peer cannot pick its own key with the header
	e.addr = "198.51.100.9:40000"
	for i := 0; i < 60; i++ {
		e.callExt(http.MethodGet, "/api/v1/external/me", bad, map[string]string{"X-Real-IP": "203.0.113." + strconv.Itoa(i+100)})
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/me", bad, map[string]string{"X-Real-IP": "203.0.113.250"}); code != http.StatusTooManyRequests {
		t.Fatalf("a public peer rotated X-Real-IP to dodge the throttle and got %d, want 429", code)
	}
}

// Codex H1 (MEDIUM): the administration through the REAL authentication chain (cookie session + same-origin check), not with a principal injected.
type fakeStore struct{ user uuid.UUID }

func (f fakeStore) CreateSession(context.Context, uuid.UUID, string, time.Duration) (string, error) {
	return "x", nil
}
func (f fakeStore) ResolveSession(_ context.Context, id string) (uuid.UUID, error) {
	if id != "good" {
		return uuid.Nil, errors.New("no")
	}
	return f.user, nil
}
func (f fakeStore) RevokeSession(context.Context, string) error  { return nil }
func (f fakeStore) UpdateActivity(context.Context, string) error { return nil }

type noBearer struct{}

func (noBearer) Verify(context.Context, string) (*authn.Principal, error) {
	return nil, errors.New("no bearer in this test")
}

func TestAdministrationRefusesCrossSiteCookieWritesThroughTheRealChain(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	app := e.install(admin, a, "CRM", "conversation.read")
	authn.EnableOriginCheck("https://omnira.example")
	t.Cleanup(authn.DisableOriginCheck)
	chain := authn.WebMiddleware(noBearer{}, fakeStore{user: admin})(e.mux)

	do := func(method, path, origin string, body string) int {
		req := httptest.NewRequest(method, "/api/v1/tenants/"+a.String()+"/integrations/apps"+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: "good"})
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec.Code
	}
	id := app.Installation.ID.String()
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "", `{"name":"evil","scopes":["conversation.read"]}`},
		{http.MethodPatch, "/" + id, `{"name":"evil"}`},
		{http.MethodPost, "/" + id + "/credentials", ``},
		{http.MethodPost, "/" + id + "/revoke", ``},
		{http.MethodDelete, "/" + id + "/credentials/" + app.Credential.ID.String(), ``},
	} {
		if code := do(c.method, c.path, "https://evil.example", c.body); code != http.StatusForbidden {
			t.Errorf("cross-site %s %s = %d, want 403", c.method, c.path, code)
		}
		if code := do(c.method, c.path, "", c.body); code != http.StatusForbidden {
			t.Errorf("%s %s with no Origin/Referer = %d, want 403", c.method, c.path, code)
		}
	}
	// nothing the cross-site calls attempted happened
	if code, body := e.admin(admin, a, http.MethodGet, "/"+id, nil); code != http.StatusOK || !strings.Contains(string(body), `"status":"active"`) || strings.Contains(string(body), "evil") {
		t.Fatalf("the installation changed: %d %s", code, body)
	}
	// the same call from the application's own origin works; a safe GET from anywhere is not blocked
	if code := do(http.MethodPost, "/"+id+"/credentials", "https://omnira.example", ``); code != http.StatusCreated {
		t.Errorf("same-origin POST = %d, want 201", code)
	}
	if code := do(http.MethodGet, "", "https://evil.example", ``); code != http.StatusOK {
		t.Errorf("cross-site GET = %d, want 200 (a safe method)", code)
	}
}

// Codex H1 (LOW): the audit row of an administrative action carries the request id as its correlation id.
func TestAuditRowsOfAdministrationCarryTheRequestID(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	rid := uuid.New()
	b, _ := json.Marshal(map[string]any{"name": "CRM", "scopes": []string{"conversation.read"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+a.String()+"/integrations/apps", bytes.NewReader(b))
	req.Header.Set("X-Request-ID", rid.String())
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: admin, Subject: admin.String()}))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var got uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT correlation_id FROM audit_events WHERE tenant_id=$1 AND action='external_app.created'`, a).Scan(&got); err != nil || got != rid {
		t.Fatalf("correlation id = %s (%v), want the request id %s", got, err, rid)
	}
}
