package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	"github.com/omnira/omnira/internal/testhelpers"
)

// ADR-0022 decision 3, refined: an installation belongs to the user and spans every tenant they are in, so revoking another user's device
// needs membership.manage in EVERY tenant of the target - not just the path tenant.
func TestAdminRevokesAnotherUsersDeviceOnlyWithAuthorityOverEveryTenantOfTheTarget(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	newUser := func() uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, id, id.String(), id.String()+"@invalid")
		t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		return id
	}
	tenantA, tenantB := uuid.New(), uuid.New()
	for _, tn := range []uuid.UUID{tenantA, tenantB} {
		exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
	})
	role := func(key string) uuid.UUID {
		var id uuid.UUID
		if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	admin, agent := role("tenant_admin"), role("tenant_agent")
	member := func(tenant, user, roleID uuid.UUID) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO memberships(id,tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4,'active')`, id, tenant, user, roleID)
		return id
	}
	adminA, adminAB, plainAgent := newUser(), newUser(), newUser()
	onlyA, bothAB := newUser(), newUser()
	member(tenantA, adminA, admin)
	member(tenantA, adminAB, admin)
	member(tenantB, adminAB, admin)
	member(tenantA, plainAgent, agent)
	onlyAMembership := member(tenantA, onlyA, agent)
	bothMembershipInA := member(tenantA, bothAB, agent)
	member(tenantB, bothAB, agent)

	devices := authn.NewPostgresDeviceStore(seed)
	sessions := authn.NewPostgresSessionStore(seed)
	s := New("127.0.0.1:0")
	s.RegisterHealthHandlers()
	s.RegisterAuthHandlers(nil, false, nil, 0)
	s.RegisterOIDCAuthHandlers(authn.NewDeviceAuthenticator(s.authenticator, devices), sessions, contractOIDCHandler{})
	s.RegisterTenancyHandlers(app, false)
	s.RegisterDeviceAdminHandlers(app, tenancyadapters.NewDeviceAdminHandler(app, devices, auditadapters.NewPostgresAuditEventRepository(app)))

	tokenOf := func(u uuid.UUID) authn.TokenPair {
		p, err := devices.IssueForLogin(ctx, u, "phone", "android", nil)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	do := func(method, path, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rr := httptest.NewRecorder()
		s.mux.ServeHTTP(rr, req)
		return rr
	}
	base := "/api/v1/tenants/" + tenantA.String() + "/team/"

	both := tokenOf(bothAB)
	only := tokenOf(onlyA)
	adminAToken := tokenOf(adminA).AccessToken
	adminABToken := tokenOf(adminAB).AccessToken
	agentToken := tokenOf(plainAgent).AccessToken

	// An administrator of A alone cannot touch a user who is also in B.
	if rr := do("GET", base+bothMembershipInA.String()+"/devices", adminAToken); rr.Code != http.StatusForbidden {
		t.Fatalf("list across tenants without authority over B: %d %q", rr.Code, rr.Body.String())
	}
	if rr := do("DELETE", base+bothMembershipInA.String()+"/devices/"+both.DeviceID.String(), adminAToken); rr.Code != http.StatusForbidden {
		t.Fatalf("revoke across tenants without authority over B: %d", rr.Code)
	}
	if _, err := devices.ResolveAccess(ctx, both.AccessToken); err != nil {
		t.Fatalf("the device was revoked despite the refusal: %v", err)
	}
	// A non-administrator cannot, even for a single-tenant user.
	if rr := do("DELETE", base+onlyAMembership.String()+"/devices/"+only.DeviceID.String(), agentToken); rr.Code != http.StatusForbidden {
		t.Fatalf("agent revoking: %d", rr.Code)
	}
	// An administrator of A can revoke the device of a user who exists only in A.
	if rr := do("GET", base+onlyAMembership.String()+"/devices", adminAToken); rr.Code != http.StatusOK {
		t.Fatalf("list single-tenant target: %d %q", rr.Code, rr.Body.String())
	}
	if rr := do("DELETE", base+onlyAMembership.String()+"/devices/"+only.DeviceID.String(), adminAToken); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke single-tenant target: %d %q", rr.Code, rr.Body.String())
	}
	if _, err := devices.ResolveAccess(ctx, only.AccessToken); err == nil {
		t.Fatal("revoked device still authenticates")
	}
	// An administrator of both tenants can revoke the multi-tenant user's device.
	if rr := do("DELETE", base+bothMembershipInA.String()+"/devices/"+both.DeviceID.String(), adminABToken); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke multi-tenant target with authority over both: %d %q", rr.Code, rr.Body.String())
	}
	if _, err := devices.ResolveAccess(ctx, both.AccessToken); err == nil {
		t.Fatal("revoked device still authenticates")
	}
	// Foreign or unknown membership ids and device ids: 404, never an oracle.
	if rr := do("GET", base+uuid.NewString()+"/devices", adminABToken); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown membership: %d", rr.Code)
	}
	if rr := do("DELETE", base+bothMembershipInA.String()+"/devices/"+uuid.NewString(), adminABToken); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown device: %d", rr.Code)
	}
	// An administrator's own installations are not managed through this route.
	var selfMembership uuid.UUID
	_ = seed.QueryRow(ctx, `SELECT id FROM memberships WHERE tenant_id=$1 AND user_id=$2`, tenantA, adminAB).Scan(&selfMembership)
	if rr := do("GET", base+selfMembership.String()+"/devices", adminABToken); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("self: %d", rr.Code)
	}
	// Both revocations were audited with the right tenant, actor and resource - and nothing secret.
	var audited int
	_ = seed.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='device.revoked_by_admin'`, tenantA).Scan(&audited)
	if audited != 2 {
		t.Fatalf("audited admin revocations = %d, want 2", audited)
	}
	var actor, resource uuid.UUID
	var meta string
	if err := seed.QueryRow(ctx, `SELECT actor_id, resource_id, metadata::text FROM audit_events WHERE tenant_id=$1 AND action='device.revoked_by_admin' AND resource_id=$2`,
		tenantA, both.DeviceID).Scan(&actor, &resource, &meta); err != nil {
		t.Fatal(err)
	}
	if actor != adminAB || resource != both.DeviceID || !strings.Contains(meta, bothAB.String()) || strings.Contains(meta, "omn_") {
		t.Fatalf("audit event: actor=%s resource=%s meta=%s", actor, resource, meta)
	}
	// Deleting one's own installation through the administration route is refused (422), also for DELETE.
	own := tokenOf(adminAB)
	if rr := do("DELETE", base+selfMembership.String()+"/devices/"+own.DeviceID.String(), adminABToken); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("self delete: %d", rr.Code)
	}
	// A target whose membership in ANOTHER tenant is not active does not block the administrator of the path tenant.
	inactiveInB := newUser()
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'inactive')`, tenantB, inactiveInB, agent)
	mid := member(tenantA, inactiveInB, agent)
	inactiveToken := tokenOf(inactiveInB)
	if rr := do("DELETE", base+mid.String()+"/devices/"+inactiveToken.DeviceID.String(), adminAToken); rr.Code != http.StatusNoContent {
		t.Fatalf("an inactive membership elsewhere must not block: %d %q", rr.Code, rr.Body.String())
	}
	// A membership id that belongs to ANOTHER tenant is not found through this tenant's URL.
	var foreignMembership uuid.UUID
	_ = seed.QueryRow(ctx, `SELECT id FROM memberships WHERE tenant_id=$1 AND user_id=$2`, tenantB, adminAB).Scan(&foreignMembership)
	if rr := do("GET", base+foreignMembership.String()+"/devices", adminABToken); rr.Code != http.StatusNotFound {
		t.Fatalf("membership of another tenant through this URL: %d", rr.Code)
	}
}
