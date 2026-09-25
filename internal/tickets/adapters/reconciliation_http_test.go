package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PRODUCT.7A1: real-Postgres tests for the read-only ticket reconciliation
// surface. seedPool/seedMember/seedTicket/callAsTenant/nextPhone are shared
// with http_test.go (same package, same test binary).

func seedConversation(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	contactID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email, status) VALUES ($1,$2,$3,$4,'','active')`,
		contactID, tenantID, "Contact "+contactID.String()[:8], nextPhone()); err != nil {
		t.Fatalf("seed conversation contact: %v", err)
	}
	conversationID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, status) VALUES ($1,$2,$3,'open')`,
		conversationID, tenantID, contactID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	return conversationID
}

type createAttemptSeed struct {
	provider           *string
	externalTicketID   *string
	localTicketID      *uuid.UUID
	projectionSyncedAt *time.Time
	createdAt          *time.Time
	updatedAt          *time.Time
}

func seedCreateAttempt(t *testing.T, pool *pgxpool.Pool, tenantID, conversationID, actorID uuid.UUID, state string, s createAttemptSeed) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	created := time.Now().UTC()
	if s.createdAt != nil {
		created = *s.createdAt
	}
	updated := created
	if s.updatedAt != nil {
		updated = *s.updatedAt
	}
	key := "seed-create-" + id.String()
	_, err := pool.Exec(ctx,
		`INSERT INTO ticket_external_create_attempts
		 (id, tenant_id, conversation_id, actor_user_id, idempotency_key, request_hash, state,
		  provider, external_ticket_id, local_ticket_id, projection_synced_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		id, tenantID, conversationID, actorID, key, "hash-"+id.String(), state,
		s.provider, s.externalTicketID, s.localTicketID, s.projectionSyncedAt, created, updated)
	if err != nil {
		t.Fatalf("seed create attempt: %v", err)
	}
	return id
}

type statusAttemptSeed struct {
	confirmedExternalStatus      *string
	confirmedExternalStatusLabel *string
	projectionSyncedAt           *time.Time
	createdAt                    *time.Time
	updatedAt                    *time.Time
}

func seedStatusAttempt(t *testing.T, pool *pgxpool.Pool, tenantID, localTicketID, conversationID, actorID uuid.UUID, provider, externalTicketID, targetStatus, state string, s statusAttemptSeed) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	created := time.Now().UTC()
	if s.createdAt != nil {
		created = *s.createdAt
	}
	updated := created
	if s.updatedAt != nil {
		updated = *s.updatedAt
	}
	key := "seed-status-" + id.String()
	_, err := pool.Exec(ctx,
		`INSERT INTO ticket_external_status_attempts
		 (id, tenant_id, local_ticket_id, conversation_id, actor_user_id, idempotency_key, request_hash,
		  provider, external_ticket_id, target_status, state, confirmed_external_status, confirmed_external_status_label,
		  projection_synced_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)`,
		id, tenantID, localTicketID, conversationID, actorID, key, "hash-"+id.String(),
		provider, externalTicketID, targetStatus, state, s.confirmedExternalStatus, s.confirmedExternalStatusLabel,
		s.projectionSyncedAt, created)
	if err != nil {
		t.Fatalf("seed status attempt: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE ticket_external_status_attempts SET updated_at=$2 WHERE id=$1`, id, updated)
	if err != nil {
		t.Fatalf("seed status attempt updated_at: %v", err)
	}
	return id
}

type reconciliationPage struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
	Count      int              `json:"count"`
	Limit      int              `json:"limit"`
}

func decodeReconciliationPage(t *testing.T, rec *httptest.ResponseRecorder) reconciliationPage {
	t.Helper()
	var body reconciliationPage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode page: %v (body=%s)", err, rec.Body.String())
	}
	return body
}

func strp(s string) *string { return &s }

// ============================================================
// Permission matrix (section 14)
// ============================================================

