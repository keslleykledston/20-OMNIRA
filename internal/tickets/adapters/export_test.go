package adapters

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedTicketsBulk seeds n tickets, each against its own conversation
// (tickets_active_conversation_uq allows only one open/in_progress/waiting
// ticket per conversation) sharing a single contact — cheap enough to reach
// the 5000/5001-row ceiling tests.
func seedTicketsBulk(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, n int, status, priority string) {
	t.Helper()
	ctx := context.Background()
	contactID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email, status) VALUES ($1,$2,$3,$4,'','active')`,
		contactID, tenantID, "Bulk "+contactID.String()[:8], nextPhone()); err != nil {
		t.Fatalf("seed bulk contact: %v", err)
	}

	convBatch := &pgx.Batch{}
	conversationIDs := make([]uuid.UUID, n)
	for i := 0; i < n; i++ {
		conversationIDs[i] = uuid.New()
		convBatch.Queue(
			`INSERT INTO conversations (id, tenant_id, contact_id, status) VALUES ($1,$2,$3,'closed')`,
			conversationIDs[i], tenantID, contactID)
	}
	cbr := pool.SendBatch(ctx, convBatch)
	for i := 0; i < n; i++ {
		if _, err := cbr.Exec(); err != nil {
			cbr.Close()
			t.Fatalf("seed bulk conversation %d: %v", i, err)
		}
	}
	cbr.Close()

	base := time.Now().UTC().Add(-time.Duration(n) * time.Second)
	ticketBatch := &pgx.Batch{}
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		ticketBatch.Queue(
			`INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
			uuid.New(), tenantID, conversationIDs[i], status, priority, "Bulk ticket", ts)
	}
	tbr := pool.SendBatch(ctx, ticketBatch)
	defer tbr.Close()
	for i := 0; i < n; i++ {
		if _, err := tbr.Exec(); err != nil {
			t.Fatalf("seed bulk ticket %d: %v", i, err)
		}
	}
}

func TestExportCSVRequiresTicketRead(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportperm")
	admin := seedMember(t, seed, tenantID, "tenant_admin", "active")
	supervisor := seedMember(t, seed, tenantID, "tenant_supervisor", "active")
	agent := seedMember(t, seed, tenantID, "tenant_agent", "active")
	seedTicket(t, seed, tenantID, "Ticket 1", "open", "medium", time.Now().UTC())

	h := NewHandler(app)
	target := "/api/v1/tenants/" + tenantID.String() + "/tickets/export.csv"

	if rec := callAsTenant(t, app, tenantID, admin, target, h.ExportCSV); rec.Code != http.StatusOK {
		t.Fatalf("tenant_admin (has ticket.read) = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if rec := callAsTenant(t, app, tenantID, supervisor, target, h.ExportCSV); rec.Code != http.StatusOK {
		t.Fatalf("tenant_supervisor (has ticket.read) = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if rec := callAsTenant(t, app, tenantID, agent, target, h.ExportCSV); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_agent (no ticket.read) = %d %q, want 403", rec.Code, rec.Body.String())
	}
}

func parseCSV(t *testing.T, rec *httptest.ResponseRecorder) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v (body=%s)", err, rec.Body.String())
	}
	return rows
}

func TestExportCSVHeaderAndRow(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportrow")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	ticketID := seedTicket(t, seed, tenantID, "Export me", "open", "high", time.Now().UTC())

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv", h.ExportCSV)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="tickets.csv"` {
		t.Fatalf("content-disposition = %q", cd)
	}

	rows := parseCSV(t, rec)
	wantHeader := []string{"id", "conversation_id", "subject", "status", "priority", "assigned_to", "created_at", "updated_at"}
	if len(rows) < 1 {
		t.Fatalf("expected at least a header row")
	}
	for i, col := range wantHeader {
		if rows[0][i] != col {
			t.Fatalf("header[%d] = %q, want %q", i, rows[0][i], col)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("expected 1 data row, got %d", len(rows)-1)
	}
	if rows[1][0] != ticketID.String() {
		t.Fatalf("row id = %q, want %q", rows[1][0], ticketID.String())
	}
	if rows[1][2] != "Export me" {
		t.Fatalf("row subject = %q", rows[1][2])
	}
	if rows[1][5] != "" {
		t.Fatalf("assigned_to should be empty for an unassigned ticket, got %q", rows[1][5])
	}
	if _, err := time.Parse(time.RFC3339, rows[1][6]); err != nil {
		t.Fatalf("created_at not RFC3339: %v", err)
	}
}

func TestExportCSVIsScopedToTheSessionTenant(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantA, tenantB := seedTenant(t, seed, "exportA"), seedTenant(t, seed, "exportB")
	userA := seedMember(t, seed, tenantA, "tenant_admin", "active")
	seedTicket(t, seed, tenantA, "Tenant A", "open", "medium", time.Now().UTC())
	seedTicket(t, seed, tenantB, "Tenant B", "open", "medium", time.Now().UTC())

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantA, userA, "/api/v1/tenants/"+tenantA.String()+"/tickets/export.csv", h.ExportCSV)
	rows := parseCSV(t, rec)
	if len(rows) != 2 || rows[1][2] != "Tenant A" {
		t.Fatalf("tenant A export should contain exactly its own ticket, got %v", rows)
	}
}

