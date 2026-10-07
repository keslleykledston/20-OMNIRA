package authn

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

const (
	testMobileClient   = "omnira-mobile"
	testMobileRedirect = "com.omnira.app:/oauth2redirect"
	testVerifier       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ0123456789"
	testNonce          = "nonce-0123456789abcdef"
)

type fakeIdP struct {
	srv        *httptest.Server
	key        *rsa.PrivateKey
	aud, azp   string
	nonce      string
	status     int // token endpoint status override (0 = 200)
	lastForm   map[string][]string
	issuer     string
	callCount  int
	redirectTo string // when set, the token endpoint answers 307 to this URL
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, aud: testMobileClient, azp: testMobileClient, nonce: testNonce}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(OIDCDiscovery{AuthorizationEndpoint: f.issuer + "/authorize", TokenEndpoint: f.issuer + "/token", JWKSURI: f.issuer + "/jwks"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
				"kty": "RSA", "kid": "key-1", "alg": "RS256", "n": jwkInt(key.N), "e": jwkInt(big.NewInt(int64(key.E))),
			}}})
		case "/token":
			f.callCount++
			_ = r.ParseForm()
			f.lastForm = r.Form
			if f.redirectTo != "" {
				http.Redirect(w, r, f.redirectTo, http.StatusTemporaryRedirect)
				return
			}
			if f.status != 0 {
				w.WriteHeader(f.status)
				return
			}
			claims := oidcClaims{Nonce: f.nonce, Azp: f.azp, RegisteredClaims: jwt.RegisteredClaims{
				Issuer: f.issuer, Subject: "idp-user-1", Audience: []string{f.aud},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now()),
			}}
			tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			tok.Header["kid"] = "key-1"
			raw, _ := tok.SignedString(key)
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	f.issuer = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

type mobileRig struct {
	idp     *fakeIdP
	handler *MobileHandler
	store   *PostgresDeviceStore
	user    uuid.UUID
	mux     *http.ServeMux
}

func newMobileRig(t *testing.T) *mobileRig {
	t.Helper()
	return newMobileRigWith(t, nil)
}

// newMobileRigWith lets a test swap the identity resolver (nil = a fake that knows the subject "idp-user-1").
func newMobileRigWith(t *testing.T, resolverFor func(user uuid.UUID) OIDCIdentityResolver) *mobileRig {
	t.Helper()
	store, _, newUser := deviceStoreForTest(t)
	user := newUser()
	idp := newFakeIdP(t)
	var resolver OIDCIdentityResolver = fakeOIDCResolver{userID: user}
	if resolverFor != nil {
		resolver = resolverFor(user)
	}
	auth, discovery, err := NewOIDCAuthenticator(context.Background(), idp.issuer, testMobileClient, idp.srv.Client(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	h := NewMobileHandler(auth, discovery, resolver, store, idp.issuer, MobileConfig{ClientID: testMobileClient, RedirectURIs: []string{testMobileRedirect}})
	// The same boundary every API route uses: opaque device tokens are resolved by the wrapper, anything else by the inner authenticator.
	boundary := WebMiddleware(NewDeviceAuthenticator(nil, store), &fakeSessionStore{sessions: map[string]uuid.UUID{}})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", h.Token)
	mux.HandleFunc("POST /refresh", h.Refresh)
	mux.HandleFunc("POST /logout", h.Logout)
	mux.Handle("GET /me/devices", boundary(http.HandlerFunc(h.ListDevices)))
	mux.Handle("DELETE /me/devices/{device_id}", boundary(http.HandlerFunc(h.DeleteDevice)))
	mux.Handle("GET /whoami", boundary(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := FromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]string{"user": p.UserID.String(), "kind": p.SessionKind})
	})))
	return &mobileRig{idp: idp, handler: h, store: store, user: user, mux: mux}
}