func TestReconciliationPermissionMatrix(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "reconcileperm")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	supervisorID := seedMember(t, seed, tenantID, "tenant_supervisor", "active")
	agentID := seedMember(t, seed, tenantID, "tenant_agent", "active")

	h := NewHandler(app)
	path := "/api/v1/tenants/" + tenantID.String() + "/ticket-reconciliation/create"

	rec := callAsTenant(t, app, tenantID, adminID, path, h.ListCreateAttempts)
	if rec.Code != http.StatusOK {
		t.Fatalf("tenant_admin: HTTP %d, want 200: %s", rec.Code, rec.Body.String())
	}

	rec = callAsTenant(t, app, tenantID, supervisorID, path, h.ListCreateAttempts)
	if rec.Code != http.StatusOK {
		t.Fatalf("tenant_supervisor: HTTP %d, want 200: %s", rec.Code, rec.Body.String())
	}

	rec = callAsTenant(t, app, tenantID, agentID, path, h.ListCreateAttempts)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_agent: HTTP %d, want 403 (no ticket.reconcile)", rec.Code)
	}
}

func TestReconciliationDoesNotAlterExistingTicketPermissions(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "reconcilenoregress")
	agentID := seedMember(t, seed, tenantID, "tenant_agent", "active")

	h := NewHandler(app)
	// tenant_agent still has ticket.create/ticket.update but not ticket.read —
	// unchanged by this slice (section 14).
	rec := callAsTenant(t, app, tenantID, agentID, "/api/v1/tenants/"+tenantID.String()+"/tickets", h.List)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_agent ticket.read: HTTP %d, want 403 (unchanged)", rec.Code)
	}
}

// ============================================================
// Route method (section 17)
// ============================================================

func TestReconciliationRoutesAreGetOnly(t *testing.T) {
	h := NewHandler(nil)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/ticket-reconciliation/create", http.HandlerFunc(h.ListCreateAttempts))
	mux.Handle("GET /api/v1/tenants/{tenant_id}/ticket-reconciliation/status", http.HandlerFunc(h.ListStatusAttempts))

	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		for _, path := range []string{"/create", "/status"} {
			req := httptest.NewRequest(method, "/api/v1/tenants/t/ticket-reconciliation"+path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: HTTP %d, want 405 (method not allowed)", method, path, rec.Code)
			}
		}
	}
}

// ============================================================
// Create attempt tests (section 15)
// ============================================================

func TestCreateAttemptsListsEachState(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createstates")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	seedCreateAttempt(t, seed, tenantID, conv, actorID, "in_flight", createAttemptSeed{})
	seedCreateAttempt(t, seed, tenantID, conv, actorID, "outcome_unknown", createAttemptSeed{})
	seedCreateAttempt(t, seed, tenantID, conv, actorID, "confirmed_success", createAttemptSeed{
		provider: strp("k3g"), externalTicketID: strp("111"),
	})
	seedCreateAttempt(t, seed, tenantID, conv, actorID, "confirmed_failure", createAttemptSeed{})

	h := NewHandler(app)
	for _, state := range []string{"in_flight", "outcome_unknown", "confirmed_success", "confirmed_failure"} {
		rec := callAsTenant(t, app, tenantID, adminID,
			"/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create?state="+state, h.ListCreateAttempts)
		if rec.Code != http.StatusOK {
			t.Fatalf("state=%s: HTTP %d: %s", state, rec.Code, rec.Body.String())
		}
		p := decodeReconciliationPage(t, rec)
		if len(p.Items) != 1 || p.Items[0]["state"] != state {
			t.Fatalf("state=%s: expected exactly 1 matching row, got %v", state, p.Items)
		}
	}
}

// E: local_ticket_id NULL row remains visible
func TestCreateAttemptsNullLocalTicketRemainsVisible(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createnulllocal")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	seedCreateAttempt(t, seed, tenantID, conv, actorID, "in_flight", createAttemptSeed{})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create", h.ListCreateAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 {
		t.Fatalf("expected 1 row, got %d", len(p.Items))
	}
	if p.Items[0]["local_ticket_id"] != nil {
		t.Fatalf("local_ticket_id should be null, got %v", p.Items[0]["local_ticket_id"])
	}
	if p.Items[0]["local_ticket_subject"] != nil {
		t.Fatalf("local_ticket_subject should be null when there is no ticket, got %v", p.Items[0]["local_ticket_subject"])
	}
}

// F: confirmed_success + projection_synced_at NULL visible
func TestCreateAttemptsConfirmedSuccessUnsyncedVisible(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createunsynced")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	seedCreateAttempt(t, seed, tenantID, conv, actorID, "confirmed_success", createAttemptSeed{
		provider: strp("k3g"), externalTicketID: strp("222"),
	})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create?state=confirmed_success", h.ListCreateAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 {
		t.Fatalf("expected 1 row, got %d", len(p.Items))
	}
	if p.Items[0]["state"] != "confirmed_success" {
		t.Fatalf("state = %v, want confirmed_success", p.Items[0]["state"])
	}
	if p.Items[0]["projection_synced_at"] != nil {
		t.Fatalf("projection_synced_at should be null (unsynced), got %v", p.Items[0]["projection_synced_at"])
	}
}

