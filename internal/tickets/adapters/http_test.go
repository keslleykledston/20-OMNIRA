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

var phoneSeq int64

// nextPhone returns a fresh, valid E.164 number (contacts_phone_e164_format:
// '+' followed by 7-15 digits) — a UUID fragment can contain hex letters,
// which the constraint rejects.
func nextPhone() string {
	n := atomic.AddInt64(&phoneSeq, 1)
	return fmt.Sprintf("+551190%05d", n)
}

// seedPool connects as the owner to prepare state directly, bypassing RLS on
// purpose (same pattern as internal/contacts/adapters/http_test.go). It is
// never the path the application takes.
func seedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping tickets API tests")
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
		t.Skip("OMNIRA_APP_DATABASE_URL not set; skipping RLS-enforced tickets tests")
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

// seedMember creates a user with a membership under the given fixed system
// role (e.g. "tenant_admin", "tenant_supervisor", "tenant_agent") — the exact
// grant matrix PRODUCT.2-A froze in internal/iam3/security_test.go.
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

func seedTicket(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, subject, status, priority string, updatedAt time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	contactID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email, status) VALUES ($1,$2,$3,$4,'','active')`,
		contactID, tenantID, "Contact "+contactID.String()[:8], nextPhone()); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	conversationID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, status) VALUES ($1,$2,$3,'open')`,
		conversationID, tenantID, contactID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	ticketID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
		ticketID, tenantID, conversationID, status, priority, subject, updatedAt); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return ticketID
}

// callAsTenant drives the handler exactly as the server does: inside a real
// RLS session opened for userID, with a TenantContext already established.
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

type page struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
	Count      int              `json:"count"`
	Limit      int              `json:"limit"`
}

func decodePage(t *testing.T, rec *httptest.ResponseRecorder) page {
	t.Helper()
	var body page
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode page: %v (body=%s)", err, rec.Body.String())
	}
	return body
}

func TestListTicketsRequiresTicketRead(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "perm")
	admin := seedMember(t, seed, tenantID, "tenant_admin", "active")
	supervisor := seedMember(t, seed, tenantID, "tenant_supervisor", "active")
	agent := seedMember(t, seed, tenantID, "tenant_agent", "active")
	seedTicket(t, seed, tenantID, "Ticket 1", "open", "medium", time.Now().UTC())

	h := NewHandler(app)
	target := "/api/v1/tenants/" + tenantID.String() + "/tickets"

	if rec := callAsTenant(t, app, tenantID, admin, target, h.List); rec.Code != http.StatusOK {
		t.Fatalf("tenant_admin (has ticket.read) = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if rec := callAsTenant(t, app, tenantID, supervisor, target, h.List); rec.Code != http.StatusOK {
		t.Fatalf("tenant_supervisor (has ticket.read) = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if rec := callAsTenant(t, app, tenantID, agent, target, h.List); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_agent (no ticket.read) = %d %q, want 403", rec.Code, rec.Body.String())
	}
}

func TestListTicketsIsScopedToTheSessionTenant(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "A"), seedTenant(t, seed, "B")
	userA := seedMember(t, seed, tenantA, "tenant_admin", "active")
	userB := seedMember(t, seed, tenantB, "tenant_admin", "active")

	now := time.Now().UTC()
	seedTicket(t, seed, tenantA, "Tenant A ticket", "open", "medium", now)
	seedTicket(t, seed, tenantB, "Tenant B ticket", "open", "medium", now)

	h := NewHandler(app)

	recA := callAsTenant(t, app, tenantA, userA, "/api/v1/tenants/"+tenantA.String()+"/tickets", h.List)
	pageA := decodePage(t, recA)
	if len(pageA.Items) != 1 || pageA.Items[0]["subject"] != "Tenant A ticket" {
		t.Fatalf("tenant A should see exactly its own ticket, got %v", pageA.Items)
	}

	recB := callAsTenant(t, app, tenantB, userB, "/api/v1/tenants/"+tenantB.String()+"/tickets", h.List)
	pageB := decodePage(t, recB)
	if len(pageB.Items) != 1 || pageB.Items[0]["subject"] != "Tenant B ticket" {
		t.Fatalf("tenant B should see exactly its own ticket, got %v", pageB.Items)
	}
}

