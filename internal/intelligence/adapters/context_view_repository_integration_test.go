package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// CONTEXT.2C's read side of migration 000177 — LatestForConversation and ConversationCoverage. Real Postgres,
// FORCE RLS, runtime role.

func TestLatestForConversationReturnsTheFactsGroupedByTopicWithEvidenceAndIsReadableByAnyMember(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	topicA := e.topic(a.id, "Entrega")
	win := e.contextWindow(a.id, a.conversation)
	msg1 := e.message(a.id, a.conversation, "onde está meu pedido")

	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{
			TenantID: a.id, ConversationID: a.conversation, WindowID: win, Status: "ok", SummaryText: "cliente perguntou sobre entrega",
			AnalyzedMessageIDs: []uuid.UUID{msg1},
			Facts:              []ports.ResolvedFact{{TopicThreadID: topicA, Kind: "need", Text: "saber status da entrega", EvidenceMessageIDs: []uuid.UUID{msg1}}},
		})
	}); err != nil {
		t.Fatal(err)
	}

	agent := e.member(a.id, "tenant_agent") // a plain member, not a system session — the read policy must allow this
	var row *ports.ContextViewRow
	var err error
	e.session(a.id, agent, func(ctx context.Context) { row, err = repo.LatestForConversation(ctx, a.id, a.conversation) })
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.Status != "ok" || row.SummaryText != "cliente perguntou sobre entrega" || len(row.Facts) != 1 {
		t.Fatalf("row = %+v", row)
	}
	f := row.Facts[0]
	if f.TopicTitle != "Entrega" || f.TopicStatus != "open" || f.Kind != "need" || len(f.EvidenceMessageIDs) != 1 || f.EvidenceMessageIDs[0] != msg1 {
		t.Fatalf("fact = %+v", f)
	}

	var total int
	var lastAt *time.Time
	e.session(a.id, agent, func(ctx context.Context) { total, lastAt, err = repo.ConversationCoverage(ctx, a.id, a.conversation) })
	if err != nil {
		t.Fatal(err)
	}
	// 2, not 1: e.contextWindow() plants its own inbound message (to get a valid window_id) besides msg1.
	if total != 2 || lastAt == nil {
		t.Fatalf("coverage = %d %v", total, lastAt)
	}
}

// Regression for a Codex finding (round 1, CONTEXT.2D): appending to row.Facts in a loop can reallocate its
// backing array, which invalidated a `byID` pointer taken mid-loop and silently dropped evidence for earlier
// facts once a later fact's append triggered the reallocation. Needs >= 2 facts to reproduce.
func TestLatestForConversationKeepsEvidenceForEveryFactWhenAnAnalysisHasMultipleFacts(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	topic := e.topic(a.id, "Entrega")
	win := e.contextWindow(a.id, a.conversation)
	msg1 := e.message(a.id, a.conversation, "quero saber da entrega")
	msg2 := e.message(a.id, a.conversation, "e também do reembolso")

	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{
			TenantID: a.id, ConversationID: a.conversation, WindowID: win, Status: "ok", SummaryText: "duas pendências",
			AnalyzedMessageIDs: []uuid.UUID{msg1, msg2},
			Facts: []ports.ResolvedFact{
				{TopicThreadID: topic, Kind: "need", Text: "saber status da entrega", EvidenceMessageIDs: []uuid.UUID{msg1}},
				{TopicThreadID: topic, Kind: "need", Text: "saber status do reembolso", EvidenceMessageIDs: []uuid.UUID{msg2}},
			},
		})
	}); err != nil {
		t.Fatal(err)
	}

	agent := e.member(a.id, "tenant_agent")
	var row *ports.ContextViewRow
	var err error
	e.session(a.id, agent, func(ctx context.Context) { row, err = repo.LatestForConversation(ctx, a.id, a.conversation) })
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || len(row.Facts) != 2 {
		t.Fatalf("row = %+v", row)
	}
	for _, f := range row.Facts {
		switch f.Text {
		case "saber status da entrega":
			if len(f.EvidenceMessageIDs) != 1 || f.EvidenceMessageIDs[0] != msg1 {
				t.Fatalf("fact %q evidence = %v, want [%s]", f.Text, f.EvidenceMessageIDs, msg1)
			}
		case "saber status do reembolso":
			if len(f.EvidenceMessageIDs) != 1 || f.EvidenceMessageIDs[0] != msg2 {
				t.Fatalf("fact %q evidence = %v, want [%s]", f.Text, f.EvidenceMessageIDs, msg2)
			}
		default:
			t.Fatalf("unexpected fact text %q", f.Text)
		}
	}
}

