package authn

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DeviceAuthenticator makes the existing web boundary (WebMiddleware, every authenticated route) accept the opaque access token of a
// native app installation, without touching a single route: a token with the device prefix is resolved server side on every request;
// anything else goes to the wrapped authenticator exactly as before.
type DeviceAuthenticator struct {
	inner   Authenticator
	devices DeviceStore
}

func NewDeviceAuthenticator(inner Authenticator, devices DeviceStore) *DeviceAuthenticator {
	return &DeviceAuthenticator{inner: inner, devices: devices}
}

func (a *DeviceAuthenticator) Verify(ctx context.Context, token string) (*Principal, error) {
	if strings.HasPrefix(token, AccessTokenPrefix) {
		cred, err := a.devices.ResolveAccess(ctx, token)
		if err != nil {
			return nil, ErrInvalidToken
		}
		return &Principal{UserID: cred.UserID, Subject: cred.UserID.String(), SessionKind: SessionKindDevice, SessionKey: cred.TokenKey, DeviceID: cred.DeviceID}, nil
	}
	if a.inner == nil {
		return nil, ErrInvalidToken
	}
	return a.inner.Verify(ctx, token)
}

// SessionValidator is the optional, side-effect-free check a store offers to long-lived streams.
type SessionValidator interface {
	ValidateSession(ctx context.Context, sessionID string) error
}

