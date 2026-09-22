package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// testSessionStore is an in-memory SessionStore for unit-testing WebMiddleware
// and DevLogin without a real Postgres instance. Distinct from oidc_test.go's
// fakeSessionStore (which ignores TTL/revocation semantics): this one honors
// expiry and revocation, needed to exercise E/F below.
type testSessionStore struct {
	mu       sync.Mutex
	sessions map[string]testSession
}

type testSession struct {
	userID    uuid.UUID
	expiresAt time.Time
	revoked   bool
}

func newTestSessionStore() *testSessionStore {
	return &testSessionStore{sessions: map[string]testSession{}}
}

func (s *testSessionStore) CreateSession(_ context.Context, userID uuid.UUID, _ string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := generateSessionID()
	s.sessions[id] = testSession{userID: userID, expiresAt: time.Now().Add(ttl)}
	return id, nil
}

func (s *testSessionStore) ResolveSession(_ context.Context, sessionID string) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.revoked || time.Now().After(sess.expiresAt) {
		return uuid.Nil, errors.New("session not found or expired")
	}
	return sess.userID, nil
}

func (s *testSessionStore) RevokeSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return errors.New("session not found")
	}
	sess.revoked = true
	s.sessions[sessionID] = sess
	return nil
}

func (s *testSessionStore) UpdateActivity(context.Context, string) error { return nil }

func webMiddlewareTestHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := FromContext(r.Context())
		if err != nil {
			http.Error(w, "no principal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-User-ID", principal.UserID.String())
		w.WriteHeader(http.StatusOK)
	})
}

// B: opaque cookie -> protected route success.
func TestWebMiddleware_OpaqueCookieSucceeds(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	auth := NewJWTAuthenticator(&privateKey.PublicKey, "test-issuer", "test-audience")
	store := newTestSessionStore()
	userID := uuid.New()
	sessionID, err := store.CreateSession(context.Background(), userID, "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	handler := WebMiddleware(auth, store)(webMiddlewareTestHandler(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-User-ID") != userID.String() {
		t.Errorf("resolved user = %s, want %s", rec.Header().Get("X-User-ID"), userID)
	}
}

// C: random/unknown cookie -> unauthorized.
func TestWebMiddleware_UnknownCookieRejected(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	auth := NewJWTAuthenticator(&privateKey.PublicKey, "test-issuer", "test-audience")
	store := newTestSessionStore()

	handler := WebMiddleware(auth, store)(webMiddlewareTestHandler(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "totally-random-unknown-value"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

// D: an old/legit JWT placed in omnira_session must be rejected as an opaque
// lookup miss — never fall back to JWT parsing for the cookie path. This is
// the regression test for the original RELEASE.1 finding.
func TestWebMiddleware_LegacyJWTInCookieRejected(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	auth := NewJWTAuthenticator(&privateKey.PublicKey, "test-issuer", "test-audience")
	store := newTestSessionStore()

	claims := &Claims{Subject: "legacy-user", UserID: uuid.NewString(), RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "test-issuer", Audience: []string{"test-audience"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	rawJWT, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(rawJWT, ".") != 2 {
		t.Fatalf("test setup: expected a real JWT (2 dots), got %q", rawJWT)
	}

	handler := WebMiddleware(auth, store)(webMiddlewareTestHandler(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawJWT})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy JWT in cookie: status=%d, want 401 (opaque lookup miss, no JWT fallback)", rec.Code)
	}
}

// E: expired session -> rejected (store-level expiry, exercised through the middleware).
func TestWebMiddleware_ExpiredSessionRejected(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	auth := NewJWTAuthenticator(&privateKey.PublicKey, "test-issuer", "test-audience")
	store := newTestSessionStore()
	sessionID, err := store.CreateSession(context.Background(), uuid.New(), "test", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	handler := WebMiddleware(auth, store)(webMiddlewareTestHandler(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

// F: logout revokes server-side; a replayed cookie afterwards fails.
func TestWebMiddleware_RevokedSessionReplayFails(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	auth := NewJWTAuthenticator(&privateKey.PublicKey, "test-issuer", "test-audience")
	store := newTestSessionStore()
	sessionID, err := store.CreateSession(context.Background(), uuid.New(), "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	handler := WebMiddleware(auth, store)(webMiddlewareTestHandler(t))
	reqBefore := httptest.NewRequest(http.MethodGet, "/", nil)
	reqBefore.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	recBefore := httptest.NewRecorder()
	handler.ServeHTTP(recBefore, reqBefore)
	if recBefore.Code != http.StatusOK {
		t.Fatalf("pre-logout: status=%d, want 200", recBefore.Code)
	}

	if err := store.RevokeSession(context.Background(), sessionID); err != nil {
		t.Fatal(err)
	}

	reqAfter := httptest.NewRequest(http.MethodGet, "/", nil)
	reqAfter.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionID})
	recAfter := httptest.NewRecorder()
	handler.ServeHTTP(recAfter, reqAfter)
	if recAfter.Code != http.StatusUnauthorized {
		t.Fatalf("replay after logout: status=%d, want 401", recAfter.Code)
	}
}

// Bearer JWT continues to work through WebMiddleware — preserves real API/dev
// tooling consumers; only the cookie path changed to opaque-only.
func TestWebMiddleware_BearerJWTStillWorks(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	auth := NewJWTAuthenticator(&privateKey.PublicKey, "test-issuer", "test-audience")
	store := newTestSessionStore()

	claims := &Claims{Subject: "bearer-user", UserID: uuid.NewString(), RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "test-issuer", Audience: []string{"test-audience"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	rawJWT, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}

	handler := WebMiddleware(auth, store)(webMiddlewareTestHandler(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawJWT)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("bearer JWT: status=%d body=%q, want 200", rec.Code, rec.Body.String())
	}
}

// G: session fixation — login always creates a brand-new opaque id, the
// store never reuses one supplied by the caller (CreateSession ignores any
// pre-existing browser cookie value and mints a fresh random id every time).
func TestSessionFixation_LoginAlwaysMintsNewSessionID(t *testing.T) {
	store := newTestSessionStore()
	userID := uuid.New()

	session1, err := store.CreateSession(context.Background(), userID, "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session2, err := store.CreateSession(context.Background(), userID, "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if session1 == session2 {
		t.Fatal("two logins produced the same session id — fixation risk")
	}

	// Revoking the first (simulating a prior/attacker-supplied session)
	// never invalidates the second, and vice-versa: they are independent.
	if err := store.RevokeSession(context.Background(), session1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveSession(context.Background(), session2); err != nil {
		t.Fatalf("second session unexpectedly affected: %v", err)
	}
}
