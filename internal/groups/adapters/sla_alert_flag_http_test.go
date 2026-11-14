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

	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Platform-operator-only endpoint (ADR-0058 addendum): never tenant-scoped, so this does NOT reuse
// call()/tenancydomain.WithTenantContext — only the user session a real request would carry
// (authnMiddleware + tenancyadapters.UserSessionMiddleware in RegisterSLAAlertFeatureFlagHandlers).
func platformCall(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, method, body string, fn http.HandlerFunc) *httptest.ResponseRecorder {
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

func (e *env) user() uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	return u
}

func (e *env) platformOperator() uuid.UUID {
	u := e.user()
	e.exec(`INSERT INTO platform_operators(user_id, granted_by) VALUES($1, 'test')`, u)
	return u
}

func decodeFlagState(t *testing.T, rec *httptest.ResponseRecorder) SLAAlertFeatureFlagState {
	t.Helper()
	var out SLAAlertFeatureFlagState
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

// Anyone who is not an active platform operator gets the SAME 404 a nonexistent route would — no
// enumeration oracle, same convention as internal/platformrelease/updatemgr.
func TestSLAAlertFeatureFlagRequiresAnActivePlatformOperator(t *testing.T) {
	e := newEnv(t)
	h := NewSLAAlertFeatureFlagHandler(e.app)
	someone := e.user()

	if rec := platformCall(t, e.app, someone, http.MethodGet, "", h.Get); rec.Code != http.StatusNotFound {
		t.Fatalf("GET as non-operator = %d, want 404", rec.Code)
	}
	if rec := platformCall(t, e.app, someone, http.MethodPut, `{"enabled":true}`, h.Set); rec.Code != http.StatusNotFound {
		t.Fatalf("PUT as non-operator = %d, want 404", rec.Code)
	}
}

// Fresh install: off by default (migration 000184's seed row), exactly the OMNIRA_GROUP_SLA_ALERT_ENABLED
// default it replaces.
func TestSLAAlertFeatureFlagDefaultsToDisabled(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE group_sla_alert_feature SET enabled = false, updated_by = NULL`)
	h := NewSLAAlertFeatureFlagHandler(e.app)
	op := e.platformOperator()

	rec := platformCall(t, e.app, op, http.MethodGet, "", h.Get)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	if state := decodeFlagState(t, rec); state.Enabled {
		t.Fatalf("state = %+v, want disabled by default", state)
	}
}

// An operator can flip it on and off at runtime — the whole point of replacing the .env + restart
// switch with a button — and each flip is audited (platform-wide event, tenant_id NULL).
func TestSLAAlertFeatureFlagCanBeToggledByAnOperatorAndIsAudited(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE group_sla_alert_feature SET enabled = false, updated_by = NULL`)
	h := NewSLAAlertFeatureFlagHandler(e.app)
	op := e.platformOperator()

	rec := platformCall(t, e.app, op, http.MethodPut, `{"enabled":true}`, h.Set)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT enable = %d: %s", rec.Code, rec.Body.String())
	}
	state := decodeFlagState(t, rec)
	if !state.Enabled || state.UpdatedBy == nil || *state.UpdatedBy != op {
		t.Fatalf("state = %+v, want enabled by %s", state, op)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'group_sla_alert_feature.enabled' AND actor_id = $1 AND tenant_id IS NULL`, op); n != 1 {
		t.Fatalf("audit rows for enable = %d, want 1", n)
	}

	rec = platformCall(t, e.app, op, http.MethodPut, `{"enabled":false}`, h.Set)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT disable = %d: %s", rec.Code, rec.Body.String())
	}
	if state := decodeFlagState(t, rec); state.Enabled {
		t.Fatalf("state = %+v, want disabled after the second PUT", state)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'group_sla_alert_feature.disabled' AND actor_id = $1 AND tenant_id IS NULL`, op); n != 1 {
		t.Fatalf("audit rows for disable = %d, want 1", n)
	}

	// GET reflects the latest state for anyone who asks afterwards, operator or not the same one who flipped it.
	rec = platformCall(t, e.app, op, http.MethodGet, "", h.Get)
	if state := decodeFlagState(t, rec); state.Enabled {
		t.Fatalf("GET after disable = %+v, want disabled", state)
	}
}