// ValidateSession reports whether a cookie session is still valid, WITHOUT touching last_activity_at (a stream must not keep a session alive).
func (s *PostgresSessionStore) ValidateSession(ctx context.Context, sessionID string) error {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM auth_sessions WHERE id=$1 AND revoked_at IS NULL AND expires_at > NOW()`, sessionID).Scan(&one)
	if err != nil {
		return errors.New("session not found or expired")
	}
	return nil
}

// SessionChecker answers "is the credential this principal authenticated with still valid?" (R-3: SSE must end when a session is
// revoked, not only when the membership is).
type SessionChecker interface {
	StillValid(ctx context.Context, p *Principal) error
}

type credentialChecker struct {
	sessions SessionStore
	devices  DeviceStore
}

func NewSessionChecker(sessions SessionStore, devices DeviceStore) SessionChecker {
	return credentialChecker{sessions: sessions, devices: devices}
}

func (c credentialChecker) StillValid(ctx context.Context, p *Principal) error {
	switch p.SessionKind {
	case "":
		return nil // a Bearer ID token has no server-side session to revoke
	case SessionKindCookie:
		if c.sessions == nil {
			return nil
		}
		if v, ok := c.sessions.(SessionValidator); ok {
			return v.ValidateSession(ctx, p.SessionKey)
		}
		_, err := c.sessions.ResolveSession(ctx, p.SessionKey)
		return err
	case SessionKindDevice:
		if c.devices == nil {
			return ErrInvalidToken
		}
		return c.devices.StillValid(ctx, p.SessionKey)
	}
	return ErrInvalidToken
}

// MobileConfig is the server-side allowlist of the native OIDC client (ADR-0022): nothing the app sends can widen it.
type MobileConfig struct {
	ClientID     string
	RedirectURIs []string
}

// MobileHandler serves the native credential endpoints. Plain values of tokens, codes and verifiers never reach a log.
type MobileHandler struct {
	auth      *OIDCAuthenticator // verifier whose audience is the mobile client
	discovery OIDCDiscovery
	resolver  OIDCIdentityResolver
	devices   DeviceStore
	cfg       MobileConfig
	issuer    string
	tokenRL   *ipLimiter
	refreshRL *ipLimiter
	// keyRL is a second, independent bucket keyed by something the caller names (previous_device_id at login, the refresh token's digest at
	// refresh). It is never trusted as authority; it only stops one identifier from being hammered from many addresses.
	keyRL *ipLimiter
}

func NewMobileHandler(auth *OIDCAuthenticator, discovery OIDCDiscovery, resolver OIDCIdentityResolver, devices DeviceStore, issuer string, cfg MobileConfig) *MobileHandler {
	return &MobileHandler{auth: auth, discovery: discovery, resolver: resolver, devices: devices, cfg: cfg, issuer: issuer,
		tokenRL: newIPLimiter(20, time.Minute), refreshRL: newIPLimiter(60, time.Minute), keyRL: newIPLimiter(30, time.Minute)}
}

var (
	pkceVerifierRE = regexp.MustCompile(`^[A-Za-z0-9\-._~]{43,128}$`)
	nonceRE        = regexp.MustCompile(`^[A-Za-z0-9\-._~]{16,128}$`)
	authCodeRE     = regexp.MustCompile(`^[\x21-\x7e]{1,1000}$`)
)

type mobileTokenRequest struct {
	Code             string `json:"code"`
	CodeVerifier     string `json:"code_verifier"`
	RedirectURI      string `json:"redirect_uri"`
	Nonce            string `json:"nonce"`
	DeviceLabel      string `json:"device_label"`
	Platform         string `json:"platform"`
	PreviousDeviceID string `json:"previous_device_id"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func (h *MobileHandler) redirectAllowed(uri string) bool {
	for _, allowed := range h.cfg.RedirectURIs {
		if subtle.ConstantTimeCompare([]byte(uri), []byte(allowed)) == 1 {
			return true
		}
	}
	return false
}

// Token exchanges an authorization code (obtained by the app with Code+PKCE in the system browser) for a device session. The server,
// not the app, talks to the IdP, validates the ID Token like the web callback does and never hands the ID Token to the app.
func (h *MobileHandler) Token(w http.ResponseWriter, r *http.Request) {
	if !h.tokenRL.allow(clientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var req mobileTokenRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !authCodeRE.MatchString(req.Code) || !pkceVerifierRE.MatchString(req.CodeVerifier) || !nonceRE.MatchString(req.Nonce) || !h.redirectAllowed(req.RedirectURI) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	var previous *uuid.UUID
	if req.PreviousDeviceID != "" {
		id, err := uuid.Parse(req.PreviousDeviceID)
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		previous = &id
		if !h.keyRL.allow("device:" + id.String()) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {req.Code}, "redirect_uri": {req.RedirectURI},
		"client_id": {h.cfg.ClientID}, "code_verifier": {req.CodeVerifier}}
	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	hreq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.idpClient().Do(hreq)
	if err != nil {
		http.Error(w, "identity provider unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	var tokens struct {
		IDToken string `json:"id_token"`
	}
	if resp.StatusCode >= 500 {
		http.Error(w, "identity provider unavailable", http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokens) != nil || tokens.IDToken == "" {
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	claims, _, err := h.auth.VerifyIDTokenWithClaims(r.Context(), tokens.IDToken, req.Nonce)
	if err != nil || claims.Azp != h.cfg.ClientID {
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	// ADR-0022: the identity must ALREADY exist and its user must be active (the web callback may provision a first login; a native app
	// never creates users). Unknown or inactive => the same generic 401, and no session is created.
	userID, err := h.resolver.ResolveIdentity(r.Context(), h.issuer, claims.Subject)
	if err != nil || userID == uuid.Nil {
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	pair, err := h.devices.IssueForLogin(r.Context(), userID, req.DeviceLabel, req.Platform, previous)
	if err != nil {
		log.Printf("mobile auth: could not issue a device session: %v", err)
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse(pair))
}

// idpClient is the IdP HTTP client for the code exchange: same transport and timeout as the verifier's, but it NEVER follows a redirect, so
// the authorization code and the PKCE verifier cannot be forwarded to another host by a (mis)configured or compromised endpoint.
func (h *MobileHandler) idpClient() *http.Client {
	c := *h.auth.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func tokenResponse(p TokenPair) map[string]any {
	return map[string]any{
		"token_type":         "Bearer",
		"access_token":       p.AccessToken,
		"access_expires_at":  p.AccessExpiresAt.UTC(),
		"expires_in":         int(time.Until(p.AccessExpiresAt).Seconds()),
		"refresh_token":      p.RefreshToken,
		"refresh_expires_at": p.RefreshExpiresAt.UTC(),
		"device_id":          p.DeviceID,
	}
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh rotates the refresh token. Every failure is the same 401: a client cannot tell expired, spent, revoked and unknown apart.
func (h *MobileHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if !h.refreshRL.allow(clientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var req refreshRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RefreshToken != "" && !h.keyRL.allow("refresh:"+digestHex(tokenDigest(req.RefreshToken))) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	pair, err := h.devices.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		if errors.Is(err, ErrRefreshReuse) {
			log.Printf("mobile auth: refresh token reuse detected, installation revoked")
		} else if !errors.Is(err, ErrInvalidToken) {
			log.Printf("mobile auth: refresh failed: %v", err)
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse(pair))
}

// Logout ends THIS installation immediately. The access token (Authorization: Bearer) is the normal proof; a refresh token in the body
// also works, for an app whose access token already expired.
func (h *MobileHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if !h.refreshRL.allow(clientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	if parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2); len(parts) == 2 && parts[0] == "Bearer" {
		if err := h.devices.RevokeByAccess(r.Context(), parts[1], RevokedLogout); err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if r.ContentLength != 0 {
		var req refreshRequest
		if !decodeBody(w, r, &req) {
			return // 400 already written
		}
		if req.RefreshToken != "" {
			if err := h.devices.RevokeByRefresh(r.Context(), req.RefreshToken, RevokedLogout); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	http.Error(w, "invalid or expired token", http.StatusUnauthorized)
}

// ListDevices lists the caller's own live installations (behind WebMiddleware).
func (h *MobileHandler) ListDevices(w http.ResponseWriter, r *http.Request) {
	p, err := FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	devices, err := h.devices.ListDevices(r.Context(), p.UserID, p.DeviceID)
	if err != nil {
		http.Error(w, "devices unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": devices})
}

// DeleteDevice revokes one of the caller's own installations (another user's id answers 404: no enumeration).
func (h *MobileHandler) DeleteDevice(w http.ResponseWriter, r *http.Request) {
	p, err := FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := uuid.Parse(r.PathValue("device_id"))
	if err != nil {
		http.Error(w, "invalid device id", http.StatusBadRequest)
		return
	}
	if err := h.devices.RevokeDevice(r.Context(), p.UserID, id, RevokedUser); err != nil {
		if errors.Is(err, ErrDeviceNotFound) {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		http.Error(w, "devices unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ipLimiter is a small fixed-window counter keyed by client address, for the pre-authentication endpoints (the global limiter cannot
// tell callers apart before a tenant is known, R-2).
type ipLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*ipWindow
}

type ipWindow struct {
	start time.Time
	n     int
}

func newIPLimiter(max int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: max, window: window, hits: map[string]*ipWindow{}}
}

func (l *ipLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10000 {
		for k, w := range l.hits {
			if now.Sub(w.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[key]
	if w == nil || now.Sub(w.start) > l.window {
		l.hits[key] = &ipWindow{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.max
}

// clientIP is the address the request came from. Behind the trusted web proxy (loopback or private peer) the first X-Forwarded-For hop
// is used; a direct public peer is never allowed to choose its own key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if xff := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(xff) != nil {
			return xff
		}
	}
	return host
}
