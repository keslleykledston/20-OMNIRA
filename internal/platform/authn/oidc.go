package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	oidcStateCookie    = "omnira_oidc_state"
	oidcVerifierCookie = "omnira_oidc_verifier"
	oidcNonceCookie    = "omnira_oidc_nonce"
	oidcReturnToCookie = "omnira_oidc_return_to"
)

// allowedOIDCReturnPath é a única forma de path que Start aceita como retorno
// pós-login além do padrão (h.postLoginURL). Sem allowlist, um return_to
// arbitrário na query vindo do navegador viraria open redirect — o servidor
// mandaria o usuário, já autenticado, para qualquer origem que o parâmetro
// apontasse.
var allowedOIDCReturnPath = regexp.MustCompile(`^/invite/[A-Za-z0-9_-]{16,}$`)

func sanitizeOIDCReturnTo(raw string) string {
	if allowedOIDCReturnPath.MatchString(raw) {
		return raw
	}
	return ""
}

type OIDCDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	EndSessionEndpoint    string `json:"end_session_endpoint,omitempty"`
}

type OIDCIdentityResolver interface {
	// ResolveUserID takes the validated issuer and the subject: an identity is
	// the pair, never the subject alone.
	ResolveUserID(ctx context.Context, issuer, subject string) (uuid.UUID, error)
	ResolveIdentity(context.Context, string, string) (uuid.UUID, error)
	ProvisionIdentity(context.Context, string, string, string, string) (uuid.UUID, error)
	SessionProfile(context.Context, uuid.UUID) (SessionProfile, error)
}

type SessionProfile struct {
	User   SessionUser   `json:"user"`
	Tenant SessionTenant `json:"tenant"`
}

