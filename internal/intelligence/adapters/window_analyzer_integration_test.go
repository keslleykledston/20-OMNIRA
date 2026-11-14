package adapters

import (
	"context"
	"fmt"
	"strings"
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

// Regression for a Codex finding (round 3, CONTEXT.2D): contextdelta.Parse accepts a summary up to 2,000
// characters (and the prompt asks for up to two sentences), but domain.NewTopicThread rejects a title over
// domain.MaxTitleRunes (200) — a valid, otherwise-unremarkable model response could repeatedly fail Save and
// eventually dead-letter the window. The title must be bounded before NewTopicThread ever sees it.
func TestWindowAnalyzerBoundsALongSummaryBeforeUsingItAsATopicTitle(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "quero saber sobre o atraso da minha encomenda")
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	longSummary := strings.Repeat("Cliente relata atraso recorrente na entrega e pede atualização urgente do status do pedido. ", 5) // ~470 runes, well under the parser's 2000-char cap but over the 200-rune title limit
	gen := tokenGen{out: fmt.Sprintf(`{"summary":%q,"facts":[{"topic_alias":"NEW","kind":"need","text":"Cliente quer status do pedido","evidence":["M1"]}]}`, longSummary), in: 200, outTok: 80}
	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: gen, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatalf("a long but otherwise valid summary must not fail the window: %v", err)
	}
	var title string
	if err := e.seed.QueryRow(e.ctx, `SELECT title FROM topic_threads WHERE tenant_id=$1`, a.id).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(title)); n == 0 || n > 200 {
		t.Fatalf("title rune length = %d, want 1..200", n)
	}
	if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2 AND status='ok'`, a.id, w.ID); n != 1 {
		t.Fatalf("analyses = %d, want exactly 1 ok", n)
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

// Regression for a Codex finding (round 4, CONTEXT.2D): a window whose only message is media still waiting on its
// transcript/description used to be saved as a terminal "empty" receipt — nothing ever reopens a completed
// window, so that content would never actually be analyzed once the media pipeline finally finished. Analyze
// must return an error (so the runner retries/backs off) while media analysis is still pending.
func TestWindowAnalyzerRetriesInsteadOfCompletingEmptyWhileMediaAnalysisIsPending(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "") // media-only: no body of its own
	e.exec(`INSERT INTO message_media_analysis (tenant_id, message_id, kind, status) VALUES ($1,$2,'transcript','pending')`, a.id, m1)
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	_, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) })
	if err == nil {
		t.Fatal("pending media analysis must be returned as an error for the runner to retry, not completed")
	}
	if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID); n != 0 {
		t.Fatalf("analyses = %d, want 0 (never a terminal receipt while media is still pending)", n)
	}
}

// Once media analysis is no longer pending (done with no usable text, failed, or skipped — never 'pending'), a
// window with no usable text genuinely has nothing to analyze and "empty" is the correct, terminal answer.
func TestWindowAnalyzerSavesEmptyOnceMediaAnalysisIsNoLongerPending(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "")
	e.exec(`INSERT INTO message_media_analysis (tenant_id, message_id, kind, status) VALUES ($1,$2,'transcript','failed')`, a.id, m1)
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatalf("a window whose media analysis failed (not pending) must still complete: %v", err)
	}
	var status string
	if err := e.seed.QueryRow(e.ctx, `SELECT status FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "empty" {
		t.Fatalf("status = %q, want empty", status)
	}
}

