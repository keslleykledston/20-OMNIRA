package adapters_test

// Real PostgreSQL, through the real handler and the caller's own RLS session: the admin console's
// "am I a platform operator" probe (ADR-0038) must answer for an operator regardless of any OTHER
// platform feature's own state (found 2026-11-15: usePlatformOperator used to probe Update
// Manager's /platform/installation instead, so a real operator saw "Acesso restrito" on any host
// where OMNIRA_PLATFORM_RELEASE_API_ENABLED happened to be off).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

func operatorStatusCall(t *testing.T, w *world, user uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	h := adapters.NewOperatorStatusHandler(w.app)
	rec := httptest.NewRecorder()
	err := platformdb.WithTenantSession(context.Background(), w.app, user, false, func(sessionCtx context.Context) error {
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.WithValue(sessionCtx, authn.PrincipalKey, &authn.Principal{UserID: user}))
		h.Get(rec, req)
		return nil
	})
	w.must(err)
	return rec
}

// A platform operator gets a real answer, with no dependency on Hub membership, Update Manager, or
// any other platform feature's own enable flag.
func TestOperatorStatus_PlatformOperatorIsConfirmed(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op", false, true)

	rec := operatorStatusCall(t, w, op)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET as operator = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		IsOperator bool `json:"is_operator"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if !out.IsOperator {
		t.Fatalf("is_operator = false, want true")
	}
}

// Anyone who is not an active platform operator — including a plain Hub member — gets the SAME
// 404 a nonexistent route would, same convention as every other platform-wide handler.
func TestOperatorStatus_NonOperatorGetsTheUniform404(t *testing.T) {
	w := newWorld(t)
	member := w.user("plain-member")

	rec := operatorStatusCall(t, w, member)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET as non-operator = %d, want 404", rec.Code)
	}
}

// A revoked operator loses access immediately — the status check re-reads the database on every
// call, never caching a past grant.
func TestOperatorStatus_RevokedOperatorLosesAccess(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op", false, true)
	w.exec(`UPDATE platform_operators SET status = 'revoked', revoked_at = now() WHERE user_id = $1`, op)

	rec := operatorStatusCall(t, w, op)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET as revoked operator = %d, want 404", rec.Code)
	}
}