// G: provider filter
func TestCreateAttemptsProviderFilter(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createproviderfilter")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	seedCreateAttempt(t, seed, tenantID, conv, actorID, "confirmed_success", createAttemptSeed{provider: strp("k3g"), externalTicketID: strp("1")})
	seedCreateAttempt(t, seed, tenantID, conv, actorID, "confirmed_success", createAttemptSeed{provider: strp("ixc"), externalTicketID: strp("2")})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create?provider=ixc", h.ListCreateAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 || p.Items[0]["provider"] != "ixc" {
		t.Fatalf("provider=ixc should return exactly the ixc row, got %v", p.Items)
	}
}

// H: external_ticket_id exact filter
func TestCreateAttemptsExternalIDExactFilter(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createextidfilter")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	seedCreateAttempt(t, seed, tenantID, conv, actorID, "confirmed_success", createAttemptSeed{provider: strp("k3g"), externalTicketID: strp("28180")})
	seedCreateAttempt(t, seed, tenantID, conv, actorID, "confirmed_success", createAttemptSeed{provider: strp("k3g"), externalTicketID: strp("99999")})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create?external_ticket_id=28180", h.ListCreateAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 || p.Items[0]["external_ticket_id"] != "28180" {
		t.Fatalf("external_ticket_id=28180 should return exactly that row, got %v", p.Items)
	}
}

// I: date range
func TestCreateAttemptsDateRangeFilter(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createdaterange")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	old := time.Now().UTC().Add(-48 * time.Hour)
	recent := time.Now().UTC()
	seedCreateAttempt(t, seed, tenantID, conv, actorID, "in_flight", createAttemptSeed{createdAt: &old})
	seedCreateAttempt(t, seed, tenantID, conv, actorID, "in_flight", createAttemptSeed{createdAt: &recent})

	h := NewHandler(app)
	from := recent.Add(-1 * time.Hour).Format(time.RFC3339)
	rec := callAsTenant(t, app, tenantID, adminID,
		"/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create?created_from="+from, h.ListCreateAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 {
		t.Fatalf("created_from filter should exclude the old row, got %d rows", len(p.Items))
	}
}

// J: cursor pagination — first page, next page, no duplicates, no missing rows
func TestCreateAttemptsCursorPagination(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createpagination")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	base := time.Now().UTC()
	ids := make([]uuid.UUID, 5)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		ids[i] = seedCreateAttempt(t, seed, tenantID, conv, actorID, "in_flight", createAttemptSeed{createdAt: &ts, updatedAt: &ts})
	}

	h := NewHandler(app)
	path := "/api/v1/tenants/" + tenantID.String() + "/ticket-reconciliation/create?limit=2"
	rec := callAsTenant(t, app, tenantID, adminID, path, h.ListCreateAttempts)
	p1 := decodeReconciliationPage(t, rec)
	if len(p1.Items) != 2 || !p1.HasMore {
		t.Fatalf("first page: expected 2 items + has_more, got %d items has_more=%v", len(p1.Items), p1.HasMore)
	}

	seen := map[string]bool{}
	for _, it := range p1.Items {
		seen[it["id"].(string)] = true
	}

	rec2 := callAsTenant(t, app, tenantID, adminID, path+"&cursor="+p1.NextCursor, h.ListCreateAttempts)
	p2 := decodeReconciliationPage(t, rec2)
	for _, it := range p2.Items {
		id := it["id"].(string)
		if seen[id] {
			t.Fatalf("duplicate row %s across pages", id)
		}
		seen[id] = true
	}

	rec3 := callAsTenant(t, app, tenantID, adminID, path+"&cursor="+p2.NextCursor, h.ListCreateAttempts)
	p3 := decodeReconciliationPage(t, rec3)
	for _, it := range p3.Items {
		seen[it["id"].(string)] = true
	}

	if len(seen) != 5 {
		t.Fatalf("expected all 5 seeded rows visible across pages exactly once, got %d", len(seen))
	}
}

