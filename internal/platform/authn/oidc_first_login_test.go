package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// firstLoginResolver simulates a user who has never logged in before:
// ResolveUserID always fails ("identity not provisioned"); only
// ProvisionIdentity succeeds and returns the authoritative user id. This is
// the PILOT.1 P0 regression scenario: VerifyIDTokenWithClaims used to call
// ResolveUserID internally and fail before the callback ever reached
// ProvisionIdentity, permanently blocking anyone's first login.
type firstLoginResolver struct {
	provisionedUserID uuid.UUID
	resolveCalls      int
	provisionCalls    int
}

func (r *firstLoginResolver) ResolveUserID(context.Context, string, string) (uuid.UUID, error) {
	r.resolveCalls++
	return uuid.Nil, errors.New("identity not provisioned")
}

func (r *firstLoginResolver) ResolveIdentity(context.Context, string, string) (uuid.UUID, error) {
	return uuid.Nil, errors.New("not used in this test")
}

func (r *firstLoginResolver) ProvisionIdentity(context.Context, string, string, string, string, bool) (uuid.UUID, error) {
	r.provisionCalls++
	return r.provisionedUserID, nil
}

func (r *firstLoginResolver) SessionProfile(context.Context, uuid.UUID) (SessionProfile, error) {
	return SessionProfile{}, nil
}

// TestOIDCCallbackProvisionsFirstLoginWithoutRequiringExistingIdentity fails
// against the previous broken behavior (ResolveUserID gating the ID token
// verification) and passes now that verification is independent of
// identity resolution: a brand new identity must reach ProvisionIdentity and
// get a real, resolvable server-side session for the user id it returns.
func TestOIDCCallbackProvisionsFirstLoginWithoutRequiringExistingIdentity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &firstLoginResolver{provisionedUserID: uuid.New()}
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
			claims := oidcClaims{Nonce: expectedNonce, RegisteredClaims: jwt.RegisteredClaims{
				Issuer: issuer, Subject: "brand-new-idp-user", Audience: []string{"omnira-web"},
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
	cookies := startRec.Result().Cookies()
	byName := map[string]*http.Cookie{}
	for _, cookie := range cookies {
		byName[cookie.Name] = cookie
	}
	expectedNonce = byName[oidcNonceCookie].Value

	callbackReq := httptest.NewRequest(http.MethodGet, "https://app.example/api/v1/auth/oidc/callback?code=ok&state="+url.QueryEscape(byName[oidcStateCookie].Value), nil)
	for _, cookie := range cookies {
		callbackReq.AddCookie(cookie)
	}
	callbackRec := httptest.NewRecorder()
	h.Callback(callbackRec, callbackReq)

	if callbackRec.Code != http.StatusFound {
		t.Fatalf("first login must succeed even though ResolveUserID never finds the identity: status=%d body=%q", callbackRec.Code, callbackRec.Body.String())
	}
	if resolver.provisionCalls != 1 {
		t.Fatalf("ProvisionIdentity calls = %d, want 1", resolver.provisionCalls)
	}
	var session *http.Cookie
	for _, cookie := range callbackRec.Result().Cookies() {
		if cookie.Name == SessionCookieName && cookie.MaxAge > 0 {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("no session cookie set on first login")
	}
	userID, err := sessionStore.ResolveSession(context.Background(), session.Value)
	if err != nil || userID != resolver.provisionedUserID {
		t.Fatalf("session must be created for the ProvisionIdentity-returned user id: got=%v err=%v want=%v", userID, err, resolver.provisionedUserID)
	}
}
