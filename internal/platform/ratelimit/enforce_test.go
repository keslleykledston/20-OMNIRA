package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// R-2: through the real middleware (pre-auth, no tenant context) the per-tenant and per-user quotas are enforced by the post-authorization
// hook, and one tenant exhausting its quota never throttles another.
func TestTenantAndUserQuotasApplyAfterAuthorizationAndAreIsolated(t *testing.T) {
	l := NewLimiter()
	l.SetQuota(QuotaTypeTenant, 3, time.Minute)
	l.SetQuota(QuotaTypeUser, 100, time.Minute)
	l.SetQuota(QuotaTypeGlobal, 1000, time.Minute)

	tenantA, tenantB := uuid.New(), uuid.New()
	// Stands in for tenancy.AuthorizationMiddleware: the tenant and actor come from a verified membership, never from the request.
	handler := Middleware(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantA
		if r.URL.Query().Get("t") == "b" {
			tenant = tenantB
		}
		if !EnforceTenantUser(w, r, tenant, uuid.MustParse(r.URL.Query().Get("u"))) {
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	hit := func(tenant string, user uuid.UUID) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("GET", "/x?t="+tenant+"&u="+user.String(), nil))
		return rr
	}
	alice, bob := uuid.New(), uuid.New()
	for i := 0; i < 3; i++ {
		if rr := hit("a", alice); rr.Code != 200 {
			t.Fatalf("request %d: %d", i, rr.Code)
		}
	}
	rr := hit("a", bob) // another user of the SAME tenant: the tenant bucket is shared and spent
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("tenant quota not enforced: %d %v", rr.Code, rr.Header())
	}
	if rr := hit("b", alice); rr.Code != 200 {
		t.Fatalf("a noisy tenant A throttled tenant B: %d", rr.Code)
	}
}

func TestUserQuotaIsPerUser(t *testing.T) {
	l := NewLimiter()
	l.SetQuota(QuotaTypeUser, 2, time.Minute)
	tenant := uuid.New()
	handler := Middleware(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !EnforceTenantUser(w, r, tenant, uuid.MustParse(r.Header.Get("X-Test-User"))) {
			return
		}
		w.WriteHeader(200)
	}))
	hit := func(u uuid.UUID) int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("X-Test-User", u.String())
		handler.ServeHTTP(rr, req)
		return rr.Code
	}
	alice, bob := uuid.New(), uuid.New()
	hit(alice)
	hit(alice)
	if hit(alice) != 429 {
		t.Fatal("user quota not enforced")
	}
	if hit(bob) != 200 {
		t.Fatal("alice's quota throttled bob")
	}
}

func TestEnforceAllowsWhenNoLimiterIsInstalled(t *testing.T) {
	rr := httptest.NewRecorder()
	if !EnforceTenantUser(rr, httptest.NewRequest("GET", "/x", nil), uuid.New(), uuid.New()) {
		t.Fatal("must allow without a limiter in the context")
	}
}