// K: tenant isolation
func TestCreateAttemptsTenantIsolation(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA := seedTenant(t, seed, "createisoA")
	tenantB := seedTenant(t, seed, "createisoB")
	adminA := seedMember(t, seed, tenantA, "tenant_admin", "active")
	actorB := seedMember(t, seed, tenantB, "tenant_agent", "active")
	convB := seedConversation(t, seed, tenantB)

	seedCreateAttempt(t, seed, tenantB, convB, actorB, "confirmed_success", createAttemptSeed{
		provider: strp("k3g"), externalTicketID: strp("SECRET-B"),
	})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantA, adminA, "/api/v1/tenants/"+tenantA.String()+"/ticket-reconciliation/create", h.ListCreateAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 0 {
		t.Fatalf("tenant A must never see tenant B's create attempts, got %v", p.Items)
	}
	if strings.Contains(rec.Body.String(), "SECRET-B") {
		t.Fatalf("tenant B's external_ticket_id must never appear in tenant A's response: %s", rec.Body.String())
	}
}

// L: actor display projection
func TestCreateAttemptsActorProjection(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createactor")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	seedCreateAttempt(t, seed, tenantID, conv, actorID, "in_flight", createAttemptSeed{})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create", h.ListCreateAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 {
		t.Fatalf("expected 1 row, got %d", len(p.Items))
	}
	actor, ok := p.Items[0]["actor"].(map[string]any)
	if !ok {
		t.Fatalf("actor field missing or wrong shape: %v", p.Items[0]["actor"])
	}
	if actor["id"] != actorID.String() {
		t.Fatalf("actor.id = %v, want %s", actor["id"], actorID.String())
	}
	if _, hasEmail := actor["email"]; !hasEmail {
		t.Fatalf("actor.email missing")
	}
}

// M: full idempotency key absent
func TestCreateAttemptsFullIdempotencyKeyAbsent(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createkeyredact")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	attemptID := seedCreateAttempt(t, seed, tenantID, conv, actorID, "in_flight", createAttemptSeed{})
	fullKey := "seed-create-" + attemptID.String()

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create", h.ListCreateAttempts)
	if strings.Contains(rec.Body.String(), fullKey) {
		t.Fatalf("full idempotency key must never appear in the response body: %s", rec.Body.String())
	}
	p := decodeReconciliationPage(t, rec)
	redacted, _ := p.Items[0]["idempotency_key_redacted"].(string)
	if redacted == "" || redacted == fullKey {
		t.Fatalf("idempotency_key_redacted = %q, want a redacted, non-full value", redacted)
	}
}

// N: request_hash absent
func TestCreateAttemptsRequestHashAbsent(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createhashabsent")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)

	seedCreateAttempt(t, seed, tenantID, conv, actorID, "in_flight", createAttemptSeed{})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create", h.ListCreateAttempts)
	if strings.Contains(rec.Body.String(), `"request_hash"`) {
		t.Fatalf("request_hash must never be serialized: %s", rec.Body.String())
	}
}

// Ticket subject enrichment (section 10)
func TestCreateAttemptsTicketSubjectEnrichment(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "createsubject")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)
	ticketID := seedTicket(t, seed, tenantID, "Preciso de ajuda", "open", "medium", time.Now().UTC())

	seedCreateAttempt(t, seed, tenantID, conv, actorID, "confirmed_success", createAttemptSeed{
		provider: strp("k3g"), externalTicketID: strp("333"), localTicketID: &ticketID,
	})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create", h.ListCreateAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 || p.Items[0]["local_ticket_subject"] != "Preciso de ajuda" {
		t.Fatalf("expected enriched subject, got %v", p.Items)
	}
}

// ============================================================
// Status attempt tests (section 16)
// ============================================================

func seedLinkedTicket(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, subject string) uuid.UUID {
	t.Helper()
	return seedTicket(t, pool, tenantID, subject, "open", "medium", time.Now().UTC())
}

