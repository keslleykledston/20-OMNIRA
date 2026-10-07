package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/config"
	"github.com/omnira/omnira/internal/testhelpers"
)

// MOBILE.1 end to end through the REAL route wiring and PostgreSQL with RLS: a native app credential is authenticated by the same boundary as
// the browser, tenant isolation is exactly the web's (404 for a foreign tenant), and revocation is immediate.
func TestDeviceTokenGoesThroughTheSameTenantIsolationAsTheBrowser(t *testing.T) {
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
	alice, bob := uuid.New(), uuid.New()
	tenantA, tenantB := uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{alice, bob} {
		exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u.String(), u.String()+"@invalid")
	}
	for _, tn := range []uuid.UUID{tenantA, tenantB} {
		exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id IN ($1,$2)`, alice, bob)
	})
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantA, alice, role)
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantB, bob, role)

	devices := authn.NewPostgresDeviceStore(seed)
	sessions := authn.NewPostgresSessionStore(seed)
	s := New("127.0.0.1:0")
	s.RegisterHealthHandlers()
	s.RegisterAuthHandlers(nil, false, nil, 0) // RSA keys the other registrations need
	s.RegisterOIDCAuthHandlers(authn.NewDeviceAuthenticator(s.authenticator, devices), sessions, contractOIDCHandler{})
	s.SetSessionChecker(authn.NewSessionChecker(sessions, devices))
	s.RegisterMobileAuthHandlers(contractMobileHandler{})
	s.RegisterTenancyHandlers(app, false)
	s.RegisterInboxHandlers(app, &config.Config{})

	get := func(path, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		// A client may claim any tenant/user in headers: they must never matter.
		req.Header.Set("X-Tenant-ID", tenantB.String())
		req.Header.Set("X-User-ID", bob.String())
		rr := httptest.NewRecorder()
		s.mux.ServeHTTP(rr, req)
		return rr
	}

	pair, err := devices.IssueForLogin(ctx, alice, "Pixel", "android", nil)
	if err != nil {
		t.Fatal(err)
	}
	own := "/api/v1/tenants/" + tenantA.String() + "/inbox/conversations"
	foreign := "/api/v1/tenants/" + tenantB.String() + "/inbox/conversations"

	if rr := get(own, pair.AccessToken); rr.Code != http.StatusOK {
		t.Fatalf("own tenant: %d %q", rr.Code, rr.Body.String())
	}
	if rr := get(foreign, pair.AccessToken); rr.Code != http.StatusNotFound {
		t.Fatalf("foreign tenant must be 404 for a device token, got %d %q", rr.Code, rr.Body.String())
	}
	if rr := get(own, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no credential: %d", rr.Code)
	}
	// A refresh token is not an API credential, and neither is a token of the wrong shape.
	if rr := get(own, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh token accepted as an access token: %d", rr.Code)
	}
	if rr := get(own, authn.AccessTokenPrefix+"forged"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("forged device token: %d", rr.Code)
	}

	// Revoking the membership closes the tenant even though the device token is still valid (authorization is per request).
	exec(`UPDATE memberships SET status='revoked' WHERE tenant_id=$1 AND user_id=$2`, tenantA, alice)
	if rr := get(own, pair.AccessToken); rr.Code == http.StatusOK {
		t.Fatalf("revoked membership still served through a device token: %d", rr.Code)
	}
	exec(`UPDATE memberships SET status='active' WHERE tenant_id=$1 AND user_id=$2`, tenantA, alice)
	if rr := get(own, pair.AccessToken); rr.Code != http.StatusOK {
		t.Fatalf("membership restored: %d", rr.Code)
	}

	// Revoking the device is immediate.
	if err := devices.RevokeDevice(ctx, alice, pair.DeviceID, authn.RevokedUser); err != nil {
		t.Fatal(err)
	}
	if rr := get(own, pair.AccessToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("revoked device still authenticated: %d", rr.Code)
	}
}
