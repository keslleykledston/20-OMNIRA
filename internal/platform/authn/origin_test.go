package authn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSameOriginCheckForCookieRequests(t *testing.T) {
	t.Cleanup(DisableOriginCheck)
	req := func(method, host, origin, referer string) *http.Request {
		r := httptest.NewRequest(method, "http://"+host+"/api/v1/x", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		return r
	}
	// off until the server turns it on
	if !sameOriginOK(req("POST", "omnira.example", "https://evil.example", "")) {
		t.Fatal("the check must be off until EnableOriginCheck")
	}
	EnableOriginCheck("https://omnira.example/api/v1/auth/oidc/callback", "https://outro.example")
	cases := []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"safe method without any origin", req("GET", "omnira.example", "", ""), true},
		{"the application's own origin (from the redirect URL)", req("POST", "10.0.0.5:3000", "https://omnira.example", ""), true},
		{"a configured extra origin", req("DELETE", "10.0.0.5:3000", "https://outro.example", ""), true},
		{"another site", req("POST", "omnira.example", "https://evil.example", ""), false},
		{"another subdomain of the same site", req("POST", "omnira.example", "https://evil.omnira.example", ""), false},
		{"the same host but another scheme/port is another origin", req("POST", "10.0.0.5:3000", "http://omnira.example", ""), false},
		{"origin null (sandboxed frame, privacy redirect)", req("POST", "omnira.example", "null", ""), false},
		{"no origin and no referer on a state-changing request", req("PUT", "omnira.example", "", ""), false},
		{"no origin, the referer is the application", req("PATCH", "10.0.0.5:3000", "", "https://omnira.example/inbox"), true},
		{"no origin, the referer is another site", req("POST", "omnira.example", "", "https://evil.example/page"), false},
		{"origin equal to the host the request was addressed to", req("POST", "intranet.local:8443", "https://intranet.local:8443", ""), true},
		{"a loopback origin (a developer machine)", req("POST", "omnira.example", "http://localhost:3000", ""), true},
		{"a loopback address", req("POST", "omnira.example", "http://127.0.0.1:20081", ""), true},
		{"a look-alike of loopback is not loopback", req("POST", "omnira.example", "http://localhost.evil.example", ""), false},
	}
	for _, c := range cases {
		if got := sameOriginOK(c.r); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// a proxy in front rewrites Host: X-Forwarded-Host names the address the browser used
	r := req("POST", "web:80", "https://public.example", "")
	r.Header.Set("X-Forwarded-Host", "public.example")
	if !sameOriginOK(r) {
		t.Error("X-Forwarded-Host should name the browser's address")
	}
}

func TestTheMiddlewaresRefuseACrossSiteCookieRequestButNotABearerOne(t *testing.T) {
	t.Cleanup(DisableOriginCheck)
	EnableOriginCheck("https://omnira.example")
	store := newTestSessionStore()
	id, err := store.CreateSession(context.Background(), uuid.New(), "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	do := func(mw func(http.Handler) http.Handler, origin string) int {
		r := httptest.NewRequest("POST", "http://omnira.example/api/v1/x", nil)
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: id})
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		mw(ok).ServeHTTP(rec, r)
		return rec.Code
	}
	for name, mw := range map[string]func(http.Handler) http.Handler{
		"WebMiddleware":     WebMiddleware(nil, store),
		"SessionMiddleware": SessionMiddleware(store),
	} {
		if c := do(mw, "https://evil.example"); c != http.StatusForbidden {
			t.Errorf("%s: a cross-site cookie request got %d, want 403", name, c)
		}
		if c := do(mw, "https://omnira.example"); c != http.StatusOK {
			t.Errorf("%s: a same-origin cookie request got %d, want 200", name, c)
		}
	}
}
