package adapters_test

// MT-AT-040 (docs/architecture/HUB-ACCEPTANCE-MATRIX.md): a VALID token (signed by the trusted issuer, right audience, not expired) that carries claims naming another
// company gains nothing. The company of a request is resolved by the server from the person's membership and the path, never from the token. Real
// JWT authenticator and the real authorization middleware, on a real PostgreSQL with the application role.

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestAValidTokenNamingAnotherCompanyGainsNothing(t *testing.T) {
	w := newWorld(t)
	member := w.user("member-of-a-only")
	w.directMember(member, "A")

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	w.must(err)
	auth := authn.NewJWTAuthenticator(&key.PublicKey, "test-issuer", "test-audience")
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	var mu sync.Mutex
	var seen *tenancydomain.TenantContext
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/probe", tenancyadapters.AuthorizationMiddleware(w.app, authz)(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		tc, err := tenancydomain.FromContext(r.Context())
		if err != nil {
			http.Error(rw, "no context", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		seen = tc
		mu.Unlock()
		rw.WriteHeader(http.StatusOK)
	})))
	h := authn.Middleware(auth)(mux)

	token := func(extra jwt.MapClaims) string {
		claims := jwt.MapClaims{"sub": "member", "user_id": member.String(), "iss": "test-issuer", "aud": []string{"test-audience"}, "exp": time.Now().Add(time.Hour).Unix()}
		for k, v := range extra {
			claims[k] = v
		}
		raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
		w.must(err)
		return raw
	}
	get := func(tenantKey string, tok string, hdr map[string]string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+w.tenant[tenantKey].String()+"/probe", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	b := w.tenant["B"].String()
	naming := token(jwt.MapClaims{"tenant_id": b, "tid": b, "tenant": b, "tenants": []string{b}, "company": b, "roles": []string{"system_admin", "hub_admin"}, "is_system_admin": true})

	if code := get("A", naming, nil); code != http.StatusOK {
		t.Fatalf("the person's own company with a token that also names B: %d, want 200", code)
	}
	mu.Lock()
	if seen == nil || seen.TenantID != w.tenant["A"] || seen.Source != tenancydomain.AccessSourceDirect {
		t.Fatalf("the effective context is %+v; it must be company A through the person's membership, whatever the token claims", seen)
	}
	mu.Unlock()
	for name, code := range map[string]int{
		"path B with a token naming B":                      get("B", naming, nil),
		"path B with a plain token":                         get("B", token(nil), nil),
		"path B, token naming B, X-Tenant-ID header B":      get("B", naming, map[string]string{"X-Tenant-ID": b}),
		"path C (a company the person never heard of)":      get("C", naming, nil),
		"path B claiming to act for the Hub that serves it": get("B", naming, map[string]string{tenancydomain.ActingAsHeader: "hub:" + w.hub.String()}),
	} {
		if code == http.StatusOK {
			t.Errorf("%s: answered 200; the token's claim must not open a company the person does not belong to", name)
		}
	}
	// and a token that is not trusted is refused before any of this
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	w.must(err)
	forged, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": "x", "user_id": member.String(), "iss": "test-issuer", "aud": []string{"test-audience"}, "exp": time.Now().Add(time.Hour).Unix(), "tenant_id": b}).SignedString(other)
	w.must(err)
	if code := get("A", forged, nil); code != http.StatusUnauthorized {
		t.Errorf("a token signed by another key: %d, want 401", code)
	}
}
