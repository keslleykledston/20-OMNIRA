package adapters

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/media/ports"
)

type xorCipher struct{}

func (xorCipher) Decrypt(b []byte) ([]byte, error) {
	if len(b) > 0 && b[0] == 'X' {
		return nil, errors.New("bad ciphertext")
	}
	return b, nil
}

func (e *menv) cleanMedia(tenant, conversation uuid.UUID, mime, kind string) uuid.UUID {
	msg := e.message(tenant, conversation, "inbound", "http://localhost:3000/api/files/s/"+uuid.NewString())
	e.exec(`UPDATE message_media SET status='clean', kind=$2, mime=$3, fetched_at=now(), scanned_at=now() WHERE message_id=$1`, msg, kind, mime)
	return msg
}

func (e *menv) optIn(tenant uuid.UUID, enabled bool, secret string) {
	if enabled {
		e.exec(`INSERT INTO tenant_ai_integrations(tenant_id,provider,enabled,secret_ciphertext,consent_version,consent_at) VALUES($1,'gemini',true,$2,'v1',now())`, tenant, []byte(secret))
		return
	}
	e.exec(`INSERT INTO tenant_ai_integrations(tenant_id,provider,enabled) VALUES($1,'gemini',false)`, tenant)
}

func TestVisionJobsExistOnlyForOptedInTenantsAndAllowListedFiles(t *testing.T) {
	e := newMEnv(t)
	on, convOn := e.tenant()
	off, convOff := e.tenant()
	none, convNone := e.tenant()
	e.optIn(on, true, "key-do-tenant-1234567890")
	e.optIn(off, false, "")
	repo := NewPostgresRepository(e.app)
	_ = none

	png := e.cleanMedia(on, convOn, "image/png", "image")
	pdf := e.cleanMedia(on, convOn, "application/pdf", "document")
	gif := e.cleanMedia(on, convOn, "image/gif", "image")
	audio := e.cleanMedia(on, convOn, "audio/ogg", "audio")
	pending := e.message(on, convOn, "inbound", "http://localhost:3000/api/files/s/p") // not cleared yet
	_ = pending
	offImg := e.cleanMedia(off, convOff, "image/png", "image")
	noneImg := e.cleanMedia(none, convNone, "image/png", "image")
	old := e.cleanMedia(on, convOn, "image/jpeg", "image")
	e.exec(`UPDATE message_media SET created_at = now() - interval '30 days' WHERE message_id=$1`, old)
	purged := e.cleanMedia(on, convOn, "image/jpeg", "image")
	e.exec(`UPDATE message_media SET file_purged_at = now() WHERE message_id=$1`, purged)

	n, err := repo.EnqueueVision(e.ctx, time.Now().Add(-7*24*time.Hour), 50)
	if err != nil || n != 2 {
		t.Fatalf("enqueued %d (%v), want exactly the opted-in tenant's recent png and pdf", n, err)
	}
	kind := func(msg uuid.UUID) string {
		var k string
		_ = e.seed.QueryRow(e.ctx, `SELECT kind FROM message_media_analysis WHERE message_id=$1 AND kind IN ('description','document_text')`, msg).Scan(&k)
		return k
	}
	if kind(png) != "description" || kind(pdf) != "document_text" {
		t.Fatalf("kinds: png=%q pdf=%q", kind(png), kind(pdf))
	}
	for name, msg := range map[string]uuid.UUID{"gif": gif, "audio": audio, "opted-out tenant": offImg, "tenant without a row": noneImg, "too old": old, "purged": purged} {
		if kind(msg) != "" {
			t.Errorf("%s must not get a vision job", name)
		}
	}
	// idempotent: running again creates nothing
	if n, err := repo.EnqueueVision(e.ctx, time.Now().Add(-7*24*time.Hour), 50); err != nil || n != 0 {
		t.Fatalf("second run created %d", n)
	}
	// the batch limit is honored
	for i := 0; i < 5; i++ {
		e.cleanMedia(on, convOn, "image/jpeg", "image")
	}
	if n, _ := repo.EnqueueVision(e.ctx, time.Now().Add(-7*24*time.Hour), 3); n != 3 {
		t.Fatalf("limit: %d", n)
	}
}

