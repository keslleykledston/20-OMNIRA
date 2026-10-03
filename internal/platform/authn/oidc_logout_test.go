package authn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Logout must end the identity provider's session too, otherwise the next login
// silently reuses the previous account (and the user can never switch accounts).

const testCallback = "https://app.example/api/v1/auth/oidc/callback"

func logoutHandler(discovery OIDCDiscovery, callback string) (*OIDCHandler, *fakeSessionStore, string) {
	store := &fakeSessionStore{sessions: map[string]uuid.UUID{}}
	sessionID, _ := store.CreateSession(nil, uuid.New(), "oidc", 0) //nolint:staticcheck // fake ignores ctx
	h := NewOIDCHandler(nil, discovery, nil, store, "https://idp.example/realms/omnira", "omnira-web", "secret", callback, "/login?oidc=complete", true)
	return h, store, sessionID
}

func doLogout(h *OIDCHandler, sessionID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	rec := httptest.NewRecorder()
	h.Logout(rec, req)
	return rec
}

func TestLogoutReturnsTheProvidersEndSessionURL(t *testing.T) {
	h, store, sid := logoutHandler(OIDCDiscovery{EndSessionEndpoint: "https://idp.example/realms/omnira/protocol/openid-connect/logout"}, testCallback)

	rec := doLogout(h, sid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 with the end-session URL", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	u, err := url.Parse(body["end_session_url"])
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "idp.example" || u.Path != "/realms/omnira/protocol/openid-connect/logout" {
		t.Fatalf("must point at the provider's end-session endpoint: %s", u)
	}
	if u.Query().Get("client_id") != "omnira-web" {
		t.Fatalf("client_id = %q", u.Query().Get("client_id"))
	}
	if got := u.Query().Get("post_logout_redirect_uri"); got != "https://app.example/login" {
		t.Fatalf("post_logout_redirect_uri = %q, want the app's /login", got)
	}
	// Our own session is still revoked and the cookie expired, whatever the provider does.
	if _, err := store.ResolveSession(nil, sid); err == nil { //nolint:staticcheck
		t.Fatal("the app session must be revoked")
	}
	expired := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			expired = true
		}
	}
	if !expired {
		t.Fatal("the session cookie must be expired")
	}
}

func TestLogoutURLNeverCarriesRequestInput(t *testing.T) {
	h, _, sid := logoutHandler(OIDCDiscovery{EndSessionEndpoint: "https://idp.example/logout"}, testCallback)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout?post_logout_redirect_uri=https://evil.example&redirect=https://evil.example", nil)
	req.Header.Set("Host", "evil.example")
	req.Header.Set("Referer", "https://evil.example/")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sid})
	rec := httptest.NewRecorder()
	h.Logout(rec, req)
	if strings.Contains(rec.Body.String(), "evil.example") {
		t.Fatalf("no request value may reach the end-session URL (open redirect): %s", rec.Body.String())
	}
}

func TestLogoutKeepsAnExistingQueryOfTheEndpoint(t *testing.T) {
	h, _, sid := logoutHandler(OIDCDiscovery{EndSessionEndpoint: "https://idp.example/logout?ui_locales=pt-BR"}, testCallback)
	var body map[string]string
	_ = json.Unmarshal(doLogout(h, sid).Body.Bytes(), &body)
	u, _ := url.Parse(body["end_session_url"])
	if u.Query().Get("ui_locales") != "pt-BR" || u.Query().Get("client_id") != "omnira-web" {
		t.Fatalf("existing query must be kept and ours added: %s", u)
	}
}

func TestLogoutIs204WhenTheProviderHasNoEndSessionEndpointOrNoUsableOrigin(t *testing.T) {
	for name, tc := range map[string]struct {
		discovery OIDCDiscovery
		callback  string
	}{
		"no end-session endpoint":  {OIDCDiscovery{}, testCallback},
		"callback without origin":  {OIDCDiscovery{EndSessionEndpoint: "https://idp.example/logout"}, "/api/v1/auth/oidc/callback"},
		"unparseable callback url": {OIDCDiscovery{EndSessionEndpoint: "https://idp.example/logout"}, "://bad"},
	} {
		h, store, sid := logoutHandler(tc.discovery, tc.callback)
		rec := doLogout(h, sid)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: status %d, want 204 (the previous behaviour)", name, rec.Code)
		}
		if _, err := store.ResolveSession(nil, sid); err == nil { //nolint:staticcheck
			t.Fatalf("%s: the app session must still be revoked", name)
		}
	}
}

func TestLogoutWithoutACookieStillAnswers(t *testing.T) {
	h, _, _ := logoutHandler(OIDCDiscovery{EndSessionEndpoint: "https://idp.example/logout"}, testCallback)
	rec := httptest.NewRecorder()
	h.Logout(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("a logout without a session must still hand back the provider URL, got %d", rec.Code)
	}
}