func TestListTicketsDoesNotExposeTenantID(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "payload")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	seedTicket(t, seed, tenantID, "Ticket", "open", "medium", time.Now().UTC())

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets", h.List)
	p := decodePage(t, rec)
	if len(p.Items) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(p.Items))
	}
	if _, present := p.Items[0]["tenant_id"]; present {
		t.Fatalf("tenant_id must not be serialized: %v", p.Items[0])
	}
}

func TestListTicketsCursorPaginationIsDeterministic(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "paging")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	base := time.Now().UTC().Add(-time.Hour)
	var subjects []string
	for i := 0; i < 5; i++ {
		subject := "Ticket " + string(rune('A'+i))
		subjects = append(subjects, subject)
		seedTicket(t, seed, tenantID, subject, "open", "medium", base.Add(time.Duration(i)*time.Minute))
	}

	h := NewHandler(app)
	target := "/api/v1/tenants/" + tenantID.String() + "/tickets?limit=2"

	var seen []string
	cursor := ""
	for i := 0; i < 10; i++ {
		url := target
		if cursor != "" {
			url = target + "&cursor=" + cursor
		}
		rec := callAsTenant(t, app, tenantID, userID, url, h.List)
		if rec.Code != http.StatusOK {
			t.Fatalf("list page %d = %d %q", i, rec.Code, rec.Body.String())
		}
		p := decodePage(t, rec)
		for _, item := range p.Items {
			seen = append(seen, item["subject"].(string))
		}
		if !p.HasMore {
			break
		}
		if p.NextCursor == "" {
			t.Fatalf("has_more=true but next_cursor is empty")
		}
		cursor = p.NextCursor
	}

	if len(seen) != 5 {
		t.Fatalf("expected to walk all 5 tickets across pages exactly once, got %v", seen)
	}
	// Newest first (updated_at DESC): last seeded ("Ticket E") comes first.
	if seen[0] != "Ticket E" || seen[4] != "Ticket A" {
		t.Fatalf("unexpected order: %v", seen)
	}
}

func TestListTicketsInvalidCursorIsRejected(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "badcursor")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?cursor=not-base64!!", h.List)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor = %d %q, want 400", rec.Code, rec.Body.String())
	}
}

func TestListTicketsStatusFilter(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "statusfilter")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	now := time.Now().UTC()
	seedTicket(t, seed, tenantID, "Open one", "open", "medium", now)
	seedTicket(t, seed, tenantID, "Closed one", "closed", "medium", now)

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?status=open", h.List)
	p := decodePage(t, rec)
	if len(p.Items) != 1 || p.Items[0]["subject"] != "Open one" {
		t.Fatalf("status=open should return exactly the open ticket, got %v", p.Items)
	}
}

func TestListTicketsPriorityFilter(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "priorityfilter")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	now := time.Now().UTC()
	seedTicket(t, seed, tenantID, "Critical one", "open", "critical", now)
	seedTicket(t, seed, tenantID, "Low one", "open", "low", now)

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?priority=critical", h.List)
	p := decodePage(t, rec)
	if len(p.Items) != 1 || p.Items[0]["subject"] != "Critical one" {
		t.Fatalf("priority=critical should return exactly the critical ticket, got %v", p.Items)
	}
}

func TestListTicketsInvalidStatusIsRejected(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "badstatus")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?status=bogus", h.List)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d %q, want 400", rec.Code, rec.Body.String())
	}
}

func TestListTicketsLimitIsBounded(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "boundedlimit")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?limit=500", h.List)
	p := decodePage(t, rec)
	if p.Limit != 100 {
		t.Fatalf("limit should clamp to the platform max of 100, got %d", p.Limit)
	}
}