func TestStatusAttemptsListsEachState(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "statusstates")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)
	// A separate local ticket per row: ticket_external_status_attempts_
	// blocking_local_ticket_uq allows at most one in_flight/outcome_unknown
	// row per (tenant_id, local_ticket_id) — the exact same real barrier
	// proven end to end in O2BH. in_flight and outcome_unknown can never
	// coexist for the SAME ticket, so each state here needs its own ticket.
	seedStatusAttempt(t, seed, tenantID, seedLinkedTicket(t, seed, tenantID, "Ticket in_flight"), conv, actorID, "k3g", "9115", "5", "in_flight", statusAttemptSeed{})
	seedStatusAttempt(t, seed, tenantID, seedLinkedTicket(t, seed, tenantID, "Ticket outcome_unknown"), conv, actorID, "k3g", "9115", "2", "outcome_unknown", statusAttemptSeed{})
	seedStatusAttempt(t, seed, tenantID, seedLinkedTicket(t, seed, tenantID, "Ticket confirmed_success"), conv, actorID, "k3g", "9115", "5", "confirmed_success", statusAttemptSeed{
		confirmedExternalStatus: strp("5"), confirmedExternalStatusLabel: strp("Resolvido"),
	})
	seedStatusAttempt(t, seed, tenantID, seedLinkedTicket(t, seed, tenantID, "Ticket confirmed_failure"), conv, actorID, "k3g", "9115", "6", "confirmed_failure", statusAttemptSeed{})

	h := NewHandler(app)
	for _, state := range []string{"in_flight", "outcome_unknown", "confirmed_success", "confirmed_failure"} {
		rec := callAsTenant(t, app, tenantID, adminID,
			"/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/status?state="+state, h.ListStatusAttempts)
		if rec.Code != http.StatusOK {
			t.Fatalf("state=%s: HTTP %d: %s", state, rec.Code, rec.Body.String())
		}
		p := decodeReconciliationPage(t, rec)
		if len(p.Items) != 1 || p.Items[0]["state"] != state {
			t.Fatalf("state=%s: expected exactly 1 matching row, got %v", state, p.Items)
		}
	}
}

func TestStatusAttemptsTargetStatusReturned(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "statustarget")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)
	ticketID := seedLinkedTicket(t, seed, tenantID, "Ticket")

	seedStatusAttempt(t, seed, tenantID, ticketID, conv, actorID, "k3g", "9115", "5", "in_flight", statusAttemptSeed{})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/status", h.ListStatusAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 || p.Items[0]["target_status"] != "5" {
		t.Fatalf("target_status = %v, want \"5\"", p.Items[0]["target_status"])
	}
}

func TestStatusAttemptsConfirmedFieldsReturnedWhereAvailable(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "statusconfirmed")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)
	ticketID := seedLinkedTicket(t, seed, tenantID, "Ticket")

	seedStatusAttempt(t, seed, tenantID, ticketID, conv, actorID, "k3g", "9115", "5", "confirmed_success", statusAttemptSeed{
		confirmedExternalStatus: strp("5"), confirmedExternalStatusLabel: strp("Resolvido"),
	})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/status?state=confirmed_success", h.ListStatusAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 {
		t.Fatalf("expected 1 row, got %d", len(p.Items))
	}
	if p.Items[0]["confirmed_external_status"] != "5" {
		t.Fatalf("confirmed_external_status = %v, want \"5\"", p.Items[0]["confirmed_external_status"])
	}
	if p.Items[0]["confirmed_external_status_label"] != "Resolvido" {
		t.Fatalf("confirmed_external_status_label = %v, want Resolvido", p.Items[0]["confirmed_external_status_label"])
	}
}

func TestStatusAttemptsConfirmedSuccessUnsyncedDetectable(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "statusunsynced")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)
	ticketID := seedLinkedTicket(t, seed, tenantID, "Ticket")

	seedStatusAttempt(t, seed, tenantID, ticketID, conv, actorID, "k3g", "9115", "5", "confirmed_success", statusAttemptSeed{
		confirmedExternalStatus: strp("5"), confirmedExternalStatusLabel: strp("Resolvido"),
		// projectionSyncedAt intentionally nil
	})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/status?state=confirmed_success", h.ListStatusAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 1 || p.Items[0]["projection_synced_at"] != nil {
		t.Fatalf("expected confirmed_success + unsynced (projection_synced_at null), got %v", p.Items)
	}
}

