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
	fullyAnalyzed          bool
	coverageErr, latestErr error
}

func (f *fakeAnalysisRepo) WindowMessages(ctx context.Context, tenantID, conversationID uuid.UUID, messageIDs []uuid.UUID) ([]ports.WindowMessageRow, error) {
	return nil, nil
}
func (f *fakeAnalysisRepo) Save(ctx context.Context, rec ports.AnalysisRecord) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeAnalysisRepo) ExistsForWindow(ctx context.Context, tenantID, windowID uuid.UUID) (bool, error) {
	return false, nil
}
func (f *fakeAnalysisRepo) HasPendingMediaAnalysis(ctx context.Context, tenantID uuid.UUID, messageIDs []uuid.UUID) (bool, error) {
	return false, nil
}
func (f *fakeAnalysisRepo) LatestForConversation(ctx context.Context, tenantID, conversationID uuid.UUID) (*ports.ContextViewRow, error) {
	return f.row, f.latestErr
}
func (f *fakeAnalysisRepo) ConversationCoverage(ctx context.Context, tenantID, conversationID uuid.UUID) (int, *time.Time, error) {
	return f.total, f.lastInboundAt, f.coverageErr
}
func (f *fakeAnalysisRepo) IsConversationFullyAnalyzed(ctx context.Context, tenantID, conversationID uuid.UUID) (bool, error) {
	return f.fullyAnalyzed, nil
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

	repo := &fakeAnalysisRepo{row: nil, total: 0, fullyAnalyzed: true}
	v, err := NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil || v.AnalysisState != "none" || v.Stale {
		t.Fatalf("empty conversation: %+v err=%v", v, err)
	}

	repo = &fakeAnalysisRepo{row: nil, total: 3, fullyAnalyzed: false}
	v, err = NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil || v.AnalysisState != "none" || !v.Stale {
		t.Fatalf("messages but nothing analyzed: %+v err=%v", v, err)
	}
}

// Staleness is message/window MEMBERSHIP (IsConversationFullyAnalyzed), never a timestamp compare: two messages
// sharing a created_at, or an older message landing in a later window, would defeat an "After" check (Codex
// review round 2) but never defeat exact membership.
func TestContextViewStalenessFollowsMessageWindowMembershipNotTimestamps(t *testing.T) {
	ctx := context.Background()
	tenant, conv := uuid.New(), uuid.New()
	analyzedAt := at("2026-11-13T10:00:00Z")

	// every inbound message belongs to an analyzed window: not stale, regardless of what the timestamps say.
	repo := &fakeAnalysisRepo{total: 5, fullyAnalyzed: true, row: &ports.ContextViewRow{Status: "ok", AnalyzedLastMessageAt: &analyzedAt}}
	v, err := NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil || v.Stale {
		t.Fatalf("fully analyzed must not be stale: %+v err=%v", v, err)
	}

	// a message exists that is not yet covered by any analyzed window: stale, even if its timestamp happens to be
	// older than or equal to AnalyzedLastMessageAt (out-of-order delivery, or two messages sharing a timestamp).
	repo = &fakeAnalysisRepo{total: 6, fullyAnalyzed: false, row: &ports.ContextViewRow{Status: "ok", AnalyzedLastMessageAt: &analyzedAt}}
	v, err = NewContextViewService(repo).Get(ctx, tenant, conv)
	if err != nil || !v.Stale {
		t.Fatalf("an unanalyzed message must mark the view stale: %+v err=%v", v, err)
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
	repo := &fakeAnalysisRepo{total: 4, lastInboundAt: &createdAt, fullyAnalyzed: true, row: row}
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
