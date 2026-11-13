package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	aiports "github.com/omnira/omnira/internal/ai/ports"
	aiusageadapters "github.com/omnira/omnira/internal/aiusage/adapters"
	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// CONTEXT.2B end to end: a real closed window (CONTEXT.2A's own Touch/Force/Claim), analyzed against real Postgres
// (topics, summaries, analysis storage), with only the model call faked (tokenGen, already defined in this package
// by ai_usage_integration_test.go) so the proposal is deterministic.

func claimOneDueWindow(t *testing.T, e *env, tenant, conversation, msg uuid.UUID, policy contextwindow.Policy) ports.ContextWindow {
	t.Helper()
	store := NewPostgresContextWindowStore(e.app)
	if _, err := sys(e, tenant, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, store.Touch(ctx, policy, tenant, conversation, msg, time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sys(e, tenant, func(ctx context.Context) (bool, error) {
		return store.Force(ctx, tenant, conversation, contextwindow.TriggerManual)
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := sys(e, tenant, func(ctx context.Context) ([]ports.ContextWindow, error) { return store.Claim(ctx, 1, time.Minute) })
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: n=%d err=%v", len(claimed), err)
	}
	return claimed[0]
}

func TestWindowAnalyzerCreatesANewTopicLinksEvidenceAndAccountsUsage(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "minha internet caiu de novo")
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	gen := tokenGen{out: `{"summary":"Cliente relata queda de internet","facts":[{"topic_alias":"NEW","kind":"need","text":"Cliente ficou sem internet","evidence":["M1"]}]}`, in: 300, outTok: 60}
	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: gen, MaxOutputTokens: 500})
	ledger := aiusageadapters.NewPostgresLedger(e.app)
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models).WithLedger(ledger)

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatal(err)
	}

	var status, summary string
	if err := e.seed.QueryRow(e.ctx, `SELECT status, summary_text FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID).Scan(&status, &summary); err != nil {
		t.Fatal(err)
	}
	if status != "ok" || summary != "Cliente relata queda de internet" {
		t.Fatalf("status=%s summary=%q", status, summary)
	}

	var topicID uuid.UUID
	var title, kind, factText string
	if err := e.seed.QueryRow(e.ctx, `
		SELECT f.topic_thread_id, t.title, f.kind, f.text FROM context_proposed_facts f
		JOIN context_window_analyses a2 ON a2.id = f.analysis_id
		JOIN topic_threads t ON t.id = f.topic_thread_id
		WHERE a2.tenant_id = $1 AND a2.window_id = $2`, a.id, w.ID).Scan(&topicID, &title, &kind, &factText); err != nil {
		t.Fatal(err)
	}
	if title != "Cliente relata queda de internet" || kind != "need" || factText != "Cliente ficou sem internet" {
		t.Fatalf("fact: title=%q kind=%s text=%q", title, kind, factText)
	}
	if n := e.count(`SELECT count(*) FROM topic_conversation_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND conversation_id=$3`, a.id, topicID, a.conversation); n != 1 {
		t.Fatalf("topic_conversation_links = %d, want 1 (the new topic must be linked to the conversation)", n)
	}

	var relation, source string
	if err := e.seed.QueryRow(e.ctx, `SELECT relation, decision_source FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND topic_thread_id=$3`, a.id, m1, topicID).Scan(&relation, &source); err != nil {
		t.Fatal(err)
	}
	if relation != "supporting" || source != "ai" {
		t.Fatalf("message_topic_links: relation=%s source=%s, want supporting/ai", relation, source)
	}

	var ok bool
	var in, out int
	if err := e.seed.QueryRow(e.ctx, `SELECT success, input_tokens, output_tokens FROM ai_usage WHERE tenant_id=$1 AND task='context_window_analyze'`, a.id).Scan(&ok, &in, &out); err != nil {
		t.Fatal(err)
	}
	if !ok || in != 300 || out != 60 {
		t.Fatalf("ledger: ok=%v in=%d out=%d", ok, in, out)
	}
}

func TestWindowAnalyzerAttachesAFactToAnExistingCandidateTopicWithoutCreatingANewOne(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	existing := e.topic(a.id, "Instabilidade de conexão")
	e.exec(`INSERT INTO topic_conversation_links (tenant_id, topic_thread_id, conversation_id, relation) VALUES ($1,$2,$3,'active')`, a.id, existing, a.conversation)
	m1 := e.message(a.id, a.conversation, "ainda sem internet")
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	gen := tokenGen{out: `{"summary":"Cliente insiste na falta de internet","facts":[{"topic_alias":"T1","kind":"observation","text":"Persiste sem internet","evidence":["M1"]}]}`, in: 200, outTok: 30}
	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: gen, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id); n != 1 {
		t.Fatalf("topics = %d, want still 1 (T1 reused, no NEW topic created)", n)
	}
	var topicID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT topic_thread_id FROM context_proposed_facts WHERE tenant_id=$1`, a.id).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	if topicID != existing {
		t.Fatalf("fact topic = %s, want the existing candidate %s", topicID, existing)
	}
}