// Regression for a Codex finding (round 5, CONTEXT.2D): round 4's fix only checked for pending media when the
// window had ZERO usable text. A window with one text message AND one audio message whose transcript is still
// pending has usable text (buildMessages keeps the text one), so that check was skipped entirely — the window
// would be finalized with a terminal receipt that permanently omits the audio, even once its transcript later
// finishes. The check must run for the WHOLE window, not only the all-media, nothing-usable case.
func TestWindowAnalyzerRetriesAMixedWindowWhileOnlySomeOfItsMediaIsStillPending(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	policy := contextwindow.Policy{Idle: 30 * time.Second, MaxAge: 2 * time.Minute, MaxMessages: 10}
	m1 := e.message(a.id, a.conversation, "aqui vai um áudio")
	e.exec(`INSERT INTO message_media_analysis (tenant_id, message_id, kind, status) VALUES ($1,$2,'transcript','pending')`, a.id, m1)
	m2 := e.message(a.id, a.conversation, "e aqui o texto sobre o mesmo assunto")
	if err := windowStore.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := windowStore.Touch(e.ctx, policy, a.id, a.conversation, m2, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := windowStore.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerManual); err != nil {
		t.Fatal(err)
	}
	claimed, err := windowStore.Claim(e.ctx, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: n=%d err=%v", len(claimed), err)
	}
	w := claimed[0]

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	_, err = sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) })
	if err == nil {
		t.Fatal("a window with usable text but ALSO pending media must still be retried, not finalized")
	}
	if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID); n != 0 {
		t.Fatalf("analyses = %d, want 0 (never a terminal receipt while any of the window's media is still pending)", n)
	}
}

// Regression for a Codex finding (round 6, CONTEXT.2D): round 4/5's pending check only looked at
// message_media_analysis rows, but an image/PDF still being virus-scanned, or already clean but not yet picked
// up by the next EnqueueVision batch tick (internal/media/adapters — periodic, unlike audio's trigger-based
// transcript enqueue), has NO message_media_analysis row at all yet. The window must still be retried, not
// finalized missing that attachment.
func TestWindowAnalyzerRetriesWhileAnImageIsStillBeingScanned(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	e.exec(`INSERT INTO tenant_ai_integrations (tenant_id, provider, enabled, secret_ciphertext, consent_at) VALUES ($1,'gemini',true,'\x00','1970-01-01'::timestamptz)`, a.id)
	m1 := e.message(a.id, a.conversation, "")
	e.exec(`INSERT INTO message_media (id,tenant_id,message_id,status,kind,mime) VALUES ($1,$2,$3,'pending','image','image/png')`, uuid.New(), a.id, m1)
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	_, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) })
	if err == nil {
		t.Fatal("an image still being scanned (no message_media_analysis row yet) must be retried, not finalized")
	}
	if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID); n != 0 {
		t.Fatalf("analyses = %d, want 0", n)
	}
}

func TestWindowAnalyzerRetriesWhileACleanImageHasNotYetBeenEnqueuedForVisionAnalysis(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	e.exec(`INSERT INTO tenant_ai_integrations (tenant_id, provider, enabled, secret_ciphertext, consent_at) VALUES ($1,'gemini',true,'\x00','1970-01-01'::timestamptz)`, a.id)
	m1 := e.message(a.id, a.conversation, "")
	e.exec(`INSERT INTO message_media (id,tenant_id,message_id,status,kind,mime) VALUES ($1,$2,$3,'clean','image','image/png')`, uuid.New(), a.id, m1)
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	_, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) })
	if err == nil {
		t.Fatal("a clean image not yet enqueued for vision analysis must be retried, not finalized")
	}
	if n := e.count(`SELECT count(*) FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID); n != 0 {
		t.Fatalf("analyses = %d, want 0", n)
	}
}

// Regression for a Codex finding (round 7, CONTEXT.2D): for a tenant that never enabled the Gemini integration,
// a clean image never receives an analysis row no matter how long the window waits — EnqueueVision explicitly
// requires the per-tenant opt-in. HasPendingMediaAnalysis must recognize this and let the window finalize
// (previously it would retry, and eventually dead-letter, the window forever).
func TestWindowAnalyzerDoesNotWaitForVisionWhenTheTenantNeverOptedIn(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "")
	e.exec(`INSERT INTO message_media (id,tenant_id,message_id,status,kind,mime) VALUES ($1,$2,$3,'clean','image','image/png')`, uuid.New(), a.id, m1)
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatalf("a tenant without vision opt-in must not block the window forever: %v", err)
	}
	var status string
	if err := e.seed.QueryRow(e.ctx, `SELECT status FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "empty" {
		t.Fatalf("status = %q, want empty", status)
	}
}

