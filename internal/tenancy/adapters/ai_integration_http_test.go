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

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Real Postgres, runtime role under FORCE RLS. Tenant A configures the integration; Tenant B must never see it;
// the key is write-only and must not appear in any response, log-bound error or audit event.

const testKey = "AIzaSyTESTKEY0123456789abcdefghijklmn"

func aiHandler(t *testing.T, app *pgxpool.Pool) *AIIntegrationHandler {
	t.Helper()
	cipher, err := channelcrypto.NewAESGCM([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return NewAIIntegrationHandler(app, auditadapters.NewPostgresAuditEventRepository(app), cipher)
}

func callAI(t *testing.T, app *pgxpool.Pool, fn http.HandlerFunc, tenant, actor uuid.UUID, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rec *httptest.ResponseRecorder
	if err := asActor(t, app, tenant, actor, func(ctx context.Context) error {
		rec = doRequest(t, ctx, method, fn, nil, []byte(body))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rec
}

func aiBody(t *testing.T, rec *httptest.ResponseRecorder) aiResponse {
	t.Helper()
	var r aiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return r
}

func TestAIIntegrationDefaultsOffAndOnlyAnAdminManagesIt(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	h := aiHandler(t, app)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	supervisor := seedTeamMember(t, seed, a.tenantID, "tenant_supervisor", "active")
	agent := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	adminB := seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")

	rec := callAI(t, app, h.Get, a.tenantID, admin, http.MethodGet, "")
	got := aiBody(t, rec)
	if rec.Code != 200 || got.Enabled || got.KeyConfigured || got.Model != "gemini-2.5-flash" || got.BudgetUSD != 10 || got.ConsentCurrent || got.ConsentText == "" {
		t.Fatalf("fresh tenant = %d %+v: must be off, no key, US$10, no consent", rec.Code, got)
	}
	for name, user := range map[string]uuid.UUID{"supervisor": supervisor, "agent": agent} {
		if rec := callAI(t, app, h.Get, a.tenantID, user, http.MethodGet, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s read = %d, want 403", name, rec.Code)
		}
		if rec := callAI(t, app, h.Put, a.tenantID, user, http.MethodPut, `{"api_key":"`+testKey+`"}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s write = %d, want 403", name, rec.Code)
		}
	}
	if n := countAI(t, seed, a.tenantID); n != 0 {
		t.Fatalf("a refused write created %d rows", n)
	}

	// Tenant B's admin sees only Tenant B's (default) state and cannot touch A's.
	if rec := callAI(t, app, h.Put, a.tenantID, adminB, http.MethodPut, `{"model":"x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant write = %d, want 403", rec.Code)
	}
}

func countAI(t *testing.T, seed *pgxpool.Pool, tenant uuid.UUID) (n int) {
	t.Helper()
	if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM tenant_ai_integrations WHERE tenant_id=$1`, tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAIKeyIsWriteOnlyEncryptedAndNeverLeaks(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	h := aiHandler(t, app)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")

	rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"api_key":"`+testKey+`"}`)
	got := aiBody(t, rec)
	if rec.Code != 200 || !got.KeyConfigured || got.Enabled || got.KeySetAt == nil || got.KeySetBy == nil {
		t.Fatalf("save key = %d %s", rec.Code, rec.Body.String())
	}
	// Not in this response, not in a later read.
	if strings.Contains(rec.Body.String(), testKey) || strings.Contains(callAI(t, app, h.Get, a.tenantID, admin, http.MethodGet, "").Body.String(), "TESTKEY") {
		t.Fatal("the API key came back in a response")
	}
	// At rest it is ciphertext: neither the plaintext nor a recognisable fragment is in the column.
	var blob []byte
	if err := seed.QueryRow(context.Background(), `SELECT secret_ciphertext FROM tenant_ai_integrations WHERE tenant_id=$1`, a.tenantID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if len(blob) < 12+16 || strings.Contains(string(blob), "TESTKEY") || strings.Contains(string(blob), "AIza") {
		t.Fatalf("stored value does not look encrypted (%d bytes)", len(blob))
	}
	// Two saves of the same key must not produce the same ciphertext (fresh nonce).
	_ = callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"api_key":"`+testKey+`"}`)
	var blob2 []byte
	_ = seed.QueryRow(context.Background(), `SELECT secret_ciphertext FROM tenant_ai_integrations WHERE tenant_id=$1`, a.tenantID).Scan(&blob2)
	if string(blob) == string(blob2) {
		t.Fatal("same ciphertext twice: nonce reused")
	}
	// The audit trail records that a key changed, never the key.
	var meta string
	if err := seed.QueryRow(context.Background(), `SELECT metadata::text FROM audit_events WHERE tenant_id=$1 AND action='tenant.ai_integration_updated' ORDER BY created_at LIMIT 1`, a.tenantID).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(meta, "TESTKEY") || strings.Contains(meta, "AIza") || !strings.Contains(meta, `"key_changed": true`) {
		t.Fatalf("audit metadata = %s", meta)
	}
	// Malformed keys are refused without echoing them.
	for _, bad := range []string{"short", "has spaces in the key 0123456789", "<script>alert(1)</script>0123456789"} {
		rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"api_key":"`+bad+`"}`)
		if rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), bad) {
			t.Errorf("bad key %q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	// A key with a dot (newer Google format) is accepted.
	if rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"api_key":"AQ.TESTKEYwithadot0123456789abcdefghijkl"}`); rec.Code != http.StatusOK {
		t.Errorf("key with a dot = %d %s", rec.Code, rec.Body.String())
	}
	// Unknown fields are refused (no way to smuggle a column).
	if rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"secret_ciphertext":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", rec.Code)
	}
}

func TestAIEnablingNeedsAKeyAndTheCurrentConsentAndTheDatabaseAgrees(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	h := aiHandler(t, app)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")

	// No key: cannot enable.
	if rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"enabled":true,"accept_external_ai":true}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("enable without key = %d, want 422", rec.Code)
	}
	_ = callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"api_key":"`+testKey+`"}`)
	// Key but no consent: cannot enable.
	for _, body := range []string{`{"enabled":true}`, `{"enabled":true,"accept_external_ai":false}`} {
		if rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, body); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("enable without consent (%s) = %d, want 422", body, rec.Code)
		}
	}
	if aiEnabled(t, seed, a.tenantID) {
		t.Fatal("refused requests must leave it off")
	}
	rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"enabled":true,"accept_external_ai":true,"monthly_budget_usd":12.345}`)
	got := aiBody(t, rec)
	if rec.Code != 200 || !got.Enabled || !got.ConsentCurrent || got.ConsentAt == nil || got.ConsentBy == nil || got.BudgetUSD != 12.35 {
		t.Fatalf("enable = %d %s", rec.Code, rec.Body.String())
	}
	// Once consent is current, later changes do not ask again; turning off and on again works.
	if rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"enabled":false}`); rec.Code != 200 || aiEnabled(t, seed, a.tenantID) {
		t.Fatalf("disable = %d", rec.Code)
	}
	if rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"enabled":true}`); rec.Code != 200 || !aiEnabled(t, seed, a.tenantID) {
		t.Fatalf("re-enable with current consent = %d %s", rec.Code, rec.Body.String())
	}
	// A new consent text version must be accepted again.
	if _, err := seed.Exec(context.Background(), `UPDATE tenant_ai_integrations SET enabled=false, consent_version='old-version' WHERE tenant_id=$1`, a.tenantID); err != nil {
		t.Fatal(err)
	}
	if rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"enabled":true}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("enable under an outdated consent = %d, want 422", rec.Code)
	}
	// The database refuses the invalid state on its own, whatever the application does.
	if _, err := seed.Exec(context.Background(), `UPDATE tenant_ai_integrations SET enabled=true, consent_at=NULL WHERE tenant_id=$1`, a.tenantID); err == nil {
		t.Fatal("the CHECK constraint must refuse enabled without consent")
	}
	if _, err := seed.Exec(context.Background(), `UPDATE tenant_ai_integrations SET enabled=true, secret_ciphertext=NULL, consent_at=now() WHERE tenant_id=$1`, a.tenantID); err == nil {
		t.Fatal("the CHECK constraint must refuse enabled without a key")
	}
	// Budget bounds.
	for _, bad := range []string{`{"monthly_budget_usd":-1}`, `{"monthly_budget_usd":10001}`, `{"model":"bad model!"}`} {
		if rec := callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, bad); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", bad, rec.Code)
		}
	}
}

func aiEnabled(t *testing.T, seed *pgxpool.Pool, tenant uuid.UUID) (v bool) {
	t.Helper()
	if err := seed.QueryRow(context.Background(), `SELECT enabled FROM tenant_ai_integrations WHERE tenant_id=$1`, tenant).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAIRemovingTheKeySwitchesItOff(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	h := aiHandler(t, app)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	_ = callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"api_key":"`+testKey+`","enabled":true,"accept_external_ai":true}`)
	if !aiEnabled(t, seed, a.tenantID) {
		t.Fatal("setup: should be enabled")
	}
	rec := callAI(t, app, h.DeleteKey, a.tenantID, admin, http.MethodDelete, "")
	got := aiBody(t, rec)
	if rec.Code != 200 || got.KeyConfigured || got.Enabled || aiEnabled(t, seed, a.tenantID) {
		t.Fatalf("delete key = %d %+v", rec.Code, got)
	}
	var n int
	_ = seed.QueryRow(context.Background(), `SELECT count(*) FROM tenant_ai_integrations WHERE tenant_id=$1 AND secret_ciphertext IS NOT NULL`, a.tenantID).Scan(&n)
	if n != 0 {
		t.Fatal("the ciphertext must be gone")
	}
}