// Regression for a Codex finding (round 2, CONTEXT.2D): staleness used to compare the newest inbound message's
// timestamp against AnalyzedLastMessageAt, which misses a message sharing the exact same created_at as the
// analyzed one. IsConversationFullyAnalyzed checks real window membership instead — immune to that ambiguity by
// construction, proven here with an UNANALYZED message whose created_at is set to the EXACT same instant as the
// already-analyzed message.
func TestIsConversationFullyAnalyzedUsesWindowMembershipNeverMessageTimestamps(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	store := NewPostgresContextWindowStore(e.app)
	policy := contextwindow.DefaultPolicy()

	m1 := e.message(a.id, a.conversation, "primeira mensagem")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerManual); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(e.ctx, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: n=%d err=%v", len(claimed), err)
	}
	w1 := claimed[0]
	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: w1.ID, Status: "ok",
			SummaryText: "s", AnalyzedMessageIDs: []uuid.UUID{m1}})
	}); err != nil {
		t.Fatal(err)
	}

	agent := e.member(a.id, "tenant_agent")
	var ok bool
	e.session(a.id, agent, func(ctx context.Context) { ok, err = repo.IsConversationFullyAnalyzed(ctx, a.id, a.conversation) })
	if err != nil || !ok {
		t.Fatalf("fully analyzed once the only message's window is analyzed: ok=%v err=%v", ok, err)
	}

	// A 2nd message, created_at set to the EXACT same instant as m1 (a pure timestamp compare would read this as
	// "not newer", hence not stale — wrong: it belongs to a brand-new, unanalyzed window).
	var m1At time.Time
	if err := e.seed.QueryRow(e.ctx, `SELECT created_at FROM messages WHERE id=$1`, m1).Scan(&m1At); err != nil {
		t.Fatal(err)
	}
	m2 := uuid.New()
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status,created_at) VALUES($1,$2,$3,'inbound','text','segunda mensagem','received',$4)`,
		m2, a.id, a.conversation, m1At)
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m2, m1At); err != nil {
		t.Fatal(err)
	}

	e.session(a.id, agent, func(ctx context.Context) { ok, err = repo.IsConversationFullyAnalyzed(ctx, a.id, a.conversation) })
	if err != nil || ok {
		t.Fatalf("a same-timestamp, unanalyzed message must make this false: ok=%v err=%v", ok, err)
	}
}

// Regression for a Codex finding (round 5, CONTEXT.2D): migration 000176's context_analysis_windows/
// context_analysis_window_messages only had a has_active_membership() SELECT policy, so IsConversationFullyAnalyzed
// always found zero rows for a Hub-delegated actor (no membership row) even though the analyses themselves
// (migration 000177) were already correctly readable after round 3's fix — a delegated actor would read a fully
// analyzed conversation as permanently stale. Fixed with additive delegated-read policies on both tables.
func TestIsConversationFullyAnalyzedIsReadableByADelegatedActor(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	store := NewPostgresContextWindowStore(e.app)
	policy := contextwindow.DefaultPolicy()

	m1 := e.message(a.id, a.conversation, "primeira mensagem")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerManual); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(e.ctx, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: n=%d err=%v", len(claimed), err)
	}
	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: claimed[0].ID, Status: "ok",
			SummaryText: "s", AnalyzedMessageIDs: []uuid.UUID{m1}})
	}); err != nil {
		t.Fatal(err)
	}

	delegated := uuid.New()
	var ok bool
	e.delegatedSession(a.id, delegated, []string{"conversation.read", "topic.read"}, func(ctx context.Context) {
		ok, err = repo.IsConversationFullyAnalyzed(ctx, a.id, a.conversation)
	})
	if err != nil || !ok {
		t.Fatalf("a delegated actor must see the analyzed window as covered too: ok=%v err=%v", ok, err)
	}
}

// Regression for a Codex finding (round 6, CONTEXT.2D): an older window can retry (a transient failure) and still
// be in flight while a newer generation is claimed and completes first. Ordering "the latest analysis" by
// created_at would then pick the OLDER generation's receipt once its retry finally lands, even though a newer one
// already exists — stale facts shown while coverage itself reports stale=false. It must order by the window's
// own GENERATION instead.
func TestLatestForConversationOrdersByWindowGenerationNeverByWhenTheReceiptWasWritten(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	win1 := e.contextWindow(a.id, a.conversation) // generation 1
	win2 := e.contextWindow(a.id, a.conversation) // generation 2

	// Generation 2 finishes FIRST...
	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: win2, Status: "ok", SummaryText: "segunda geração"})
	}); err != nil {
		t.Fatal(err)
	}
	// ...and generation 1's retry only lands AFTER (a later created_at despite being the OLDER generation).
	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: win1, Status: "ok", SummaryText: "primeira geração, terminou depois"})
	}); err != nil {
		t.Fatal(err)
	}

	agent := e.member(a.id, "tenant_agent")
	var row *ports.ContextViewRow
	var err error
	e.session(a.id, agent, func(ctx context.Context) { row, err = repo.LatestForConversation(ctx, a.id, a.conversation) })
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.SummaryText != "segunda geração" {
		t.Fatalf("row = %+v, want the HIGHER generation's analysis even though it was written first", row)
	}
}

// Same bug, the write side: previous_analysis_id must chain by generation too, never by creation order.
func TestSaveChainsPreviousAnalysisIDByGenerationNeverByWhenItWasWritten(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	win1 := e.contextWindow(a.id, a.conversation) // generation 1
	win2 := e.contextWindow(a.id, a.conversation) // generation 2
	win3 := e.contextWindow(a.id, a.conversation) // generation 3

	gen2ID, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: win2, Status: "ok"})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Generation 1's retry lands AFTER generation 2 (later created_at, older generation) — must never become
	// anyone's "previous".
	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: win1, Status: "ok"})
	}); err != nil {
		t.Fatal(err)
	}
	gen3ID, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: win3, Status: "ok"})
	})
	if err != nil {
		t.Fatal(err)
	}

	var prev uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT previous_analysis_id FROM context_window_analyses WHERE id=$1`, gen3ID).Scan(&prev); err != nil {
		t.Fatal(err)
	}
	if prev != gen2ID {
		t.Fatalf("previous_analysis_id = %s, want generation 2's analysis %s (not generation 1's, even though it was written later)", prev, gen2ID)
	}
}

