package adapters

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/aiusage"
	aiusageadapters "github.com/omnira/omnira/internal/aiusage/adapters"
	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Regression for a Codex finding (round 5, CONTEXT.2D): WindowAnalyzerService.account() used to call
// PostgresLedger.Record() directly, which always opens its OWN pool connection. With ClaimLimit windows analyzed
// at once, each holding the outer session's connection while Record tries to acquire a second, a pool with no
// more connections than the claim batch deadlocks every one of them. ContextWindowRunner.WithUsageFlush fixes
// this by collecting usage on ctx (PendingUsage) and performing the actual Record calls itself AFTER each
// window's own session has released its connection. Proven here against a pool whose MaxConns EXACTLY equals the
// claim batch size — zero headroom for a second, nested connection — with a bounded context: the fixed runner
// must finish well within it; the old behavior would have left every window's session blocked acquiring that
// second connection until the deadline fired.
func TestContextWindowRunnerWithUsageFlushNeverDeadlocksWhenThePoolHasNoHeadroomBeyondTheClaimBatch(t *testing.T) {
	e := newEnv(t)
	_, appURL := testhelpers.RequireIntegrationDatabase(t)
	const claimLimit = 3
	cfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = claimLimit
	limited, err := pgxpool.NewWithConfig(e.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()

	windowStore := NewPostgresContextWindowStore(limited)
	ledger := aiusageadapters.NewPostgresLedger(limited)
	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1",
			Generator: tokenGen{out: `{"summary":"ok","facts":[{"topic_alias":"NEW","kind":"need","text":"algo","evidence":["M1"]}]}`, in: 10, outTok: 5}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(limited), NewPostgresTopicRepository(limited),
		NewPostgresSummaryRepository(limited), models).WithLedger(ledger)
	session := func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
		return platformdb.WithSystemTenantSession(ctx, limited, tenantID, fn)
	}
	runner := application.NewContextWindowRunner(windowStore, analyzer, session, application.ContextWindowRunnerConfig{
		Policy: contextwindow.DefaultPolicy(), SweepLimit: claimLimit, ClaimLimit: claimLimit, Lease: time.Minute,
		MaxAttempts: 1, BaseBackoff: time.Second, MaxBackoff: time.Second,
	}, nil).WithUsageFlush(func(ctx context.Context, items []aiusage.Record) error {
		for _, rec := range items {
			if err := ledger.Record(ctx, rec); err != nil {
				return err
			}
		}
		return nil
	})

	tenants := make([]uuid.UUID, claimLimit)
	for i := range tenants {
		a := e.tenant()
		tenants[i] = a.id
		m := e.message(a.id, a.conversation, fmt.Sprintf("mensagem %d", i))
		if err := windowStore.Touch(e.ctx, contextwindow.DefaultPolicy(), a.id, a.conversation, m, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if _, err := windowStore.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerManual); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	n, err := runner.ProcessOnce(ctx)
	if err != nil {
		t.Fatalf("ProcessOnce: %v (the old behavior would block here until the context's deadline)", err)
	}
	if n != claimLimit {
		t.Fatalf("claimed = %d, want %d", n, claimLimit)
	}
	for _, tenant := range tenants {
		if got := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND status='ok'`, tenant); got != 1 {
			t.Fatalf("tenant %s: analyses = %d, want 1 ok", tenant, got)
		}
		if got := e.count(`SELECT count(*) FROM ai_usage WHERE tenant_id=$1 AND task='context_window_analyze' AND success`, tenant); got != 1 {
			t.Fatalf("tenant %s: ledger rows = %d, want 1 (the usage flush must still run, just after the session closes)", tenant, got)
		}
	}
}
