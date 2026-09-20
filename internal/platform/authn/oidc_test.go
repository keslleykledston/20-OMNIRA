package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type fakeOIDCResolver struct{ userID uuid.UUID }

func (r fakeOIDCResolver) ResolveUserID(_ context.Context, issuer, subject string) (uuid.UUID, error) {
	if issuer == "" || subject != "idp-user-1" {
		return uuid.Nil, context.Canceled
	}
	return r.userID, nil
}
func (r fakeOIDCResolver) ResolveIdentity(context.Context, string, string) (uuid.UUID, error) {
	return r.userID, nil
}
func (r fakeOIDCResolver) ProvisionIdentity(context.Context, string, string, string, string) (uuid.UUID, error) {
	return r.userID, nil
}
func (r fakeOIDCResolver) SessionProfile(context.Context, uuid.UUID) (SessionProfile, error) {
	return SessionProfile{User: SessionUser{ID: r.userID.String()}, Tenant: SessionTenant{ID: uuid.NewString(), Name: "Tenant"}}, nil
}

type fakeSessionStore struct {
	sessions map[string]uuid.UUID
}

func (s *fakeSessionStore) CreateSession(_ context.Context, userID uuid.UUID, _ string, _ time.Duration) (string, error) {
	sessionID := "session-" + userID.String()
	s.sessions[sessionID] = userID
	return sessionID, nil
}
func (s *fakeSessionStore) ResolveSession(_ context.Context, sessionID string) (uuid.UUID, error) {
	userID, ok := s.sessions[sessionID]
	if !ok {
		return uuid.Nil, context.Canceled
	}
	return userID, nil
}
func (s *fakeSessionStore) RevokeSession(_ context.Context, sessionID string) error {
	delete(s.sessions, sessionID)
	return nil
}
func (s *fakeSessionStore) UpdateActivity(context.Context, string) error {
	return nil
}

func jwkInt(v *big.Int) string { return base64.RawURLEncoding.EncodeToString(v.Bytes()) }

func TestOIDCAuthorizationCodePKCEAndCookie(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	resolver := fakeOIDCResolver{userID: uuid.New()}
	var issuer, expectedNonce string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(OIDCDiscovery{AuthorizationEndpoint: issuer + "/authorize", TokenEndpoint: issuer + "/token", JWKSURI: issuer + "/jwks"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
				"kty": "RSA", "kid": "key-1", "alg": "RS256", "n": jwkInt(key.N), "e": jwkInt(big.NewInt(int64(key.E))),
			}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code_verifier") == "" || r.Form.Get("client_secret") != "secret" {
				t.Fatalf("invalid token exchange: %v", r.Form)
			}
			claims := oidcClaims{Nonce: expectedNonce, RegisteredClaims: jwt.RegisteredClaims{
				Issuer: issuer, Subject: "idp-user-1", Audience: []string{"omnira-web"},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now()),
			}}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "key-1"
			raw, _ := token.SignedString(key)
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	auth, discovery, err := NewOIDCAuthenticator(context.Background(), issuer, "omnira-web", idp.Client(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := &fakeSessionStore{sessions: map[string]uuid.UUID{}}
	h := NewOIDCHandler(auth, discovery, resolver, sessionStore, issuer, "client", "secret", "https://app.example/api/v1/auth/oidc/callback", "/login?oidc=complete", true)

	startReq := httptest.NewRequest(http.MethodGet, "https://app.example/api/v1/auth/oidc/start", nil)
	startRec := httptest.NewRecorder()
	h.Start(startRec, startReq)
	if startRec.Code != http.StatusFound {
		t.Fatalf("start=%d %q", startRec.Code, startRec.Body.String())
	}
	location, _ := url.Parse(startRec.Header().Get("Location"))
	if location.Query().Get("code_challenge_method") != "S256" || location.Query().Get("code_challenge") == "" || location.Query().Get("nonce") == "" {
		t.Fatalf("authorization URL lacks PKCE/nonce: %s", location.String())
	}
	cookies := startRec.Result().Cookies()
	byName := map[string]*http.Cookie{}
	for _, cookie := range cookies {
		byName[cookie.Name] = cookie
	}
	for _, name := range []string{oidcStateCookie, oidcVerifierCookie, oidcNonceCookie} {
		cookie := byName[name]
		if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("unsafe transient cookie %s: %#v", name, cookie)
		}
	}
	expectedNonce = byName[oidcNonceCookie].Value
	callbackReq := httptest.NewRequest(http.MethodGet, "https://app.example/api/v1/auth/oidc/callback?code=ok&state="+url.QueryEscape(byName[oidcStateCookie].Value), nil)
	for _, cookie := range cookies {
		callbackReq.AddCookie(cookie)
	}
	callbackRec := httptest.NewRecorder()
	h.Callback(callbackRec, callbackReq)
	if callbackRec.Code != http.StatusFound || callbackRec.Header().Get("Location") != "/login?oidc=complete" {
		t.Fatalf("callback=%d location=%q body=%q", callbackRec.Code, callbackRec.Header().Get("Location"), callbackRec.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range callbackRec.Result().Cookies() {
		if cookie.Name == SessionCookieName && cookie.MaxAge > 0 {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe session cookie: %#v", session)
	}
	// The session cookie carries an opaque server-side session id, not the ID
	// Token: it only identifies the user through the session store.
	userID, err := sessionStore.ResolveSession(context.Background(), session.Value)
	if err != nil || userID != resolver.userID {
		t.Fatalf("resolved session user=%v err=%v", userID, err)
	}
}

func TestOIDCCallbackRejectsStateMismatchBeforeTokenExchange(t *testing.T) {
	h := &OIDCHandler{secureCookie: true}
	req := httptest.NewRequest(http.MethodGet, "https://app.example/callback?code=x&state=attacker", strings.NewReader(""))
	req.AddCookie(h.cookie(oidcStateCookie, "expected", 600))
	req.AddCookie(h.cookie(oidcVerifierCookie, "verifier", 600))
	req.AddCookie(h.cookie(oidcNonceCookie, "nonce", 600))
	rec := httptest.NewRecorder()
	h.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
