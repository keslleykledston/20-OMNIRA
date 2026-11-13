package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// CONTEXT.2B: structured analysis storage. Real Postgres, FORCE RLS, runtime role (omnira_app).

// sys runs fn in a system session scoped to tenant — exactly how WindowAnalyzerService.Analyze really runs
// (ContextWindowRunner.handle via WithSystemTenantSession), which is what the repository's RLS write policies
// require (INSERT/UPDATE on context_window_analyses/context_proposed_facts/*_evidence is_system_admin() only).
func sys[T any](e *env, tenant uuid.UUID, fn func(ctx context.Context) (T, error)) (T, error) {
	var out T
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, tenant, func(ctx context.Context) error {
		var ferr error
		out, ferr = fn(ctx)
		return ferr
	})
	return out, err
}

func (e *env) topic(tenant uuid.UUID, title string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO topic_threads (id, tenant_id, title, status, privacy_policy, source) VALUES ($1,$2,$3,'open','public','manual')`, id, tenant, title)
	return id
}

// contextWindow creates a closed (due) window of its own — CONTEXT.2B tests only need a valid window_id to hang an
// analysis off of; the window's own first/last message is unrelated to whatever messages a test cites as evidence.
func (e *env) contextWindow(tenant, conversation uuid.UUID) uuid.UUID {
	e.t.Helper()
	id, msg := uuid.New(), e.message(tenant, conversation, "m")
	e.exec(`INSERT INTO context_analysis_windows (id, tenant_id, conversation_id, generation, state, first_message_at, first_message_id, last_message_at, last_message_id, message_count, due_at)
	        SELECT $1, $2, $3, COALESCE((SELECT max(generation) FROM context_analysis_windows WHERE tenant_id=$2 AND conversation_id=$3), 0) + 1,
	               'due', now(), $4, now(), $4, 1, now()`, id, tenant, conversation, msg)
	return id
}

func TestWindowMessagesLoadsTextFallsBackToMediaAnalysisAndExcludesOtherConversations(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)

	withText := e.message(a.id, a.conversation, "preciso de ajuda")
	withoutText := e.message(a.id, a.conversation, "")
	e.exec(`INSERT INTO message_media_analysis (tenant_id, message_id, kind, status, body) VALUES ($1,$2,'description','done','uma foto do roteador')`, a.id, withoutText)
	stillPending := e.message(a.id, a.conversation, "")
	otherConv := e.conversationFor(a.id, nil)
	elsewhere := e.message(a.id, otherConv, "mensagem de outra conversa")

	rows, err := sys(e, a.id, func(ctx context.Context) ([]ports.WindowMessageRow, error) {
		return repo.WindowMessages(ctx, a.id, a.conversation, []uuid.UUID{withText, withoutText, stillPending, elsewhere})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (elsewhere's conversation excludes it)", len(rows))
	}
	byID := map[uuid.UUID]ports.WindowMessageRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if r := byID[withText]; r.Text != "preciso de ajuda" || r.FromMedia || !r.Inbound {
		t.Fatalf("withText = %+v", r)
	}
	if r := byID[withoutText]; r.Text != "uma foto do roteador" || !r.FromMedia {
		t.Fatalf("withoutText = %+v, want the media analysis body", r)
	}
	if r := byID[stillPending]; r.Text != "" {
		t.Fatalf("stillPending = %+v, want empty text (no body, no done analysis)", r)
	}
	if _, ok := byID[elsewhere]; ok {
		t.Fatal("a message of another conversation must never be returned")
	}
}

func TestSaveEmptyRecordsTheReceiptWithNoFacts(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	w := e.contextWindow(a.id, a.conversation)

	id, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: w, Status: "empty"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var status, summary string
	var count int
	if err := e.seed.QueryRow(e.ctx, `SELECT status, summary_text, analyzed_message_count FROM context_window_analyses WHERE id=$1`, id).Scan(&status, &summary, &count); err != nil {
		t.Fatal(err)
	}
	if status != "empty" || summary != "" || count != 0 {
		t.Fatalf("status=%s summary=%q count=%d", status, summary, count)
	}
	if n := e.count(`SELECT count(*) FROM context_proposed_facts WHERE analysis_id=$1`, id); n != 0 {
		t.Fatalf("facts = %d, want 0", n)
	}
}

func TestSaveOkPersistsFactsAndEvidenceAndChainsPreviousAnalysis(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	topicID := e.topic(a.id, "Instabilidade")
	m1 := e.message(a.id, a.conversation, "minha internet caiu")
	m2 := e.message(a.id, a.conversation, "já verificou o roteador?")
	w1 := e.contextWindow(a.id, a.conversation)

	first, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{
			TenantID: a.id, ConversationID: a.conversation, WindowID: w1, Status: "ok", SummaryText: "Cliente relata queda de conexão",
			ModelProvider: "gemini", ModelName: "gemini-2.5-flash", PromptVersion: "context-window-analyze-v1",
			InputTokens: 120, OutputTokens: 40, LatencyMS: 900, AnalyzedMessageIDs: []uuid.UUID{m1, m2},
			Facts: []ports.ResolvedFact{
				{TopicThreadID: topicID, Kind: "need", Text: "Cliente sem internet", EvidenceMessageIDs: []uuid.UUID{m1}},
				{TopicThreadID: topicID, Kind: "suggested_action", Text: "Pedir reinício do roteador", EvidenceMessageIDs: []uuid.UUID{m1, m2}},
			},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var prev *uuid.UUID
	var firstAt, lastAt time.Time
	var count int
	if err := e.seed.QueryRow(e.ctx, `SELECT previous_analysis_id, analyzed_first_message_at, analyzed_last_message_at, analyzed_message_count FROM context_window_analyses WHERE id=$1`, first).
		Scan(&prev, &firstAt, &lastAt, &count); err != nil {
		t.Fatal(err)
	}
	if prev != nil || count != 2 || firstAt.IsZero() || lastAt.IsZero() {
		t.Fatalf("1st analysis: prev=%v count=%d first=%v last=%v", prev, count, firstAt, lastAt)
	}
	var facts []struct {
		id   uuid.UUID
		kind string
	}
	rows, err := e.seed.Query(e.ctx, `SELECT id, kind FROM context_proposed_facts WHERE analysis_id=$1 ORDER BY created_at`, first)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var f struct {
			id   uuid.UUID
			kind string
		}
		if err := rows.Scan(&f.id, &f.kind); err != nil {
			t.Fatal(err)
		}
		facts = append(facts, f)
	}
	rows.Close()
	if len(facts) != 2 || facts[0].kind != "need" || facts[1].kind != "suggested_action" {
		t.Fatalf("facts = %+v", facts)
	}
	if n := e.count(`SELECT count(*) FROM context_proposed_fact_evidence WHERE fact_id=$1`, facts[0].id); n != 1 {
		t.Fatalf("1st fact evidence = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM context_proposed_fact_evidence WHERE fact_id=$1`, facts[1].id); n != 2 {
		t.Fatalf("2nd fact evidence = %d, want 2", n)
	}
	for _, f := range facts {
		var truth, status string
		if err := e.seed.QueryRow(e.ctx, `SELECT truth_level, status FROM context_proposed_facts WHERE id=$1`, f.id).Scan(&truth, &status); err != nil {
			t.Fatal(err)
		}
		if truth != "ai_inferred" || status != "open" {
			t.Fatalf("fact %s: truth=%s status=%s, want ai_inferred/open", f.id, truth, status)
		}
	}

	// A second window of the SAME conversation chains to the first — this is CONTEXT.2's "versão anterior".
	w2 := e.contextWindow(a.id, a.conversation)
	second, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: w2, Status: "empty"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.seed.QueryRow(e.ctx, `SELECT previous_analysis_id FROM context_window_analyses WHERE id=$1`, second).Scan(&prev); err != nil {
		t.Fatal(err)
	}
	if prev == nil || *prev != first {
		t.Fatalf("2nd analysis previous_analysis_id = %v, want %s", prev, first)
	}
}

