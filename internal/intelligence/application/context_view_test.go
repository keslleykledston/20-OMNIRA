package application

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/ports"
)

type fakeAnalysisRepo struct {
	row                    *ports.ContextViewRow
	total                  int
	lastInboundAt          *time.Time
	coverageErr, latestErr error
}

func (f *fakeAnalysisRepo) WindowMessages(ctx context.Context, tenantID, conversationID uuid.UUID, messageIDs []uuid.UUID) ([]ports.WindowMessageRow, error) {
	return nil, nil
}
func (f *fakeAnalysisRepo) Save(ctx context.Context, rec ports.AnalysisRecord) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeAnalysisRepo) LatestForConversation(ctx context.Context, tenantID, conversationID uuid.UUID) (*ports.ContextViewRow, error) {
	return f.row, f.latestErr
}
func (f *fakeAnalysisRepo) ConversationCoverage(ctx context.Context, tenantID, conversationID uuid.UUID) (int, *time.Time, error) {
	return f.total, f.lastInboundAt, f.coverageErr
}

var _ ports.ContextAnalysisRepository = (*fakeAnalysisRepo)(nil)

func at(s string) time.Time {
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return tm
}

func TestContextViewNoAnalysisYetIsStaleExactlyWhenTheConversationHasMessages(t *testing.T) {
	ctx := context.Background()
	tenant, conv := uuid.New(), uuid.New()

	repo := &fakeAnalysisRepo{row: nil, total: 0}
	v, err := NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil || v.AnalysisState != "none" || v.Stale {
		t.Fatalf("empty conversation: %+v err=%v", v, err)
	}

	repo = &fakeAnalysisRepo{row: nil, total: 3}
	v, err = NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil || v.AnalysisState != "none" || !v.Stale {
		t.Fatalf("messages but nothing analyzed: %+v err=%v", v, err)
	}
}

func TestContextViewStalenessComparesTheNewestInboundMessageAgainstTheLatestAnalysis(t *testing.T) {
	ctx := context.Background()
	tenant, conv := uuid.New(), uuid.New()
	analyzedAt := at("2026-11-13T10:00:00Z")

	// fully covered: the newest inbound message is exactly what was analyzed
	repo := &fakeAnalysisRepo{total: 5, lastInboundAt: &analyzedAt, row: &ports.ContextViewRow{Status: "ok", AnalyzedLastMessageAt: &analyzedAt}}
	v, err := NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil || v.Stale {
		t.Fatalf("fully covered must not be stale: %+v err=%v", v, err)
	}

	// a newer message arrived after the analysis ran
	newer := analyzedAt.Add(time.Minute)
	repo = &fakeAnalysisRepo{total: 6, lastInboundAt: &newer, row: &ports.ContextViewRow{Status: "ok", AnalyzedLastMessageAt: &analyzedAt}}
	v, err = NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil || !v.Stale {
		t.Fatalf("a newer message must mark the view stale: %+v err=%v", v, err)
	}
}

func TestContextViewGroupsFactsByTopicAndSplitsPendingActionsFromSuggestions(t *testing.T) {
	ctx := context.Background()
	tenant, conv := uuid.New(), uuid.New()
	topicA, topicB := uuid.New(), uuid.New()
	createdAt := at("2026-11-13T09:00:00Z")

	row := &ports.ContextViewRow{
		AnalysisID: uuid.New(), Status: "ok", SummaryText: "cliente perguntou sobre entrega", CreatedAt: createdAt,
		AnalyzedMessageCount: 4, AnalyzedLastMessageAt: &createdAt,
		Facts: []ports.ContextViewFact{
			{ID: uuid.New(), TopicThreadID: topicA, TopicTitle: "Entrega", TopicStatus: "open", Kind: "observation", Text: "cliente mencionou atraso", Status: "open", TruthLevel: "ai_inferred"},
			{ID: uuid.New(), TopicThreadID: topicA, TopicTitle: "Entrega", TopicStatus: "open", Kind: "commitment", Text: "atendente prometeu rastrear", Status: "open", TruthLevel: "ai_inferred", EvidenceMessageIDs: []uuid.UUID{uuid.New()}},
			{ID: uuid.New(), TopicThreadID: topicA, TopicTitle: "Entrega", TopicStatus: "open", Kind: "risk", Text: "pode gerar reclamação", Status: "dismissed", TruthLevel: "ai_inferred"},
			{ID: uuid.New(), TopicThreadID: topicB, TopicTitle: "Cobrança", TopicStatus: "resolved", Kind: "suggested_action", Text: "oferecer reembolso parcial", Status: "open", TruthLevel: "ai_inferred"},
		},
	}
	repo := &fakeAnalysisRepo{total: 4, lastInboundAt: &createdAt, row: row}
	v, err := NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v.AnalysisState != "ok" || v.Summary != "cliente perguntou sobre entrega" {
		t.Fatalf("view header = %+v", v)
	}
	if len(v.Topics) != 2 || v.Topics[0].TopicThreadID != topicA || len(v.Topics[0].Facts) != 3 || v.Topics[1].TopicThreadID != topicB {
		t.Fatalf("topics = %+v", v.Topics)
	}
	// dismissed risk must not appear as a pending action; the resolved commitment does
	if len(v.PendingActions) != 1 || v.PendingActions[0].Kind != "commitment" {
		t.Fatalf("pending actions = %+v", v.PendingActions)
	}
	if len(v.Suggestions) != 1 || v.Suggestions[0].Kind != "suggested_action" {
		t.Fatalf("suggestions = %+v", v.Suggestions)
	}
	if len(v.EvidenceMessageIDs) != 1 {
		t.Fatalf("evidence refs must dedupe/collect across every fact: %+v", v.EvidenceMessageIDs)
	}
}