func TestStatusAttemptsTenantIsolation(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA := seedTenant(t, seed, "statusisoA")
	tenantB := seedTenant(t, seed, "statusisoB")
	adminA := seedMember(t, seed, tenantA, "tenant_admin", "active")
	actorB := seedMember(t, seed, tenantB, "tenant_agent", "active")
	convB := seedConversation(t, seed, tenantB)
	ticketB := seedLinkedTicket(t, seed, tenantB, "Tenant B Ticket")

	seedStatusAttempt(t, seed, tenantB, ticketB, convB, actorB, "k3g", "SECRET-B", "5", "confirmed_success", statusAttemptSeed{
		confirmedExternalStatus: strp("5"), confirmedExternalStatusLabel: strp("Resolvido"),
	})

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantA, adminA, "/api/v1/tenants/"+tenantA.String()+"/ticket-reconciliation/status", h.ListStatusAttempts)
	p := decodeReconciliationPage(t, rec)
	if len(p.Items) != 0 {
		t.Fatalf("tenant A must never see tenant B's status attempts, got %v", p.Items)
	}
	if strings.Contains(rec.Body.String(), "SECRET-B") {
		t.Fatalf("tenant B's external_ticket_id must never appear in tenant A's response: %s", rec.Body.String())
	}
}

func TestStatusAttemptsFullIdempotencyKeyAbsentAndRequestHashAbsent(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "statusredact")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)
	ticketID := seedLinkedTicket(t, seed, tenantID, "Ticket")

	attemptID := seedStatusAttempt(t, seed, tenantID, ticketID, conv, actorID, "k3g", "9115", "5", "in_flight", statusAttemptSeed{})
	fullKey := "seed-status-" + attemptID.String()

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/status", h.ListStatusAttempts)
	body := rec.Body.String()
	if strings.Contains(body, fullKey) {
		t.Fatalf("full idempotency key must never appear in the response body: %s", body)
	}
	if strings.Contains(body, `"request_hash"`) {
		t.Fatalf("request_hash must never be serialized: %s", body)
	}
}

func TestStatusAttemptsCursorPagination(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "statuspagination")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	actorID := seedMember(t, seed, tenantID, "tenant_agent", "active")
	conv := seedConversation(t, seed, tenantID)
	ticketID := seedLinkedTicket(t, seed, tenantID, "Ticket")

	// confirmed_success (unlike in_flight/outcome_unknown) is never blocked
	// by ticket_external_status_attempts_blocking_local_ticket_uq, so all 5
	// rows can safely share the same ticket here — this test is about
	// pagination correctness, not per-state filtering.
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		seedStatusAttempt(t, seed, tenantID, ticketID, conv, actorID, "k3g", "9115", "5", "confirmed_success", statusAttemptSeed{
			confirmedExternalStatus: strp("5"), confirmedExternalStatusLabel: strp("Resolvido"),
			createdAt: &ts, updatedAt: &ts,
		})
	}

	h := NewHandler(app)
	path := "/api/v1/tenants/" + tenantID.String() + "/ticket-reconciliation/status?limit=2"
	rec := callAsTenant(t, app, tenantID, adminID, path, h.ListStatusAttempts)
	p1 := decodeReconciliationPage(t, rec)
	if len(p1.Items) != 2 || !p1.HasMore {
		t.Fatalf("first page: expected 2 items + has_more, got %d items has_more=%v", len(p1.Items), p1.HasMore)
	}

	seen := map[string]bool{}
	for _, it := range p1.Items {
		seen[it["id"].(string)] = true
	}
	rec2 := callAsTenant(t, app, tenantID, adminID, path+"&cursor="+p1.NextCursor, h.ListStatusAttempts)
	p2 := decodeReconciliationPage(t, rec2)
	for _, it := range p2.Items {
		id := it["id"].(string)
		if seen[id] {
			t.Fatalf("duplicate row %s across pages", id)
		}
		seen[id] = true
	}
	rec3 := callAsTenant(t, app, tenantID, adminID, path+"&cursor="+p2.NextCursor, h.ListStatusAttempts)
	p3 := decodeReconciliationPage(t, rec3)
	for _, it := range p3.Items {
		seen[it["id"].(string)] = true
	}
	if len(seen) != 5 {
		t.Fatalf("expected all 5 seeded rows visible across pages exactly once, got %d", len(seen))
	}
}

// ============================================================
// Invalid filter -> canonical 400 (section 7)
// ============================================================

func TestReconciliationInvalidStateIsRejected(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "reconcilebadstate")
	adminID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/create?state=bogus", h.ListCreateAttempts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid state: HTTP %d, want 400", rec.Code)
	}

	rec2 := callAsTenant(t, app, tenantID, adminID, "/api/v1/tenants/"+tenantID.String()+"/ticket-reconciliation/status?state=bogus", h.ListStatusAttempts)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("invalid state (status): HTTP %d, want 400", rec2.Code)
	}
}