func TestLatestForConversationAndConversationCoverageAreTenantIsolated(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	topicA := e.topic(a.id, "Assunto de A")
	win := e.contextWindow(a.id, a.conversation)
	msg1 := e.message(a.id, a.conversation, "mensagem de A")
	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return repo.Save(ctx, ports.AnalysisRecord{TenantID: a.id, ConversationID: a.conversation, WindowID: win, Status: "ok", SummaryText: "s",
			AnalyzedMessageIDs: []uuid.UUID{msg1}, Facts: []ports.ResolvedFact{{TopicThreadID: topicA, Kind: "need", Text: "x", EvidenceMessageIDs: []uuid.UUID{msg1}}}})
	}); err != nil {
		t.Fatal(err)
	}

	stranger := e.member(b.id, "tenant_agent")
	var row *ports.ContextViewRow
	var err error
	// B's member, asked about A's tenant/conversation: RLS denies the membership check silently (0 rows), never A's data.
	e.session(b.id, stranger, func(ctx context.Context) { row, err = repo.LatestForConversation(ctx, a.id, a.conversation) })
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("a stranger to tenant A must see nothing, got %+v", row)
	}
	var total int
	e.session(b.id, stranger, func(ctx context.Context) { total, _, err = repo.ConversationCoverage(ctx, a.id, a.conversation) })
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("coverage across tenants must read as empty, got total=%d", total)
	}
}

func TestConversationCoverageCountsOnlyInboundMessagesOfThatConversation(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	repo := NewPostgresContextAnalysisRepository(e.app)
	e.message(a.id, a.conversation, "primeira")
	e.message(a.id, a.conversation, "segunda")
	e.exec(`INSERT INTO messages (id,tenant_id,conversation_id,direction,message_type,body,status) VALUES ($1,$2,$3,'outbound','text','resposta do agente','sent')`,
		uuid.New(), a.id, a.conversation)
	other := e.conversationFor(a.id, nil)
	e.message(a.id, other, "mensagem de outra conversa")

	member := e.member(a.id, "tenant_agent")
	var total int
	var err error
	e.session(a.id, member, func(ctx context.Context) { total, _, err = repo.ConversationCoverage(ctx, a.id, a.conversation) })
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (outbound and the other conversation must not count)", total)
	}
}