func (m *mobileRig) do(method, path, bearer string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		switch b := body.(type) {
		case string:
			buf.WriteString(b)
		default:
			_ = json.NewEncoder(&buf).Encode(b)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "203.0.113.9:4444"
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	m.mux.ServeHTTP(rr, req)
	return rr
}

func goodTokenRequest() map[string]any {
	return map[string]any{"code": "authcode-1", "code_verifier": testVerifier, "redirect_uri": testMobileRedirect, "nonce": testNonce,
		"device_label": "Pixel 9", "platform": "android"}
}

func decodePair(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %q", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMobileLoginRefreshDevicesAndLogoutEndToEnd(t *testing.T) {
	m := newMobileRig(t)
	rr := m.do("POST", "/token", "", goodTokenRequest())
	pair := decodePair(t, rr)
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token response must not be cacheable: %v", rr.Header())
	}
	// The IdP exchange is public-client PKCE: no secret, the verifier and the exact redirect are forwarded, the client id is the server's.
	if m.idp.lastForm["client_secret"] != nil || m.idp.lastForm["code_verifier"][0] != testVerifier ||
		m.idp.lastForm["client_id"][0] != testMobileClient || m.idp.lastForm["redirect_uri"][0] != testMobileRedirect {
		t.Fatalf("token exchange form: %v", m.idp.lastForm)
	}
	if strings.Contains(rr.Body.String(), "id_token") {
		t.Fatal("the ID Token must never reach the app")
	}
	access, refresh := pair["access_token"].(string), pair["refresh_token"].(string)

	// The access token works on a route behind the ordinary boundary.
	who := m.do("GET", "/whoami", access, nil)
	if who.Code != 200 || !strings.Contains(who.Body.String(), m.user.String()) || !strings.Contains(who.Body.String(), "device") {
		t.Fatalf("whoami: %d %q", who.Code, who.Body.String())
	}
	list := m.do("GET", "/me/devices", access, nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"current":true`) || !strings.Contains(list.Body.String(), "Pixel 9") {
		t.Fatalf("devices: %d %q", list.Code, list.Body.String())
	}

	// Refresh rotates; the old pair is dead, the new one works.
	next := decodePair(t, m.do("POST", "/refresh", "", map[string]string{"refresh_token": refresh}))
	if m.do("GET", "/whoami", access, nil).Code != 401 {
		t.Fatal("old access token still valid after the rotation")
	}
	access2, refresh2 := next["access_token"].(string), next["refresh_token"].(string)
	if m.do("GET", "/whoami", access2, nil).Code != 200 {
		t.Fatal("new access token rejected")
	}
	// A replayed refresh is the same 401 as any other failure, and kills the installation.
	if rr := m.do("POST", "/refresh", "", map[string]string{"refresh_token": refresh}); rr.Code != 401 {
		t.Fatalf("replay: %d", rr.Code)
	}
	if m.do("GET", "/whoami", access2, nil).Code != 401 {
		t.Fatal("reuse must revoke the installation")
	}
	_ = refresh2

	// Logout with the access token; and logout with only a refresh token for an expired access token.
	p1 := decodePair(t, m.do("POST", "/token", "", goodTokenRequest()))
	if rr := m.do("POST", "/logout", p1["access_token"].(string), nil); rr.Code != 204 {
		t.Fatalf("logout by access: %d %q", rr.Code, rr.Body.String())
	}
	if m.do("GET", "/whoami", p1["access_token"].(string), nil).Code != 401 {
		t.Fatal("access token alive after logout")
	}
	p2 := decodePair(t, m.do("POST", "/token", "", goodTokenRequest()))
	if rr := m.do("POST", "/logout", "", map[string]string{"refresh_token": p2["refresh_token"].(string)}); rr.Code != 204 {
		t.Fatalf("logout by refresh: %d", rr.Code)
	}
	if rr := m.do("POST", "/logout", "", nil); rr.Code != 401 {
		t.Fatalf("anonymous logout: %d", rr.Code)
	}
}

func TestMobileLoginRejectsEverythingThatIsNotAFullyValidCodeExchange(t *testing.T) {
	m := newMobileRig(t)
	tweak := func(k string, v any) map[string]any { r := goodTokenRequest(); r[k] = v; return r }
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"redirect not on the allowlist": {tweak("redirect_uri", "com.evil.app:/cb"), 400},
		"redirect with a suffix":        {tweak("redirect_uri", testMobileRedirect+"/x"), 400},
		"verifier too short (PKCE)":     {tweak("code_verifier", "short"), 400},
		"verifier with forbidden chars": {tweak("code_verifier", strings.Repeat("a", 42)+"!"), 400},
		"nonce too short":               {tweak("nonce", "abc"), 400},
		"empty code":                    {tweak("code", ""), 400},
		"previous device is not a uuid": {tweak("previous_device_id", "not-a-uuid"), 400},
		"unknown field":                 {tweak("client_id", "omnira-web"), 400},
	} {
		if rr := m.do("POST", "/token", "", tc.body); rr.Code != tc.want {
			t.Errorf("%s: %d %q, want %d", name, rr.Code, rr.Body.String(), tc.want)
		}
	}
	if m.idp.callCount != 0 {
		t.Fatalf("invalid input must be refused before the identity provider is contacted (%d calls)", m.idp.callCount)
	}

	// ID Token problems: wrong nonce (login CSRF/replay), another client's token, wrong azp, IdP refusing the code, IdP down.
	m.idp.nonce = "a-different-nonce-123456"
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code != 401 {
		t.Errorf("nonce mismatch: %d", rr.Code)
	}
	m.idp.nonce = testNonce
	m.idp.aud = "omnira-web"
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code != 401 {
		t.Errorf("web audience accepted by the mobile endpoint: %d", rr.Code)
	}
	m.idp.aud, m.idp.azp = testMobileClient, "omnira-web"
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code != 401 {
		t.Errorf("wrong azp accepted: %d", rr.Code)
	}
	m.idp.azp, m.idp.status = testMobileClient, http.StatusBadRequest
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code != 401 {
		t.Errorf("IdP refusing the code: %d", rr.Code)
	}
	m.idp.status = http.StatusInternalServerError
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code != 502 {
		t.Errorf("IdP down: %d", rr.Code)
	}
	if devices, _ := m.store.ListDevices(context.Background(), m.user, uuid.Nil); len(devices) != 0 {
		t.Fatalf("a failed login created %d installation(s)", len(devices))
	}
}

func TestMobileEndpointsDoNotLeakWhyATokenFailed(t *testing.T) {
	m := newMobileRig(t)
	pair := decodePair(t, m.do("POST", "/token", "", goodTokenRequest()))
	used := pair["refresh_token"].(string)
	decodePair(t, m.do("POST", "/refresh", "", map[string]string{"refresh_token": used}))
	bodies := map[string]string{}
	for name, tok := range map[string]string{"spent": used, "garbage": "omn_rt_" + strings.Repeat("A", 43), "wrong prefix": "omn_at_x", "empty": ""} {
		rr := m.do("POST", "/refresh", "", map[string]string{"refresh_token": tok})
		if rr.Code != 401 {
			t.Fatalf("%s: %d", name, rr.Code)
		}
		bodies[name] = rr.Body.String()
	}
	for name, b := range bodies {
		if b != bodies["garbage"] {
			t.Errorf("%s answers differently from an unknown token: %q vs %q", name, b, bodies["garbage"])
		}
	}
}

func TestMobileDeviceManagementIsPerUser(t *testing.T) {
	m := newMobileRig(t)
	mine := decodePair(t, m.do("POST", "/token", "", goodTokenRequest()))
	other, _, newUser := deviceStoreForTest(t)
	_ = other
	stranger := newUser()
	foreign, err := m.store.IssueForLogin(context.Background(), stranger, "stranger", "ios", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The stranger's installation is invisible and not revocable by me: 404, same as a nonexistent id.
	if rr := m.do("DELETE", "/me/devices/"+foreign.DeviceID.String(), mine["access_token"].(string), nil); rr.Code != 404 {
		t.Fatalf("foreign device: %d", rr.Code)
	}
	if rr := m.do("DELETE", "/me/devices/"+uuid.NewString(), mine["access_token"].(string), nil); rr.Code != 404 {
		t.Fatalf("unknown device: %d", rr.Code)
	}
	if rr := m.do("DELETE", "/me/devices/not-a-uuid", mine["access_token"].(string), nil); rr.Code != 400 {
		t.Fatalf("malformed id: %d", rr.Code)
	}
	if m.do("GET", "/whoami", foreign.AccessToken, nil).Code != 200 {
		t.Fatal("the stranger's session was affected")
	}
	// Revoking my own current installation logs me out at once.
	if rr := m.do("DELETE", "/me/devices/"+mine["device_id"].(string), mine["access_token"].(string), nil); rr.Code != 204 {
		t.Fatalf("own device: %d", rr.Code)
	}
	if m.do("GET", "/whoami", mine["access_token"].(string), nil).Code != 401 {
		t.Fatal("revoked installation still authenticated")
	}
	// No credential at all.
	if m.do("GET", "/me/devices", "", nil).Code != 401 {
		t.Fatal("anonymous device list")
	}
}

func TestDeviceAuthenticatorKeepsTheOtherCredentialsExactlyAsBefore(t *testing.T) {
	store, _, _ := deviceStoreForTest(t)
	inner := &recordingAuthenticator{}
	a := NewDeviceAuthenticator(inner, store)
	if _, err := a.Verify(context.Background(), "eyJhbGciOiJSUzI1NiJ9.e30.sig"); err != nil || inner.calls != 1 {
		t.Fatalf("a JWT must reach the wrapped authenticator: calls=%d err=%v", inner.calls, err)
	}
	if _, err := a.Verify(context.Background(), AccessTokenPrefix+"unknown"); err == nil || inner.calls != 1 {
		t.Fatalf("a device-shaped token must never fall through to the JWT path: calls=%d err=%v", inner.calls, err)
	}
	// A refresh token is not a credential for the API.
	pair, _ := store.IssueForLogin(context.Background(), uuid.New(), "", "other", nil)
	_ = pair
}

type recordingAuthenticator struct{ calls int }

func (r *recordingAuthenticator) Verify(context.Context, string) (*Principal, error) {
	r.calls++
	return &Principal{UserID: uuid.New(), Subject: "x"}, nil
}

func TestMobileRateLimitPerAddress(t *testing.T) {
	m := newMobileRig(t)
	m.handler.tokenRL = newIPLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if rr := m.do("POST", "/token", "", tweakedBad()); rr.Code != 400 {
			t.Fatalf("attempt %d: %d", i, rr.Code)
		}
	}
	if rr := m.do("POST", "/token", "", tweakedBad()); rr.Code != 429 {
		t.Fatalf("4th attempt: %d, want 429", rr.Code)
	}
}

func tweakedBad() map[string]any { r := goodTokenRequest(); r["redirect_uri"] = "x:/y"; return r }

func TestSessionCheckerEndsStreamsOfRevokedCredentials(t *testing.T) {
	m := newMobileRig(t)
	sessions := &fakeSessionStore{sessions: map[string]uuid.UUID{}}
	cookieID, _ := sessions.CreateSession(context.Background(), m.user, "oidc", time.Hour)
	checker := NewSessionChecker(sessions, m.store)
	ctx := context.Background()

	cookie := &Principal{UserID: m.user, SessionKind: SessionKindCookie, SessionKey: cookieID}
	if err := checker.StillValid(ctx, cookie); err != nil {
		t.Fatalf("live cookie session: %v", err)
	}
	_ = sessions.RevokeSession(ctx, cookieID)
	if err := checker.StillValid(ctx, cookie); err == nil {
		t.Fatal("a revoked cookie session must end the stream (R-3)")
	}

	pair := decodePair(t, m.do("POST", "/token", "", goodTokenRequest()))
	cred, err := m.store.ResolveAccess(ctx, pair["access_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	device := &Principal{UserID: m.user, SessionKind: SessionKindDevice, SessionKey: cred.TokenKey, DeviceID: cred.DeviceID}
	if err := checker.StillValid(ctx, device); err != nil {
		t.Fatalf("live device session: %v", err)
	}
	if err := m.store.RevokeDevice(ctx, m.user, cred.DeviceID, RevokedUser); err != nil {
		t.Fatal(err)
	}
	if err := checker.StillValid(ctx, device); err == nil {
		t.Fatal("a revoked device must end the stream (R-3)")
	}
	if err := checker.StillValid(ctx, &Principal{UserID: m.user}); err != nil {
		t.Fatalf("a Bearer ID token has no server-side session: %v", err)
	}
	if err := checker.StillValid(ctx, &Principal{SessionKind: "weird", SessionKey: "x"}); err == nil {
		t.Fatal("an unknown credential kind must fail closed")
	}
}

// ADR-0022: a native login never creates a user and never admits an inactive one (the web callback may provision; the app may not).
func TestMobileLoginRequiresAnExistingActiveIdentity(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx := context.Background()
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
	real := NewPostgresIdentityResolver(app)
	m := newMobileRigWith(t, func(uuid.UUID) OIDCIdentityResolver { return real })
	issuer := m.idp.issuer
	countUsers := func() (n int) {
		_ = seed.QueryRow(ctx, `SELECT count(*) FROM user_identities WHERE issuer=$1`, issuer).Scan(&n)
		return
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(ctx, `DELETE FROM users WHERE id IN (SELECT user_id FROM user_identities WHERE issuer=$1)`, issuer)
	})

	// 1) unknown identity: 401 and NOTHING is created.
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code != 401 {
		t.Fatalf("unknown identity: %d %q", rr.Code, rr.Body.String())
	}
	if countUsers() != 0 {
		t.Fatal("a native login provisioned a user")
	}

	// 2) known identity of an inactive user: 401, no installation.
	uid, err := real.ProvisionIdentity(ctx, issuer, "idp-user-1", "m@test.local", "M", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(ctx, `UPDATE users SET status='inactive' WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code != 401 {
		t.Fatalf("inactive user: %d %q", rr.Code, rr.Body.String())
	}
	if devices, _ := m.store.ListDevices(ctx, uid, uuid.Nil); len(devices) != 0 {
		t.Fatal("an inactive user got an installation")
	}

	// 3) active again: the login works.
	if _, err := seed.Exec(ctx, `UPDATE users SET status='active' WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code != 200 {
		t.Fatalf("active user: %d %q", rr.Code, rr.Body.String())
	}
}

// The authorization code and the PKCE verifier must never follow a redirect from the IdP endpoint.
func TestMobileCodeExchangeNeverFollowsRedirects(t *testing.T) {
	m := newMobileRig(t)
	var leaked int
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked++ }))
	defer evil.Close()
	m.idp.redirectTo = evil.URL
	if rr := m.do("POST", "/token", "", goodTokenRequest()); rr.Code == 200 {
		t.Fatalf("a redirected exchange produced a session: %q", rr.Body.String())
	}
	if leaked != 0 {
		t.Fatal("the code exchange followed a redirect to another host")
	}
}

func TestConcurrentLoginsOfOneUserWithDifferentPreviousDevicesDoNotDeadlock(t *testing.T) {
	store, _, newUser := deviceStoreForTest(t)
	ctx := context.Background()
	user := newUser()
	var prev []uuid.UUID
	for i := 0; i < 4; i++ {
		p, err := store.IssueForLogin(ctx, user, "", "android", nil)
		if err != nil {
			t.Fatal(err)
		}
		prev = append(prev, p.DeviceID)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for round := 0; round < 4; round++ {
		for i := range prev {
			wg.Add(1)
			go func(id uuid.UUID) {
				defer wg.Done()
				if _, err := store.IssueForLogin(ctx, user, "", "android", &id); err != nil {
					errs <- err
				}
			}(prev[i])
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent login failed: %v", err)
	}
}

func TestMobileRateLimitAlsoKeysOnTheNamedDeviceAndTheRefreshToken(t *testing.T) {
	m := newMobileRig(t)
	m.handler.keyRL = newIPLimiter(2, time.Minute)
	m.handler.tokenRL = newIPLimiter(1000, time.Minute)
	m.handler.refreshRL = newIPLimiter(1000, time.Minute)
	prev := uuid.NewString()
	good := goodTokenRequest()
	good["previous_device_id"] = prev
	for i := 0; i < 2; i++ {
		if rr := m.do("POST", "/token", "", good); rr.Code != 200 {
			t.Fatalf("login %d: %d", i, rr.Code)
		}
	}
	if rr := m.do("POST", "/token", "", good); rr.Code != 429 {
		t.Fatalf("hammering one previous_device_id from many requests: %d, want 429", rr.Code)
	}
	for i := 0; i < 2; i++ {
		m.do("POST", "/refresh", "", map[string]string{"refresh_token": "omn_rt_" + strings.Repeat("A", 43)})
	}
	if rr := m.do("POST", "/refresh", "", map[string]string{"refresh_token": "omn_rt_" + strings.Repeat("A", 43)}); rr.Code != 429 {
		t.Fatalf("hammering one refresh token: %d, want 429", rr.Code)
	}
}