type SessionUser struct {
	ID    string `json:"id"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
}

type SessionTenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type oidcClaims struct {
	Nonce       string `json:"nonce,omitempty"`
	Email       string `json:"email,omitempty"`
	Name        string `json:"name,omitempty"`
	GivenName   string `json:"given_name,omitempty"`
	FamilyName  string `json:"family_name,omitempty"`
	jwt.RegisteredClaims
}

type OIDCAuthenticator struct {
	issuer   string
	audience string
	jwksURI  string
	client   *http.Client
	resolver OIDCIdentityResolver
	mu       sync.RWMutex
	keys     map[string]*rsa.PublicKey
}

func NewOIDCAuthenticator(ctx context.Context, issuer, audience string, client *http.Client, resolver OIDCIdentityResolver) (*OIDCAuthenticator, OIDCDiscovery, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	issuer = strings.TrimRight(issuer, "/")
	var discovery OIDCDiscovery
	if err := getJSON(ctx, client, issuer+"/.well-known/openid-configuration", &discovery); err != nil {
		return nil, discovery, fmt.Errorf("oidc discovery: %w", err)
	}
	if discovery.AuthorizationEndpoint == "" || discovery.TokenEndpoint == "" || discovery.JWKSURI == "" {
		return nil, discovery, errors.New("oidc discovery is missing required endpoints")
	}
	a := &OIDCAuthenticator{issuer: issuer, audience: audience, jwksURI: discovery.JWKSURI, client: client, resolver: resolver, keys: map[string]*rsa.PublicKey{}}
	if err := a.refreshKeys(ctx); err != nil {
		return nil, discovery, err
	}
	return a, discovery, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target)
}

func (a *OIDCAuthenticator) refreshKeys(ctx context.Context) error {
	var set struct {
		Keys []struct {
			KTY string `json:"kty"`
			KID string `json:"kid"`
			ALG string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := getJSON(ctx, a.client, a.jwksURI, &set); err != nil {
		return fmt.Errorf("oidc jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, key := range set.Keys {
		if key.KTY != "RSA" || key.KID == "" || (key.ALG != "" && key.ALG != "RS256") {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
			continue
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		if e < 3 {
			continue
		}
		keys[key.KID] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: e}
	}
	if len(keys) == 0 {
		return errors.New("oidc jwks contains no usable RS256 keys")
	}
	a.mu.Lock()
	a.keys = keys
	a.mu.Unlock()
	return nil
}

func (a *OIDCAuthenticator) Verify(ctx context.Context, tokenString string) (*Principal, error) {
	principal, _, _, err := a.verify(ctx, tokenString)
	return principal, err
}

func (a *OIDCAuthenticator) VerifyIDToken(ctx context.Context, tokenString, expectedNonce string) (*Principal, time.Time, error) {
	principal, claims, expiry, err := a.verify(ctx, tokenString)
	if err != nil {
		return nil, time.Time{}, err
	}
	if expectedNonce == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) != 1 {
		return nil, time.Time{}, errors.New("oidc nonce mismatch")
	}
	return principal, expiry, nil
}

// VerifyIDTokenWithClaims verifies token and returns principal + claims (for JIT provisioning).
func (a *OIDCAuthenticator) VerifyIDTokenWithClaims(ctx context.Context, tokenString, expectedNonce string) (*Principal, *oidcClaims, time.Time, error) {
	principal, claims, expiry, err := a.verify(ctx, tokenString)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if expectedNonce == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) != 1 {
		return nil, nil, time.Time{}, errors.New("oidc nonce mismatch")
	}
	return principal, claims, expiry, nil
}

func (a *OIDCAuthenticator) verify(ctx context.Context, tokenString string) (*Principal, *oidcClaims, time.Time, error) {
	claims := &oidcClaims{}
	keyFunc := func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("oidc signing algorithm %q is not allowed", token.Method.Alg())
		}
		kid, _ := token.Header["kid"].(string)
		a.mu.RLock()
		key := a.keys[kid]
		a.mu.RUnlock()
		if key == nil {
			if err := a.refreshKeys(ctx); err != nil {
				return nil, err
			}
			a.mu.RLock()
			key = a.keys[kid]
			a.mu.RUnlock()
		}
		if key == nil {
			return nil, errors.New("oidc signing key not found")
		}
		return key, nil
	}
	token, err := jwt.ParseWithClaims(tokenString, claims, keyFunc,
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}), jwt.WithIssuer(a.issuer), jwt.WithAudience(a.audience), jwt.WithExpirationRequired())
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("invalid oidc token: %w", err)
	}
	if !token.Valid || claims.Subject == "" {
		return nil, nil, time.Time{}, errors.New("invalid oidc token")
	}
	// a.issuer, not a claim: the token was just validated against it, so the
	// client cannot steer which identity is resolved.
	userID, err := a.resolver.ResolveUserID(ctx, a.issuer, claims.Subject)
	if err != nil || userID == uuid.Nil {
		return nil, nil, time.Time{}, errors.New("oidc identity is not provisioned")
	}
	expiry := time.Time{}
	if claims.ExpiresAt != nil {
		expiry = claims.ExpiresAt.Time
	}
	return &Principal{UserID: userID, Subject: claims.Subject}, claims, expiry, nil
}

type OIDCHandler struct {
	auth         *OIDCAuthenticator
	discovery    OIDCDiscovery
	resolver     OIDCIdentityResolver
	sessionStore SessionStore
	issuer       string
	clientID     string
	clientSecret string
	redirectURL  string
	postLoginURL string
	secureCookie bool
	sessionTTL   time.Duration
}

func NewOIDCHandler(auth *OIDCAuthenticator, discovery OIDCDiscovery, resolver OIDCIdentityResolver, sessionStore SessionStore, issuer, clientID, clientSecret, redirectURL, postLoginURL string, secureCookie bool) *OIDCHandler {
	return &OIDCHandler{auth: auth, discovery: discovery, resolver: resolver, sessionStore: sessionStore, issuer: issuer, clientID: clientID, clientSecret: clientSecret,
		redirectURL: redirectURL, postLoginURL: postLoginURL, secureCookie: secureCookie, sessionTTL: 15 * time.Minute}
}

func randomURLSafe(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (h *OIDCHandler) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode}
}

func (h *OIDCHandler) Start(w http.ResponseWriter, r *http.Request) {
	state, err := randomURLSafe(32)
	if err != nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	verifier, err := randomURLSafe(48)
	if err != nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	nonce, err := randomURLSafe(32)
	if err != nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	for _, cookie := range []*http.Cookie{h.cookie(oidcStateCookie, state, 600), h.cookie(oidcVerifierCookie, verifier, 600), h.cookie(oidcNonceCookie, nonce, 600)} {
		http.SetCookie(w, cookie)
	}
	if returnTo := sanitizeOIDCReturnTo(r.URL.Query().Get("return_to")); returnTo != "" {
		http.SetCookie(w, h.cookie(oidcReturnToCookie, returnTo, 600))
	}
	params := url.Values{
		"response_type": {"code"}, "client_id": {h.clientID}, "redirect_uri": {h.redirectURL},
		"scope": {"openid email profile"}, "state": {state}, "nonce": {nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, h.discovery.AuthorizationEndpoint+"?"+params.Encode(), http.StatusFound)
}

func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	stateCookie, stateErr := r.Cookie(oidcStateCookie)
	verifierCookie, verifierErr := r.Cookie(oidcVerifierCookie)
	nonceCookie, nonceErr := r.Cookie(oidcNonceCookie)
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if stateErr != nil || verifierErr != nil || nonceErr != nil || code == "" || subtle.ConstantTimeCompare([]byte(state), []byte(stateCookie.Value)) != 1 {
		http.Error(w, "invalid oidc callback", http.StatusBadRequest)
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {h.redirectURL},
		"client_id": {h.clientID}, "client_secret": {h.clientSecret}, "code_verifier": {verifierCookie.Value}}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.auth.client.Do(req)
	if err != nil {
		http.Error(w, "identity provider unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	var tokens struct {
		IDToken string `json:"id_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokens) != nil || tokens.IDToken == "" {
		http.Error(w, "identity provider rejected the callback", http.StatusBadGateway)
		return
	}
	principal, claims, expiry, err := h.auth.VerifyIDTokenWithClaims(r.Context(), tokens.IDToken, nonceCookie.Value)
	if err != nil {
		http.Error(w, "invalid identity token", http.StatusUnauthorized)
		return
	}
	maxAge := int(time.Until(expiry).Seconds())
	if maxAge <= 0 {
		http.Error(w, "expired identity token", http.StatusUnauthorized)
		return
	}
	displayName := claims.Name
	if displayName == "" && claims.GivenName != "" {
		displayName = claims.GivenName
		if claims.FamilyName != "" {
			displayName += " " + claims.FamilyName
		}
	}
	_, err = h.resolver.ProvisionIdentity(r.Context(), h.issuer, principal.Subject, claims.Email, displayName)
	if err != nil {
		http.Error(w, fmt.Sprintf("identity provisioning error: %v", err), http.StatusInternalServerError)
		return
	}

	// Create server-side session (opaque session ID, not ID Token)
	sessionID, err := h.sessionStore.CreateSession(r.Context(), principal.UserID, "oidc", h.sessionTTL)
	if err != nil {
		http.Error(w, fmt.Sprintf("session creation error: %v", err), http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, h.cookie(SessionCookieName, sessionID, int(h.sessionTTL.Seconds())))
	// Revalidado aqui mesmo vindo de um cookie HttpOnly próprio: nunca confiar
	// cegamente num valor que atravessou um redirect externo (o IdP), mesmo
	// que o caminho normal não permita adulteração.
	redirectTo := h.postLoginURL
	if returnCookie, err := r.Cookie(oidcReturnToCookie); err == nil {
		if sanitized := sanitizeOIDCReturnTo(returnCookie.Value); sanitized != "" {
			redirectTo = sanitized
		}
	}
	for _, name := range []string{oidcStateCookie, oidcVerifierCookie, oidcNonceCookie, oidcReturnToCookie} {
		http.SetCookie(w, h.cookie(name, "", -1))
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func (h *OIDCHandler) Session(w http.ResponseWriter, r *http.Request) {
	principal, err := FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	profile, err := h.resolver.SessionProfile(r.Context(), principal.UserID)
	if err != nil {
		http.Error(w, "no active tenant membership", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(profile)
}

func (h *OIDCHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		_ = h.sessionStore.RevokeSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, h.cookie(SessionCookieName, "", -1))
	w.WriteHeader(http.StatusNoContent)
}