// Regression for a Codex finding (round 8, CONTEXT.2D): VisionProcessor's EnqueueVision only enqueues files
// created within its own Lookback (7 days by default) — an older, still-unanalyzed image reaching window
// processing late (a backlog, an outage) was NEVER going to get a receipt either way, so waiting for it (and
// retrying the whole window, including its good text, until dead) was wrong even with vision enabled and the
// tenant opted in.
func TestWindowAnalyzerDoesNotWaitForAnImageOlderThanTheVisionLookback(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	e.exec(`INSERT INTO tenant_ai_integrations (tenant_id, provider, enabled, secret_ciphertext, consent_at) VALUES ($1,'gemini',true,'\x00','1970-01-01'::timestamptz)`, a.id)
	m1 := e.message(a.id, a.conversation, "")
	e.exec(`INSERT INTO message_media (id,tenant_id,message_id,status,kind,mime,created_at) VALUES ($1,$2,$3,'clean','image','image/png', now() - interval '30 days')`,
		uuid.New(), a.id, m1)
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models) // default pipelines: both live, DefaultVisionLookback (7 days)

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatalf("an image older than the vision lookback must not block the window forever: %v", err)
	}
	var status string
	if err := e.seed.QueryRow(e.ctx, `SELECT status FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "empty" {
		t.Fatalf("status = %q, want empty", status)
	}
}

// Regression for a Codex finding (round 7, CONTEXT.2D): HasPendingMediaAnalysis itself must not wait for either
// pipeline when the analyzer is told it is not actually running — a deployment with no Whisper engine configured
// (OMNIRA_WHISPER_URL empty) must not retry a window forever over an audio file still being scanned.
func TestWindowAnalyzerDoesNotWaitForTranscriptionWhenThePipelineIsNotLive(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "")
	e.exec(`INSERT INTO message_media (id,tenant_id,message_id,status,kind,mime) VALUES ($1,$2,$3,'pending','audio','audio/ogg')`, uuid.New(), a.id, m1)
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models).WithMediaPipelines(ports.MediaPipelines{TranscriptionEnabled: false, VisionEnabled: true})

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatalf("transcription not live must not block the window forever: %v", err)
	}
	var status string
	if err := e.seed.QueryRow(e.ctx, `SELECT status FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "empty" {
		t.Fatalf("status = %q, want empty", status)
	}
}

// A clean file that neither pipeline will ever analyze (wrong mime for vision, not audio) — or a file that
// settled to a terminal non-clean status (infected/rejected/source_gone/failed) — must never block the window
// forever: HasPendingMediaAnalysis only waits for an outcome that is genuinely still unknown.
func TestWindowAnalyzerSavesEmptyForMediaThatWillNeverBeAnalyzed(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	windowStore := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "")
	e.exec(`INSERT INTO message_media (id,tenant_id,message_id,status,kind,mime) VALUES ($1,$2,$3,'infected','image','image/png')`, uuid.New(), a.id, m1)
	w := claimOneDueWindow(t, e, a.id, a.conversation, m1, contextwindow.DefaultPolicy())

	models := application.NewModelRouter().Set(application.TaskContextWindowAnalyze,
		application.ModelRoute{Provider: "fake", Model: "fake-1", Generator: tokenGen{out: `{"summary":"","facts":[]}`}, MaxOutputTokens: 500})
	analyzer := application.NewWindowAnalyzerService(windowStore, NewPostgresContextAnalysisRepository(e.app), NewPostgresTopicRepository(e.app),
		NewPostgresSummaryRepository(e.app), models)

	if _, err := sys(e, a.id, func(ctx context.Context) (struct{}, error) { return struct{}{}, analyzer.Analyze(ctx, w) }); err != nil {
		t.Fatalf("an infected file's outcome is already final; the window must still complete: %v", err)
	}
	var status string
	if err := e.seed.QueryRow(e.ctx, `SELECT status FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2`, a.id, w.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "empty" {
		t.Fatalf("status = %q, want empty", status)
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