func TestVisionJobsAreClaimedSkippedAndCannotBeWrittenByOperators(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	e.optIn(tenant, true, "key-do-tenant-1234567890")
	repo := NewPostgresRepository(e.app)
	msg := e.cleanMedia(tenant, conv, "image/png", "image")
	if _, err := repo.EnqueueVision(e.ctx, time.Now().Add(-time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	items, err := repo.ClaimAnalysis(e.ctx, "description", 5, time.Minute)
	if err != nil || len(items) != 1 || items[0].Mime != "image/png" || items[0].MessageID != msg {
		t.Fatalf("claim: %+v %v", items, err)
	}
	if again, _ := repo.ClaimAnalysis(e.ctx, "description", 5, time.Minute); len(again) != 0 {
		t.Fatal("a leased job must not be claimed twice")
	}
	if err := repo.SkipAnalysis(e.ctx, items[0], "budget_exceeded"); err != nil {
		t.Fatal(err)
	}
	var st, reason string
	_ = e.seed.QueryRow(e.ctx, `SELECT status, reason FROM message_media_analysis WHERE message_id=$1 AND kind='description'`, msg).Scan(&st, &reason)
	if st != "skipped" || reason != "budget_exceeded" {
		t.Fatalf("skip: %s %s", st, reason)
	}
	if err := repo.SkipAnalysis(e.ctx, items[0], "again"); err == nil {
		t.Fatal("a job that is no longer pending cannot be skipped again")
	}
}

func TestTenantAIResolverReturnsOnlyAnEnabledReadableIntegration(t *testing.T) {
	e := newMEnv(t)
	on, _ := e.tenant()
	off, _ := e.tenant()
	broken, _ := e.tenant()
	absent, _ := e.tenant()
	e.optIn(on, true, "key-do-tenant-1234567890")
	e.optIn(off, false, "")
	e.optIn(broken, true, "Xcorrupted")
	r := NewTenantAIResolver(e.app, xorCipher{})
	got, err := r.Resolve(e.ctx, on)
	if err != nil || got == nil || got.APIKey != "key-do-tenant-1234567890" || got.Model != "gemini-2.5-flash" || got.BudgetUSD != 10 {
		t.Fatalf("enabled: %+v %v", got, err)
	}
	for name, id := range map[string]uuid.UUID{"disabled": off, "unreadable key": broken, "no row": absent, "unknown tenant": uuid.New()} {
		if got, err := r.Resolve(e.ctx, id); err != nil || got != nil {
			t.Errorf("%s: %+v %v, want nothing", name, got, err)
		}
	}
	// it answers for the asked tenant only
	if got, _ := r.Resolve(e.ctx, off); got != nil {
		t.Fatal("tenant isolation")
	}
	var _ ports.TenantAIResolver = r
	_ = context.Background
}

// Codex H2 / ADR-0038: vision/transcription jobs of a suspended company are not claimed (they cost money and write results).
func TestASuspendedCompanysAnalysisJobsAreNotClaimedUntilReactivated(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	e.optIn(tenant, true, "key-do-tenant-1234567890")
	repo := NewPostgresRepository(e.app)
	msg := e.cleanMedia(tenant, conv, "image/png", "image")
	if _, err := repo.EnqueueVision(e.ctx, time.Now().Add(-time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, tenant)
	items, err := repo.ClaimAnalysis(e.ctx, "description", 1000, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.MessageID == msg {
			t.Fatalf("a suspended company's analysis job was claimed: %+v", it)
		}
	}
	e.exec(`UPDATE tenants SET status='active' WHERE id=$1`, tenant)
	items, err = repo.ClaimAnalysis(e.ctx, "description", 1000, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		found = found || it.MessageID == msg
	}
	if !found {
		t.Fatal("after the reactivation the analysis job must be claimed")
	}
}

// Codex H / ADR-0038: no analysis job is even created for a suspended company, and the repository answers the gate honestly.
func TestASuspendedCompanyGetsNoAnalysisJobAndTheGateSaysSo(t *testing.T) {
	e := newMEnv(t)
	tenant, conv := e.tenant()
	e.optIn(tenant, true, "key-do-tenant-1234567890")
	repo := NewPostgresRepository(e.app)
	msg := e.cleanMedia(tenant, conv, "image/png", "image")
	if ok, err := repo.TenantActive(e.ctx, tenant); err != nil || !ok {
		t.Fatalf("an active company: %v %v", ok, err)
	}
	e.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, tenant)
	if ok, err := repo.TenantActive(e.ctx, tenant); err != nil || ok {
		t.Fatalf("a suspended company: %v %v", ok, err)
	}
	if _, err := repo.EnqueueVision(e.ctx, time.Now().Add(-time.Hour), 1000); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM message_media_analysis WHERE message_id=$1`, msg).Scan(&n)
	if n != 0 {
		t.Fatalf("a suspended company got %d analysis job(s)", n)
	}
	e.exec(`UPDATE tenants SET status='active' WHERE id=$1`, tenant)
	if _, err := repo.EnqueueVision(e.ctx, time.Now().Add(-time.Hour), 1000); err != nil {
		t.Fatal(err)
	}
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM message_media_analysis WHERE message_id=$1`, msg).Scan(&n)
	if n != 1 {
		t.Fatalf("after the reactivation the job must be created, got %d", n)
	}
}
