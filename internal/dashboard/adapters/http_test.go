package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// seedPool connects as the owner to prepare state directly, bypassing RLS on
// purpose (same pattern as internal/tickets/adapters/http_test.go). It is
// never the path the application takes.
func seedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping dashboard API tests")
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
		t.Skip("OMNIRA_APP_DATABASE_URL not set; skipping RLS-enforced dashboard tests")
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
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id)
	})
	return id
}

func seedMember(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, roleKey, status string) uuid.UUID {
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
		`SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL LIMIT 1`, roleKey).Scan(&roleID); err != nil {
		t.Fatalf("seed role %s: %v", roleKey, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,$5)`,
		uuid.New(), tenantID, userID, roleID, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})
	return userID
}

var phoneSeq int64

func nextPhone() string {
	n := atomic.AddInt64(&phoneSeq, 1)
	return fmt.Sprintf("+551190%05d", n)
}

func seedContact(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email, status) VALUES ($1,$2,$3,$4,'','active')`,
		id, tenantID, "Contact "+id.String()[:8], nextPhone()); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return id
}

func seedConversation(t *testing.T, pool *pgxpool.Pool, tenantID, contactID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO conversations (id, tenant_id, contact_id, status) VALUES ($1,$2,$3,$4)`,
		id, tenantID, contactID, status); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	return id
}

func seedTicket(t *testing.T, pool *pgxpool.Pool, tenantID, conversationID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Now().UTC()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,'medium','',$5,$5)`,
		id, tenantID, conversationID, status, now); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return id
}

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

func decodeSnapshot(t *testing.T, rec *httptest.ResponseRecorder) Snapshot {
	t.Helper()
	var s Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode snapshot: %v (body=%s)", err, rec.Body.String())
	}
	return s
}

func TestGetSnapshotRequiresDashboardRead(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "perm")
	admin := seedMember(t, seed, tenantID, "tenant_admin", "active")
	supervisor := seedMember(t, seed, tenantID, "tenant_supervisor", "active")
	agent := seedMember(t, seed, tenantID, "tenant_agent", "active")

	h := NewHandler(app)
	target := "/api/v1/tenants/" + tenantID.String() + "/dashboard/snapshot"

	if rec := callAsTenant(t, app, tenantID, admin, target, h.GetSnapshot); rec.Code != http.StatusOK {
		t.Fatalf("tenant_admin (has dashboard.read) = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if rec := callAsTenant(t, app, tenantID, supervisor, target, h.GetSnapshot); rec.Code != http.StatusOK {
		t.Fatalf("tenant_supervisor (has dashboard.read) = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if rec := callAsTenant(t, app, tenantID, agent, target, h.GetSnapshot); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_agent (no dashboard.read) = %d %q, want 403", rec.Code, rec.Body.String())
	}
}

func TestGetSnapshotCounts(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "counts")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	c1, c2, c3 := seedContact(t, seed, tenantID), seedContact(t, seed, tenantID), seedContact(t, seed, tenantID)
	seedConversation(t, seed, tenantID, c1, "open")
	openConvID := seedConversation(t, seed, tenantID, c2, "open")
	seedConversation(t, seed, tenantID, c3, "closed")
	seedTicket(t, seed, tenantID, openConvID, "open")
	seedTicket(t, seed, tenantID, openConvID, "closed")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/dashboard/snapshot", h.GetSnapshot)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot = %d %q", rec.Code, rec.Body.String())
	}
	snap := decodeSnapshot(t, rec)
	if snap.OpenConversations != 2 {
		t.Fatalf("open_conversations = %d, want 2", snap.OpenConversations)
	}
	if snap.OpenTickets != 1 {
		t.Fatalf("open_tickets = %d, want 1", snap.OpenTickets)
	}
	if snap.TotalContacts != 3 {
		t.Fatalf("total_contacts = %d, want 3", snap.TotalContacts)
	}
}

func TestGetSnapshotIsScopedToTheSessionTenant(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "A"), seedTenant(t, seed, "B")
	userA := seedMember(t, seed, tenantA, "tenant_admin", "active")
	userB := seedMember(t, seed, tenantB, "tenant_admin", "active")

	seedContact(t, seed, tenantA)
	seedContact(t, seed, tenantB)
	seedContact(t, seed, tenantB)

	h := NewHandler(app)

	recA := callAsTenant(t, app, tenantA, userA, "/api/v1/tenants/"+tenantA.String()+"/dashboard/snapshot", h.GetSnapshot)
	snapA := decodeSnapshot(t, recA)
	if snapA.TotalContacts != 1 {
		t.Fatalf("tenant A should see exactly its own contact count, got %d", snapA.TotalContacts)
	}

	recB := callAsTenant(t, app, tenantB, userB, "/api/v1/tenants/"+tenantB.String()+"/dashboard/snapshot", h.GetSnapshot)
	snapB := decodeSnapshot(t, recB)
	if snapB.TotalContacts != 2 {
		t.Fatalf("tenant B should see exactly its own contact count, got %d", snapB.TotalContacts)
	}
}