func TestAINonAdminsCannotSeeTheRowAtTheDatabaseLevelEither(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	h := aiHandler(t, app)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	agent := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	_ = callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"api_key":"`+testKey+`"}`)
	read := func(user uuid.UUID) (n int) {
		_ = asActor(t, app, a.tenantID, user, func(ctx context.Context) error {
			return platformdb.QuerierFromContext(ctx, app).QueryRow(ctx, `SELECT count(*) FROM tenant_ai_integrations`).Scan(&n)
		})
		return n
	}
	if n := read(admin); n != 1 {
		t.Fatalf("admin session sees %d rows, want 1 (the test would prove nothing otherwise)", n)
	}
	if n := read(agent); n != 0 {
		t.Fatalf("agent session read %d rows of the integration table; the ciphertext must not be visible to non-admins", n)
	}
}

func TestAITestCallsTheProviderWithTheKeyInAHeaderAndReturnsOnlySafeMessages(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	h := aiHandler(t, app)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")

	var gotHeader, gotQuery string
	status := http.StatusOK
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader, gotQuery = r.Header.Get("x-goog-api-key"), r.URL.RawQuery
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash"},{"name":"models/other"}],"error":"SECRET-PROVIDER-TEXT"}`))
	}))
	defer provider.Close()
	h.geminiBase = provider.URL

	if rec := callAI(t, app, h.Test, a.tenantID, admin, http.MethodPost, ""); rec.Code != http.StatusConflict {
		t.Fatalf("test without key = %d, want 409", rec.Code)
	}
	_ = callAI(t, app, h.Put, a.tenantID, admin, http.MethodPut, `{"api_key":"`+testKey+`"}`)

	rec := callAI(t, app, h.Test, a.tenantID, admin, http.MethodPost, "")
	var res aiTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || !res.OK || res.ModelAvailable == nil || !*res.ModelAvailable {
		t.Fatalf("test = %d %s", rec.Code, rec.Body.String())
	}
	if gotHeader != testKey {
		t.Fatalf("provider received key %q in the header", gotHeader)
	}
	if strings.Contains(gotQuery, "key") || strings.Contains(gotQuery, "TESTKEY") {
		t.Fatalf("the key must never be in the URL, query = %q", gotQuery)
	}
	if strings.Contains(rec.Body.String(), "SECRET-PROVIDER-TEXT") || strings.Contains(rec.Body.String(), testKey) {
		t.Fatal("provider text or the key reached the client")
	}

	// Throttled: an immediate second test is refused.
	if rec := callAI(t, app, h.Test, a.tenantID, admin, http.MethodPost, ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate retest = %d, want 429", rec.Code)
	}

	// Rejected key, unknown model: fixed messages only.
	for _, c := range []struct {
		status int
		model  string
		ok     bool
	}{{http.StatusForbidden, "gemini-2.5-flash", false}, {http.StatusOK, "no-such-model", true}} {
		if _, err := seed.Exec(context.Background(), `UPDATE tenant_ai_integrations SET last_test_at=NULL, model=$2 WHERE tenant_id=$1`, a.tenantID, c.model); err != nil {
			t.Fatal(err)
		}
		status = c.status
		rec := callAI(t, app, h.Test, a.tenantID, admin, http.MethodPost, "")
		var r aiTestResult
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		if r.OK != c.ok || strings.Contains(rec.Body.String(), "SECRET-PROVIDER-TEXT") {
			t.Fatalf("status %d model %s: %s", c.status, c.model, rec.Body.String())
		}
		if c.ok && (r.ModelAvailable == nil || *r.ModelAvailable) {
			t.Fatalf("an unknown model must be reported unavailable: %s", rec.Body.String())
		}
	}
}
