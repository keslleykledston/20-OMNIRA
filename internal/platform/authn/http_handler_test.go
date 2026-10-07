package authn

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A / J: dev login creates an opaque server-side session; the cookie it sets
// is not JWT-shaped (a JWT always has exactly two dots: header.payload.sig).
func TestDevLogin_CookieIsOpaqueNotJWT(t *testing.T) {
	privateKey, _, err := GenerateTestRSAKeys()
	if err != nil {
		t.Fatal(err)
	}
	store := newTestSessionStore()
	handler := NewAuthHandler(privateKey, store, 0, false)

	body, _ := json.Marshal(MockLoginRequest{Email: "test@omnira.local"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/dev/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.DevLogin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}

	resp := rec.Result()
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("omnira_session cookie not set")
	}
	if strings.Count(sessionCookie.Value, ".") == 2 {
		t.Fatalf("cookie value looks like a JWT (header.payload.sig): %q", sessionCookie.Value)
	}

	// The opaque id must resolve through the same session store dev login used.
	userID, err := store.ResolveSession(context.Background(), sessionCookie.Value)
	if err != nil {
		t.Fatalf("cookie value does not resolve as a server-side session: %v", err)
	}
	if userID.String() == "" {
		t.Fatal("resolved empty user id")
	}
}

// I: cookie security attributes match the expected contract.
func TestDevLogin_CookieSecurityAttributes(t *testing.T) {
	privateKey, _, err := GenerateTestRSAKeys()
	if err != nil {
		t.Fatal(err)
	}
	store := newTestSessionStore()

	for _, tc := range []struct {
		name         string
		secureCookie bool
	}{
		{"local/http dev", false},
		{"production/https", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewAuthHandler(privateKey, store, 0, tc.secureCookie)
			body, _ := json.Marshal(MockLoginRequest{Email: "test@omnira.local"})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/dev/login", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			handler.DevLogin(rec, req)

			var sessionCookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == SessionCookieName {
					sessionCookie = c
				}
			}
			if sessionCookie == nil {
				t.Fatal("omnira_session cookie not set")
			}
			if !sessionCookie.HttpOnly {
				t.Error("expected HttpOnly=true")
			}
			if sessionCookie.Secure != tc.secureCookie {
				t.Errorf("Secure=%v, want %v", sessionCookie.Secure, tc.secureCookie)
			}
			if sessionCookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite=%v, want Lax", sessionCookie.SameSite)
			}
			if sessionCookie.Path != "/" {
				t.Errorf("Path=%q, want /", sessionCookie.Path)
			}
			if sessionCookie.MaxAge <= 0 {
				t.Errorf("MaxAge=%d, want > 0 (coherent with server-side expiry)", sessionCookie.MaxAge)
			}
		})
	}
}

// F: dev-mode logout revokes server-side and expires the cookie; a replayed
// cookie value fails resolution afterwards.
func TestDevLogout_RevokesSessionAndExpiresCookie(t *testing.T) {
	privateKey, _, err := GenerateTestRSAKeys()
	if err != nil {
		t.Fatal(err)
	}
	store := newTestSessionStore()
	handler := NewAuthHandler(privateKey, store, 0, false)

	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/dev/login", bytes.NewReader(mustJSON(t, MockLoginRequest{Email: "test@omnira.local"})))
	loginRec := httptest.NewRecorder()
	handler.DevLogin(loginRec, loginReq)
	var sessionID string
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == SessionCookieName {
			sessionID = c.Value
		}
	}
	if sessionID == "" {
		t.Fatal("no session cookie from login")
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	logoutRec := httptest.NewRecorder()
	handler.Logout(logoutRec, logoutReq)

	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d, want 204", logoutRec.Code)
	}
	var expired *http.Cookie
	for _, c := range logoutRec.Result().Cookies() {
		if c.Name == SessionCookieName {
			expired = c
		}
	}
	if expired == nil || expired.MaxAge >= 0 {
		t.Fatalf("expected cookie to be expired (MaxAge<0), got %+v", expired)
	}

	if _, err := store.ResolveSession(context.Background(), sessionID); err == nil {
		t.Fatal("replayed cookie after logout still resolves — session was not revoked")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
