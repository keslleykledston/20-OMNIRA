package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
)

// Real Postgres, runtime role under RLS, a real tenant session per call.
// Tenant A is the caller; Tenant B must never be affected.

func callSettings(t *testing.T, app *pgxpool.Pool, h *InboxSettingsHandler, tenant, actor uuid.UUID, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	fn := h.Get
	if method == http.MethodPut {
		fn = h.Put
	}
	var rec *httptest.ResponseRecorder
	if err := asActor(t, app, tenant, actor, func(ctx context.Context) error {
		rec = doRequest(t, ctx, method, fn, nil, []byte(body))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rec
}

func decodeSettings(t *testing.T, rec *httptest.ResponseRecorder) inboxSettings {
	t.Helper()
	var s inboxSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return s
}

func storedSettings(t *testing.T, seed *pgxpool.Pool, tenant uuid.UUID) inboxSettings {
	t.Helper()
	var s inboxSettings
	if err := seed.QueryRow(context.Background(), `SELECT wait_warn_minutes, wait_danger_minutes FROM tenants WHERE id=$1`, tenant).
		Scan(&s.WaitWarnMinutes, &s.WaitDangerMinutes); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInboxSettingsReadByAnyMemberWriteOnlyByAdminAndAudited(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	h := NewInboxSettingsHandler(app, auditadapters.NewPostgresAuditEventRepository(app))
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	supervisor := seedTeamMember(t, seed, a.tenantID, "tenant_supervisor", "active")
	agent := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	adminB := seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")
	revoked := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "revoked")

	defaults := inboxSettings{WaitWarnMinutes: 30, WaitDangerMinutes: 120}

	// Every role reads, and a new tenant starts at the values the UI always used.
	for name, user := range map[string]uuid.UUID{"admin": admin, "supervisor": supervisor, "agent": agent} {
		rec := callSettings(t, app, h, a.tenantID, user, http.MethodGet, "")
		if rec.Code != http.StatusOK || decodeSettings(t, rec) != defaults {
			t.Fatalf("%s read = %d %s, want 200 %+v", name, rec.Code, rec.Body.String(), defaults)
		}
	}

	// Only the admin writes; supervisor and agent are refused and nothing changes. Two
	// independent layers enforce it: the tenant.manage check in the handler and the tenants
	// UPDATE policy (admin membership only, RowsAffected != 1 -> 403). Removing either one
	// alone still passes this test by design; both going away at once would not.
	body := `{"wait_warn_minutes":45,"wait_danger_minutes":180}`
	for name, user := range map[string]uuid.UUID{"supervisor": supervisor, "agent": agent} {
		if rec := callSettings(t, app, h, a.tenantID, user, http.MethodPut, body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s write = %d, want 403", name, rec.Code)
		}
	}
	if got := storedSettings(t, seed, a.tenantID); got != defaults {
		t.Fatalf("a refused write changed the tenant: %+v", got)
	}

	rec := callSettings(t, app, h, a.tenantID, admin, http.MethodPut, body)
	want := inboxSettings{WaitWarnMinutes: 45, WaitDangerMinutes: 180}
	if rec.Code != http.StatusOK || decodeSettings(t, rec) != want {
		t.Fatalf("admin write = %d %s, want 200 %+v", rec.Code, rec.Body.String(), want)
	}
	if got := storedSettings(t, seed, a.tenantID); got != want {
		t.Fatalf("stored = %+v, want %+v", got, want)
	}
	// The agent now reads the new values (this is what paints the Inbox).
	if rec := callSettings(t, app, h, a.tenantID, agent, http.MethodGet, ""); decodeSettings(t, rec) != want {
		t.Fatalf("agent must see the saved values, got %s", rec.Body.String())
	}

	// Audited once, with before and after.
	if n := auditCount(t, seed, a.tenantID, "tenant.inbox_settings_updated"); n != 1 {
		t.Fatalf("audit events = %d, want 1", n)
	}
	var from, to int
	if err := seed.QueryRow(context.Background(),
		`SELECT (metadata->>'wait_warn_minutes_from')::int, (metadata->>'wait_warn_minutes_to')::int FROM audit_events WHERE tenant_id=$1 AND action='tenant.inbox_settings_updated'`,
		a.tenantID).Scan(&from, &to); err != nil || from != 30 || to != 45 {
		t.Fatalf("audit metadata from/to = %d/%d (%v), want 30/45", from, to, err)
	}

	// Tenant B is untouched, and A's admin cannot reach B: another tenant's context is refused.
	if got := storedSettings(t, seed, b.tenantID); got != defaults {
		t.Fatalf("tenant B changed: %+v", got)
	}
	if rec := callSettings(t, app, h, b.tenantID, admin, http.MethodPut, `{"wait_warn_minutes":5,"wait_danger_minutes":6}`); rec.Code == http.StatusOK {
		t.Fatalf("admin of A wrote tenant B: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callSettings(t, app, h, b.tenantID, admin, http.MethodGet, ""); rec.Code == http.StatusOK {
		t.Fatalf("admin of A read tenant B: %d %s", rec.Code, rec.Body.String())
	}
	if got := storedSettings(t, seed, b.tenantID); got != defaults {
		t.Fatalf("tenant B changed by a cross-tenant write: %+v", got)
	}
	// B's own admin edits only B.
	if rec := callSettings(t, app, h, b.tenantID, adminB, http.MethodPut, `{"wait_warn_minutes":10,"wait_danger_minutes":20}`); rec.Code != http.StatusOK {
		t.Fatalf("admin of B write = %d", rec.Code)
	}
	if got := storedSettings(t, seed, a.tenantID); got != want {
		t.Fatalf("tenant A changed by B's admin: %+v", got)
	}

	// A revoked membership neither reads nor writes.
	if rec := callSettings(t, app, h, a.tenantID, revoked, http.MethodGet, ""); rec.Code == http.StatusOK {
		t.Fatalf("revoked member read settings: %d", rec.Code)
	}
	if rec := callSettings(t, app, h, a.tenantID, revoked, http.MethodPut, `{"wait_warn_minutes":1,"wait_danger_minutes":2}`); rec.Code == http.StatusOK {
		t.Fatalf("revoked admin wrote settings: %d", rec.Code)
	}
	if got := storedSettings(t, seed, a.tenantID); got != want {
		t.Fatalf("tenant A changed by a revoked member: %+v", got)
	}
}

func TestInboxSettingsRejectsInvalidValuesWithoutChangingAnything(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	h := NewInboxSettingsHandler(app, auditadapters.NewPostgresAuditEventRepository(app))
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	defaults := inboxSettings{WaitWarnMinutes: 30, WaitDangerMinutes: 120}

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"warn zero":                 {`{"wait_warn_minutes":0,"wait_danger_minutes":10}`, http.StatusUnprocessableEntity},
		"warn negative":             {`{"wait_warn_minutes":-5,"wait_danger_minutes":10}`, http.StatusUnprocessableEntity},
		"danger equals warn":        {`{"wait_warn_minutes":60,"wait_danger_minutes":60}`, http.StatusUnprocessableEntity},
		"danger below warn":         {`{"wait_warn_minutes":60,"wait_danger_minutes":30}`, http.StatusUnprocessableEntity},
		"danger above one week":     {`{"wait_warn_minutes":30,"wait_danger_minutes":10081}`, http.StatusUnprocessableEntity},
		"missing danger":            {`{"wait_warn_minutes":30}`, http.StatusBadRequest},
		"missing warn":              {`{"wait_danger_minutes":120}`, http.StatusBadRequest},
		"empty body object":         {`{}`, http.StatusBadRequest},
		"not json":                  {`nope`, http.StatusBadRequest},
		"fractional minutes":        {`{"wait_warn_minutes":1.5,"wait_danger_minutes":120}`, http.StatusBadRequest},
		"string minutes":            {`{"wait_warn_minutes":"30","wait_danger_minutes":120}`, http.StatusBadRequest},
		"unknown field (tenant_id)": {`{"wait_warn_minutes":30,"wait_danger_minutes":120,"tenant_id":"` + uuid.NewString() + `"}`, http.StatusBadRequest},
	} {
		if rec := callSettings(t, app, h, a.tenantID, admin, http.MethodPut, tc.body); rec.Code != tc.code {
			t.Errorf("%s: status %d, want %d (%s)", name, rec.Code, tc.code, rec.Body.String())
		}
	}
	if got := storedSettings(t, seed, a.tenantID); got != defaults {
		t.Fatalf("a rejected request changed the tenant: %+v", got)
	}
	if n := auditCount(t, seed, a.tenantID, "tenant.inbox_settings_updated"); n != 0 {
		t.Fatalf("rejected requests must not be audited as updates, got %d", n)
	}
	// The boundaries themselves are valid.
	if rec := callSettings(t, app, h, a.tenantID, admin, http.MethodPut, `{"wait_warn_minutes":1,"wait_danger_minutes":10080}`); rec.Code != http.StatusOK {
		t.Fatalf("1 / 10080 must be accepted, got %d %s", rec.Code, rec.Body.String())
	}
}
