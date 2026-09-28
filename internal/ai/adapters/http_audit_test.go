package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
)

func TestAISummary_Audit_ContainsSafeFactsOnly(t *testing.T) {
	f := setupAIFixture(t)
	gen := &fakeGenerator{text: "este é o resumo gerado, nunca deve aparecer no audit"}
	mux := f.serve(gen, nil)
	code, _ := f.post(mux, f.userA, f.tenantA, f.convA, true)
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}

	rows, err := f.seed.Query(context.Background(),
		`SELECT tenant_id, actor_id, action, resource_type, resource_id, outcome, metadata
		 FROM audit_events WHERE tenant_id=$1 AND resource_id=$2 AND action='ai.conversation.summarize' ORDER BY created_at`,
		f.tenantA, f.convA)
	if err != nil {
		t.Fatalf("query audit_events: %v", err)
	}
	defer rows.Close()

	type row struct {
		tenantID, actorID, action, resourceType, resourceID, outcome string
		metadata                                                     []byte
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.tenantID, &r.actorID, &r.action, &r.resourceType, &r.resourceID, &r.outcome, &r.metadata); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 audit rows (requested + completed), got %d", len(got))
	}

	for i, r := range got {
		if r.tenantID != f.tenantA.String() {
			t.Fatalf("row %d: tenant_id = %s", i, r.tenantID)
		}
		if r.actorID != f.userA.String() {
			t.Fatalf("row %d: actor_id = %s, want the human user who triggered the request", i, r.actorID)
		}
		if r.resourceType != "conversation" {
			t.Fatalf("row %d: resource_type = %s", i, r.resourceType)
		}
		if r.resourceID != f.convA.String() {
			t.Fatalf("row %d: resource_id = %s", i, r.resourceID)
		}
		metaStr := string(r.metadata)
		if strings.Contains(metaStr, "Preciso de ajuda") || strings.Contains(metaStr, "qual o número do pedido") {
			t.Fatalf("row %d: audit metadata contains real message body content: %s", i, metaStr)
		}
		if strings.Contains(metaStr, "resumo gerado") {
			t.Fatalf("row %d: audit metadata contains the generated summary: %s", i, metaStr)
		}
		if strings.Contains(metaStr, "+5511") {
			t.Fatalf("row %d: audit metadata contains a phone number: %s", i, metaStr)
		}
		var meta map[string]any
		if err := json.Unmarshal(r.metadata, &meta); err != nil {
			t.Fatalf("row %d: metadata is not valid JSON: %v", i, err)
		}
		if meta["provider"] != "openai" {
			t.Fatalf("row %d: metadata.provider = %v", i, meta["provider"])
		}
	}
	if got[0].outcome != "success" {
		t.Fatalf("first (pre-call) row outcome = %s, want success", got[0].outcome)
	}
	if got[1].outcome != "success" {
		t.Fatalf("second (post-call) row outcome = %s, want success for a successful generation", got[1].outcome)
	}
}

func TestAISummary_Audit_FailureRecordsFailureOutcome(t *testing.T) {
	f := setupAIFixture(t)
	gen := &fakeGenerator{err: errAny}
	mux := f.serve(gen, nil)
	f.post(mux, f.userA, f.tenantA, f.convA, true)

	var outcome string
	err := f.seed.QueryRow(context.Background(),
		`SELECT outcome FROM audit_events WHERE tenant_id=$1 AND resource_id=$2 AND action='ai.conversation.summarize' AND metadata->>'phase'='completed' ORDER BY created_at DESC LIMIT 1`,
		f.tenantA, f.convA).Scan(&outcome)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if outcome != "failure" {
		t.Fatalf("outcome = %s, want failure", outcome)
	}
}

var errAny = &genericErr{"boom"}

type genericErr struct{ msg string }

func (e *genericErr) Error() string { return e.msg }

// postCallFailingAuditRepo lets the PRE-CALL ("requested") write succeed
// against the real repository but fails only the POST-CALL ("completed")
// write — proving the two writes fail independently (PRODUCT.7C1 FINAL
// COMMIT CORRECTION GATE §2).
type postCallFailingAuditRepo struct {
	inner auditports.AuditEventRepository
}

func (r postCallFailingAuditRepo) Store(ctx context.Context, event *auditdomain.AuditEvent) error {
	if event.Metadata != nil {
		if phase, _ := event.Metadata["phase"].(string); phase == "completed" {
			return errors.New("simulated post-call audit storage failure")
		}
	}
	return r.inner.Store(ctx, event)
}
func (r postCallFailingAuditRepo) FindByID(ctx context.Context, id uuid.UUID) (*auditdomain.AuditEvent, error) {
	return r.inner.FindByID(ctx, id)
}
func (r postCallFailingAuditRepo) FindByTenantAndCorrelation(ctx context.Context, tenantID, correlationID uuid.UUID) ([]*auditdomain.AuditEvent, error) {
	return r.inner.FindByTenantAndCorrelation(ctx, tenantID, correlationID)
}
func (r postCallFailingAuditRepo) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*auditdomain.AuditEvent, error) {
	return r.inner.FindByTenant(ctx, tenantID, limit, offset)
}
func (r postCallFailingAuditRepo) FindByAction(ctx context.Context, tenantID uuid.UUID, action auditdomain.AuditAction, limit, offset int) ([]*auditdomain.AuditEvent, error) {
	return r.inner.FindByAction(ctx, tenantID, action, limit, offset)
}

// TestAISummary_PostCallAuditFailure_DoesNotAffectResponseOrRetry proves the
// invariant required by the FINAL COMMIT CORRECTION GATE: a successful
// provider call followed by a FAILED post-call audit write must still
// return the real summary (never an error, never a retry, never a second
// provider call) — because the pre-call write already proves the external
// transfer occurred, independent of whether the outcome write succeeds.
func TestAISummary_PostCallAuditFailure_DoesNotAffectResponseOrRetry(t *testing.T) {
	f := setupAIFixture(t)
	repo := postCallFailingAuditRepo{inner: auditadapters.NewPostgresAuditEventRepository(f.app)}
	gen := &fakeGenerator{text: "resumo apesar da falha de auditoria pós-chamada"}
	mux := f.serve(gen, repo)

	code, body := f.post(mux, f.userA, f.tenantA, f.convA, true)
	if code != http.StatusOK {
		t.Fatalf("code = %d, body = %s, want 200 — a post-call audit failure must never turn a successful provider result into an error", code, body)
	}
	if !strings.Contains(body, "resumo apesar da falha de auditoria pós-chamada") {
		t.Fatalf("body = %s, want the real summary despite the post-call audit failure", body)
	}
	if gen.calls != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 (no automatic retry triggered by the audit failure)", gen.calls)
	}

	var requestedCount, completedCount int
	if err := f.seed.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2 AND action='ai.conversation.summarize' AND metadata->>'phase'='requested'`,
		f.tenantA, f.convA).Scan(&requestedCount); err != nil {
		t.Fatalf("query requested: %v", err)
	}
	if requestedCount != 1 {
		t.Fatalf("pre-call audit rows = %d, want 1 — the external transfer must remain provably recorded even when the post-call write fails", requestedCount)
	}
	if err := f.seed.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2 AND action='ai.conversation.summarize' AND metadata->>'phase'='completed'`,
		f.tenantA, f.convA).Scan(&completedCount); err != nil {
		t.Fatalf("query completed: %v", err)
	}
	if completedCount != 0 {
		t.Fatalf("post-call audit rows = %d, want 0 — the simulated failure should have actually prevented this write", completedCount)
	}
}
