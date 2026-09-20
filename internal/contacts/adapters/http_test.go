package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// seedPool connects as the owner to prepare state directly, bypassing RLS on
// purpose. It is never the path the application takes.
func seedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping contacts API tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("seed ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// appPool connects as omnira_app — the unprivileged runtime role. Queries run
// through it are subject to RLS, which is the whole point of these tests.
func appPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_APP_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_APP_DATABASE_URL not set; skipping RLS-enforced contacts tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("app ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedTenant(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tenants (id, legal_name, isolation_profile, status)
		 VALUES ($1,$2,'shared_strong_isolation','active')`,
		id, name+"-"+id.String()[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}

// seedMember creates a user and its membership in the tenant. status drives the
// revocation case: the read policy requires an *active* membership.
func seedMember(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	userID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, external_subject, email, status) VALUES ($1,$2,$3,'active')`,
		userID, userID.String(), userID.String()+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var roleID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,$5)`,
		uuid.New(), tenantID, userID, roleID, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	return userID
}

func seedContact(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, name, phone string, updatedAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email, status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,'','active',$5,$5)`,
		id, tenantID, name, phone, updatedAt); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return id
}

// callAsTenant drives the handler exactly as the server does: inside a real RLS
// session opened for userID, with a TenantContext already established.
func callAsTenant(t *testing.T, pool *pgxpool.Pool, tenantID, userID uuid.UUID, target string, fn func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	err := platformdb.WithTenantSession(context.Background(), pool, userID, false, func(sessionCtx context.Context) error {
		tc, tcErr := tenancydomain.NewTenantContext(tenantID, userID, tenancydomain.AccessSourceDirect)
		if tcErr != nil {
			return tcErr
		}
		req := httptest.NewRequest(http.MethodGet, target, nil).
			WithContext(tenancydomain.WithTenantContext(sessionCtx, tc))
		fn(rec, req)
		return nil
	})
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
	return rec
}

func decodePage(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode page: %v (body=%s)", err, rec.Body.String())
	}
	return body.Items
}

func TestListContactsIsScopedToTheSessionTenant(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "A"), seedTenant(t, seed, "B")
	userA, userB := seedMember(t, seed, tenantA, "active"), seedMember(t, seed, tenantB, "active")

	now := time.Now().UTC()
	seedContact(t, seed, tenantA, "Alice A", "+5511900000001", now)
	seedContact(t, seed, tenantB, "Bruno B", "+5511900000002", now)

	h := NewContactsAPIHandler(app)

	recA := callAsTenant(t, app, tenantA, userA, "/api/v1/tenants/"+tenantA.String()+"/contacts", h.ListContacts)
	if recA.Code != http.StatusOK {
		t.Fatalf("tenant A list = %d %q", recA.Code, recA.Body.String())
	}
	itemsA := decodePage(t, recA)
	if len(itemsA) != 1 || itemsA[0]["display_name"] != "Alice A" {
		t.Fatalf("tenant A should see exactly its own contact, got %v", itemsA)
	}

	recB := callAsTenant(t, app, tenantB, userB, "/api/v1/tenants/"+tenantB.String()+"/contacts", h.ListContacts)
	itemsB := decodePage(t, recB)
	if len(itemsB) != 1 || itemsB[0]["display_name"] != "Bruno B" {
		t.Fatalf("tenant B should see exactly its own contact, got %v", itemsB)
	}
}

// The payload must not echo tenant_id: the tenant is already established by the
// session, and returning it only widens what a compromised client learns.
func TestListContactsDoesNotExposeTenantID(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "payload")
	userID := seedMember(t, seed, tenantID, "active")
	seedContact(t, seed, tenantID, "Carol", "+5511900000003", time.Now().UTC())

	h := NewContactsAPIHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/contacts", h.ListContacts)
	items := decodePage(t, rec)
	if len(items) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(items))
	}
	if _, present := items[0]["tenant_id"]; present {
		t.Fatalf("tenant_id must not be serialized: %v", items[0])
	}
}

