package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	intelligenceapp "github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Platform-operator-only endpoint (same shape as groupsadapters.SLAAlertFeatureFlagHandler): never
// tenant-scoped, so this does NOT reuse call()/tenancydomain.WithTenantContext — only the user
// session a real request would carry (authnMiddleware + tenancyadapters.UserSessionMiddleware in
// RegisterContextIntelligenceV2FeatureFlagHandlers).
func contextFlagPlatformCall(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, method, body string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	err := platformdb.WithTenantSession(context.Background(), pool, user, false, func(sessionCtx context.Context) error {
		req := httptest.NewRequest(method, "/", strings.NewReader(body)).WithContext(authn.WithPrincipal(sessionCtx, &authn.Principal{UserID: user}))
		fn(rec, req)
		return nil
	})
	if err != nil {
		t.Fatalf("user session: %v", err)
	}
	return rec
}

func decodeContextFlagState(t *testing.T, rec *httptest.ResponseRecorder) intelligenceapp.ContextIntelligenceV2FeatureFlagState {
	t.Helper()
	var out intelligenceapp.ContextIntelligenceV2FeatureFlagState
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

// Anyone who is not an active platform operator gets the SAME 404 a nonexistent route would — no
// enumeration oracle, same convention as internal/platformrelease/updatemgr.
func TestContextIntelligenceV2FeatureFlagRequiresAnActivePlatformOperator(t *testing.T) {
	e := newEnv(t)
	h := NewContextIntelligenceV2FeatureFlagHandler(e.app)
	someone := e.user()

	if rec := contextFlagPlatformCall(t, e.app, someone, http.MethodGet, "", h.Get); rec.Code != http.StatusNotFound {
		t.Fatalf("GET as non-operator = %d, want 404", rec.Code)
	}
	if rec := contextFlagPlatformCall(t, e.app, someone, http.MethodPut, `{"enabled":true}`, h.Set); rec.Code != http.StatusNotFound {
		t.Fatalf("PUT as non-operator = %d, want 404", rec.Code)
	}
}

// Fresh install: off by default (migration 000204's seed row), exactly the
// OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED default this layers on top of.
func TestContextIntelligenceV2FeatureFlagDefaultsToDisabled(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE context_intelligence_v2_feature SET enabled = false, updated_by = NULL`)
	h := NewContextIntelligenceV2FeatureFlagHandler(e.app)
	op := e.platformOperator()

	rec := contextFlagPlatformCall(t, e.app, op, http.MethodGet, "", h.Get)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	if state := decodeContextFlagState(t, rec); state.Enabled {
		t.Fatalf("state = %+v, want disabled by default", state)
	}
}

// An operator can flip it on and off at runtime — the whole point of the admin-console switch —
// and the handler reflects the new state immediately after a successful PUT.
func TestContextIntelligenceV2FeatureFlagOperatorCanFlipIt(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE context_intelligence_v2_feature SET enabled = false, updated_by = NULL`)
	h := NewContextIntelligenceV2FeatureFlagHandler(e.app)
	op := e.platformOperator()

	rec := contextFlagPlatformCall(t, e.app, op, http.MethodPut, `{"enabled":true}`, h.Set)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if state := decodeContextFlagState(t, rec); !state.Enabled {
		t.Fatalf("state = %+v, want enabled after PUT", state)
	}

	rec = contextFlagPlatformCall(t, e.app, op, http.MethodGet, "", h.Get)
	if state := decodeContextFlagState(t, rec); !state.Enabled {
		t.Fatalf("GET after PUT: state = %+v, want still enabled", state)
	}
}