func TestFactImmutableFieldsAreGuardedButConfirmDismissIsAllowed(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	topicID := e.topic(a.id, "Assunto")
	m1 := e.message(a.id, a.conversation, "preciso de um boleto")
	w := e.contextWindow(a.id, a.conversation)
	analysisID, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{
			TenantID: a.id, ConversationID: a.conversation, WindowID: w, Status: "ok",
			AnalyzedMessageIDs: []uuid.UUID{m1},
			Facts:              []ports.ResolvedFact{{TopicThreadID: topicID, Kind: "need", Text: "Cliente pediu boleto", EvidenceMessageIDs: []uuid.UUID{m1}}},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var factID uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM context_proposed_facts WHERE analysis_id=$1`, analysisID).Scan(&factID)

	// A direct attempt to rewrite what the model said is refused by the trigger, even as the owner connection.
	if _, err := e.seed.Exec(e.ctx, `UPDATE context_proposed_facts SET text='outra coisa' WHERE id=$1`, factID); err == nil {
		t.Fatal("rewriting a fact's text must be refused")
	}
	if _, err := e.seed.Exec(e.ctx, `UPDATE context_proposed_facts SET topic_thread_id=$2 WHERE id=$1`, factID, e.topic(a.id, "Outro")); err == nil {
		t.Fatal("rewriting a fact's topic must be refused")
	}

	admin := e.member(a.id, "tenant_admin") // tenant_admin holds topic.manage (migration 000063)
	e.session(a.id, admin, func(ctx context.Context) {
		q := platformdb.QuerierFromContext(ctx, e.app)
		if _, err := q.Exec(ctx, `UPDATE context_proposed_facts SET status='confirmed', confirmed_by_user_id=$2, confirmed_at=now() WHERE id=$1`, factID, admin); err != nil {
			t.Fatalf("confirming a fact with topic.manage must be allowed: %v", err)
		}
	})
	var status string
	_ = e.seed.QueryRow(e.ctx, `SELECT status FROM context_proposed_facts WHERE id=$1`, factID).Scan(&status)
	if status != "confirmed" {
		t.Fatalf("status = %s, want confirmed", status)
	}

	noPerm := e.readOnlyMember(a.id) // only tenant.read, no topic.manage
	e.session(a.id, noPerm, func(ctx context.Context) {
		q := platformdb.QuerierFromContext(ctx, e.app)
		tag, err := q.Exec(ctx, `UPDATE context_proposed_facts SET status='dismissed' WHERE id=$1`, factID)
		if err == nil && tag.RowsAffected() != 0 {
			t.Fatal("a member without topic.manage must not be able to dismiss a fact")
		}
	})
}

func TestAnalysesAndFactsAreTenantIsolatedByRLS(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	topicA := e.topic(a.id, "Assunto A")
	mA := e.message(a.id, a.conversation, "mensagem do tenant A")
	wA := e.contextWindow(a.id, a.conversation)
	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{
			TenantID: a.id, ConversationID: a.conversation, WindowID: wA, Status: "ok", AnalyzedMessageIDs: []uuid.UUID{mA},
			Facts: []ports.ResolvedFact{{TopicThreadID: topicA, Kind: "observation", Text: "x", EvidenceMessageIDs: []uuid.UUID{mA}}},
		})
	}); err != nil {
		t.Fatal(err)
	}

	userB := e.member(b.id, "tenant_admin")
	e.session(b.id, userB, func(ctx context.Context) {
		q := platformdb.QuerierFromContext(ctx, e.app)
		var n int
		_ = q.QueryRow(ctx, `SELECT count(*) FROM context_window_analyses`).Scan(&n)
		if n != 0 {
			t.Fatalf("tenant B sees %d analyses, want 0", n)
		}
		_ = q.QueryRow(ctx, `SELECT count(*) FROM context_proposed_facts`).Scan(&n)
		if n != 0 {
			t.Fatalf("tenant B sees %d facts, want 0", n)
		}
	})

	// Outside any session (RLS applies with current_user_id() NULL, no is_system_admin): nothing is visible either.
	var n int
	if err := e.app.QueryRow(e.ctx, `SELECT count(*) FROM context_window_analyses`).Scan(&n); err == nil && n != 0 {
		t.Fatalf("unscoped read saw %d rows", n)
	}
}