// Knowing another tenant's contact id must not be enough: the response is the
// same 404 an unknown id gets, so the endpoint cannot be used to probe.
func TestGetContactOfAnotherTenantIs404(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "A"), seedTenant(t, seed, "B")
	userA := seedMember(t, seed, tenantA, "active")
	contactB := seedContact(t, seed, tenantB, "Bruno B", "+5511900000004", time.Now().UTC())

	h := NewContactsAPIHandler(app)
	rec := callAsTenant(t, app, tenantA, userA,
		"/api/v1/tenants/"+tenantA.String()+"/contacts/"+contactB.String(),
		func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("contact_id", contactB.String())
			h.GetContact(w, r)
		})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get = %d %q, want 404", rec.Code, rec.Body.String())
	}
}

func TestGetContactUnknownIDIs404(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "unknown")
	userID := seedMember(t, seed, tenantID, "active")
	missing := uuid.New()

	h := NewContactsAPIHandler(app)
	rec := callAsTenant(t, app, tenantID, userID,
		"/api/v1/tenants/"+tenantID.String()+"/contacts/"+missing.String(),
		func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("contact_id", missing.String())
			h.GetContact(w, r)
		})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", rec.Code)
	}
}

func TestGetContactInvalidUUIDIs400(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "invalid")
	userID := seedMember(t, seed, tenantID, "active")

	h := NewContactsAPIHandler(app)
	rec := callAsTenant(t, app, tenantID, userID,
		"/api/v1/tenants/"+tenantID.String()+"/contacts/not-a-uuid",
		func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("contact_id", "not-a-uuid")
			h.GetContact(w, r)
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid uuid = %d, want 400", rec.Code)
	}
}

// The read policy requires an active membership, so revoking it must close
// access even though the tenant id is unchanged.
func TestRevokedMembershipLosesAccess(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "revoked")
	userID := seedMember(t, seed, tenantID, "revoked")
	seedContact(t, seed, tenantID, "Hidden", "+5511900000005", time.Now().UTC())

	h := NewContactsAPIHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/contacts", h.ListContacts)
	if items := decodePage(t, rec); len(items) != 0 {
		t.Fatalf("revoked membership must see nothing, got %v", items)
	}
}

// Ordering is (updated_at DESC, id DESC) and the cursor walks it, so paging
// must not skip or repeat a row even when timestamps collide.
func TestListContactsPaginationIsStable(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "paging")
	userID := seedMember(t, seed, tenantID, "active")

	shared := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		seedContact(t, seed, tenantID, "Contact", "+551190000010"+string(rune('0'+i)), shared)
	}

	h := NewContactsAPIHandler(app)
	base := "/api/v1/tenants/" + tenantID.String() + "/contacts?limit=2"

	seen := map[string]bool{}
	target := base
	for page := 0; page < 5; page++ {
		rec := callAsTenant(t, app, tenantID, userID, target, h.ListContacts)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d = %d %q", page, rec.Code, rec.Body.String())
		}
		var body struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode page %d: %v", page, err)
		}
		for _, item := range body.Items {
			id, _ := item["id"].(string)
			if seen[id] {
				t.Fatalf("contact %s returned twice across pages", id)
			}
			seen[id] = true
		}
		if body.NextCursor == "" {
			break
		}
		target = base + "&cursor=" + body.NextCursor
	}

	if len(seen) != 5 {
		t.Fatalf("paging covered %d of 5 contacts", len(seen))
	}
}

// The runtime role must not be able to bypass RLS — if it ever gains SUPERUSER
// or BYPASSRLS, every isolation test above silently stops proving anything.
func TestRuntimeRoleCannotBypassRLS(t *testing.T) {
	app := appPool(t)
	var isSuper, bypassRLS bool
	if err := app.QueryRow(context.Background(),
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&isSuper, &bypassRLS); err != nil {
		t.Fatalf("inspect runtime role: %v", err)
	}
	if isSuper || bypassRLS {
		t.Fatalf("runtime role must be NOSUPERUSER/NOBYPASSRLS, got super=%v bypassrls=%v", isSuper, bypassRLS)
	}
}
