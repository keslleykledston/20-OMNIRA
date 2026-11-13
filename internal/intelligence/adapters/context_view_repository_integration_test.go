package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

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