func TestExportCSVStatusFilter(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportstatus")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	now := time.Now().UTC()
	seedTicket(t, seed, tenantID, "Open one", "open", "medium", now)
	seedTicket(t, seed, tenantID, "Closed one", "closed", "medium", now)

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv?status=open", h.ExportCSV)
	rows := parseCSV(t, rec)
	if len(rows) != 2 || rows[1][2] != "Open one" {
		t.Fatalf("status=open export should return exactly the open ticket, got %v", rows)
	}
}

func TestExportCSVPriorityFilter(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportpriority")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	now := time.Now().UTC()
	seedTicket(t, seed, tenantID, "Critical one", "open", "critical", now)
	seedTicket(t, seed, tenantID, "Low one", "open", "low", now)

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv?priority=critical", h.ExportCSV)
	rows := parseCSV(t, rec)
	if len(rows) != 2 || rows[1][2] != "Critical one" {
		t.Fatalf("priority=critical export should return exactly the critical ticket, got %v", rows)
	}
}

func TestExportCSVInvalidStatusIsRejected(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportbadstatus")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv?status=bogus", h.ExportCSV)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d %q, want 400", rec.Code, rec.Body.String())
	}
}

func TestExportCSVDeterministicOrdering(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportorder")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		seedTicket(t, seed, tenantID, "Order "+string(rune('A'+i)), "open", "medium", base.Add(time.Duration(i)*time.Minute))
	}

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv", h.ExportCSV)
	rows := parseCSV(t, rec)
	if len(rows) != 4 {
		t.Fatalf("expected 3 data rows, got %d", len(rows)-1)
	}
	// updated_at DESC, id DESC: newest seeded ("Order C") first.
	if rows[1][2] != "Order C" || rows[3][2] != "Order A" {
		t.Fatalf("unexpected order: %v", rows)
	}
}

func TestExportCSVEscapesCommasQuotesAndNewlines(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportescape")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	subject := "Hello, \"World\"\nSecond line"
	seedTicket(t, seed, tenantID, subject, "open", "medium", time.Now().UTC())

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv", h.ExportCSV)
	rows := parseCSV(t, rec)
	if len(rows) != 2 {
		t.Fatalf("expected 1 data row, got %d", len(rows)-1)
	}
	if rows[1][2] != subject {
		t.Fatalf("subject round-trip = %q, want %q", rows[1][2], subject)
	}
}

func TestExportCSVNeutralizesFormulaInjection(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		want    string // "" means: expect the value unchanged
	}{
		{"direct equals", "=SUM(A1:A10)", "'=SUM(A1:A10)"},
		{"direct plus", "+1+1", "'+1+1"},
		{"direct minus", "-1+1", "'-1+1"},
		{"direct at", "@SUM(A1:A10)", "'@SUM(A1:A10)"},
		{"leading space then equals", " =1+1", "' =1+1"},
		{"leading tab then equals", "\t=1+1", "'\t=1+1"},
		{"leading CR then equals", "\r=1+1", "'\r=1+1"},
		{"ordinary subject unchanged", "Preciso de ajuda", ""},
		{"subject containing but not starting with equals", "valor = 10", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed, app := seedPool(t), appPool(t)
			tenantID := seedTenant(t, seed, "exportformula")
			userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
			seedTicket(t, seed, tenantID, tc.subject, "open", "medium", time.Now().UTC())

			h := NewHandler(app)
			rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv", h.ExportCSV)
			rows := parseCSV(t, rec)
			if len(rows) != 2 {
				t.Fatalf("expected 1 data row, got %d", len(rows)-1)
			}
			want := tc.want
			if want == "" {
				want = tc.subject
			}
			if rows[1][2] != want {
				t.Fatalf("subject %q serialized as %q, want %q", tc.subject, rows[1][2], want)
			}
		})
	}
}

func TestExportCSVEmptyResultReturnsHeaderOnly(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportempty")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv", h.ExportCSV)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty export = %d %q, want 200", rec.Code, rec.Body.String())
	}
	rows := parseCSV(t, rec)
	if len(rows) != 1 {
		t.Fatalf("expected header-only CSV, got %d rows", len(rows))
	}
}

func TestExportCSVExactlyAtCeilingSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 5000-row export ceiling test in -short mode")
	}
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportceiling")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	seedTicketsBulk(t, seed, tenantID, exportMaxRows, "open", "medium")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv", h.ExportCSV)
	if rec.Code != http.StatusOK {
		t.Fatalf("export at exactly %d rows = %d %q, want 200", exportMaxRows, rec.Code, rec.Body.String())
	}
	rows := parseCSV(t, rec)
	if len(rows)-1 != exportMaxRows {
		t.Fatalf("expected %d data rows, got %d", exportMaxRows, len(rows)-1)
	}
}

func TestExportCSVOverCeilingIsRejectedWithoutPartialData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 5001-row export ceiling test in -short mode")
	}
	seed, app := seedPool(t), appPool(t)
	tenantID := seedTenant(t, seed, "exportoverceiling")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")
	seedTicketsBulk(t, seed, tenantID, exportMaxRows+1, "open", "medium")

	h := NewHandler(app)
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets/export.csv", h.ExportCSV)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("export over ceiling = %d %q, want 413", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct == "text/csv; charset=utf-8" {
		t.Fatalf("must not set the CSV content-type on a rejected export")
	}
	if strings.Contains(rec.Body.String(), "Bulk ticket") {
		t.Fatalf("rejected export must not leak partial data")
	}
}