func TestWindowAnalyzerPersistsNothingOnAProviderOrParseFailure(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	ledger := aiusageadapters.NewPostgresLedger(e.app)
	analysisRepo := NewPostgresContextAnalysisRepository(e.app)
	topicRepo := NewPostgresTopicRepository(e.app)
	summaryRepo := NewPostgresSummaryRepository(e.app)

	for name, gen := range map[string]tokenGen{
		"provider_error": {err: context.DeadlineExceeded},
		"invalid_output": {out: `isto não é JSON`},
	} {
		t.Run(name, func(t *testing.T) {
			m := e.message(a.id, a.conversation, "mensagem "+name)
			w := claimOneDueWindow(t, e, a.id, a.conversation, m, contextwindow.DefaultPolicy())
			models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
				application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: gen, MaxOutputTokens: 500})
			analyzer := application.NewWindowAnalyzerService(windowStore, analysisRepo, topicRepo, summaryRepo, models).WithLedger(ledger)

			_, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) })
			if err == nil {
				t.Fatal("a provider/parse failure must be returned as an error for the runner to retry")
			}
			if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID); n != 0 {
				t.Fatalf("analyses = %d, want 0 (a transient failure must never persist a partial row)", n)
			}
			if n := e.count(`SELECT count(*) FROM ai_usage WHERE tenant_id=$1 AND task='context_window_analyze' AND reason=$2`, a.id, name); n != 1 {
				t.Fatalf("ledger rows for %s = %d, want 1 (the call itself is always accounted)", name, n)
			}
		})
	}
}

// countingGen wraps tokenGen to prove the model is called AT MOST ONCE per window, even across a simulated crash
// recovery (Codex review round 2): a worker that dies after Save commits but before the runner's separate
// Complete call gets the window reclaimed and re-handed to Analyze, which must recognize the existing receipt
// instead of calling the model again (and then failing Save's UNIQUE(tenant_id, window_id)).
type countingGen struct {
	tokenGen
	calls *int
}

func (g countingGen) Generate(ctx context.Context, req aiports.GenerateRequest) (aiports.GenerateResponse, error) {
	*g.calls++
	return g.tokenGen.Generate(ctx, req)
}

func TestWindowAnalyzerRecognizesAnAlreadyPersistedReceiptInsteadOfAnalyzingAgain(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "minha entrega não chegou")
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	calls := 0
	gen := countingGen{tokenGen: tokenGen{out: `{"summary":"Cliente sem entrega","facts":[{"topic_alias":"NEW","kind":"need","text":"Receber a entrega","evidence":["M1"]}]}`, in: 100, outTok: 20}, calls: &calls}
	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: gen, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	// 1st run: persists the receipt (this is the runner's Analyze call that committed, right before a simulated
	// crash — Complete is never called here, exactly like a crash in that gap).
	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model calls after 1st run = %d, want 1", calls)
	}
	if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID); n != 1 {
		t.Fatalf("analyses = %d, want exactly 1", n)
	}

	// 2nd run: the window was reclaimed (lease expired) and handed to Analyze again, as the runner would after a
	// crash. It must succeed WITHOUT calling the model again or erroring on the unique constraint.
	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatalf("re-analyzing a window with an existing receipt must succeed (idempotent), got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("model calls after 2nd run = %d, want still 1 (no redundant model call)", calls)
	}
	if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID); n != 1 {
		t.Fatalf("analyses after 2nd run = %d, want still exactly 1 (no duplicate receipt)", n)
	}
}

func TestWindowAnalyzerWithNoModelConfiguredDegradesWithoutError(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "oi")
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), application.NewModelRouter()) // no route set for TaskContextWindowAnalyze

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatalf("no model configured must degrade silently, never fail: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1`, a.id); n != 0 {
		t.Fatalf("analyses = %d, want 0", n)
	}
}
