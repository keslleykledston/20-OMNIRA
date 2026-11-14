package application

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/ports"
)

// ContextViewFact mirrors ports.ContextViewFact for the API-facing shape — the application layer owns the grouping
// and staleness rules; adapters only persist/read rows and the HTTP layer only serializes.
type ContextViewFact struct {
	ID                 uuid.UUID
	Kind               string
	Text               string
	TruthLevel         string
	Status             string
	EvidenceMessageIDs []uuid.UUID
	CreatedAt          time.Time
}

// ContextViewTopic groups a conversation's proposed facts by the topic they were filed under, in the order each
// topic first appears among the analysis's facts (oldest fact first).
type ContextViewTopic struct {
	TopicThreadID uuid.UUID
	Title         string
	Status        string
	Facts         []ContextViewFact
}

type ContextViewCoverage struct {
	AnalyzedMessageCount     int
	TotalInboundMessageCount int
	LastAnalyzedMessageAt    *time.Time
}

// ContextView is CONTEXT.2C's read model for one conversation: the server's own aggregation over
// context_window_analyses (CONTEXT.2B) and the conversation's current inbound traffic — never the model's own
// words taken as fact, and never a decision the operator did not make. AnalysisState is "none" when nothing has
// ever been analyzed for this conversation yet.
type ContextView struct {
	AnalysisState      string // none | ok | empty | failed
	UpdatedAt          *time.Time
	Summary            string
	Stale              bool
	Coverage           ContextViewCoverage
	Topics             []ContextViewTopic
	PendingActions     []ContextViewFact
	Suggestions        []ContextViewFact
	EvidenceMessageIDs []uuid.UUID
}

// pendingActionKinds are open facts an operator should confirm or dismiss as still-open tracking items. observation
// is context only (never actionable) and suggested_action is surfaced separately under Suggestions, since it reads
// as "do this" rather than "this is true".
var pendingActionKinds = map[string]bool{"need": true, "commitment": true, "possible_resolution": true, "risk": true}

// ContextViewService builds CONTEXT.2C's read model. It never calls a model and never writes: everything it
// returns was already decided (by CONTEXT.2B's analysis, by an operator's confirm/dismiss) or is a straightforward
// comparison against the conversation's own message count.
type ContextViewService struct {
	analysis ports.ContextAnalysisRepository
}

func NewContextViewService(analysis ports.ContextAnalysisRepository) *ContextViewService {
	return &ContextViewService{analysis: analysis}
}

func (s *ContextViewService) Get(ctx context.Context, tenantID, conversationID uuid.UUID) (ContextView, error) {
	total, _, err := s.analysis.ConversationCoverage(ctx, tenantID, conversationID)
	if err != nil {
		return ContextView{}, err
	}
	view := ContextView{AnalysisState: "none", Coverage: ContextViewCoverage{TotalInboundMessageCount: total}}

	// Message/window membership, never a timestamp compare: two messages can share a created_at, and an older
	// message can land in a later window (out-of-order persistence) — both defeat a pure "After" check while
	// this exact membership never does (Codex review round 2).
	fullyAnalyzed, err := s.analysis.IsConversationFullyAnalyzed(ctx, tenantID, conversationID)
	if err != nil {
		return ContextView{}, err
	}
	view.Stale = !fullyAnalyzed

	row, err := s.analysis.LatestForConversation(ctx, tenantID, conversationID)
	if err != nil {
		return ContextView{}, err
	}
	if row == nil {
		return view, nil
	}

	view.AnalysisState = row.Status
	view.Summary = row.SummaryText
	createdAt := row.CreatedAt
	view.UpdatedAt = &createdAt
	view.Coverage.AnalyzedMessageCount = row.AnalyzedMessageCount
	view.Coverage.LastAnalyzedMessageAt = row.AnalyzedLastMessageAt

	var topicOrder []uuid.UUID
	topics := map[uuid.UUID]*ContextViewTopic{}
	evidenceSeen := map[uuid.UUID]bool{}
	for _, f := range row.Facts {
		t, ok := topics[f.TopicThreadID]
		if !ok {
			t = &ContextViewTopic{TopicThreadID: f.TopicThreadID, Title: f.TopicTitle, Status: f.TopicStatus}
			topics[f.TopicThreadID] = t
			topicOrder = append(topicOrder, f.TopicThreadID)
		}
		vf := ContextViewFact{ID: f.ID, Kind: f.Kind, Text: f.Text, TruthLevel: f.TruthLevel, Status: f.Status,
			EvidenceMessageIDs: f.EvidenceMessageIDs, CreatedAt: f.CreatedAt}
		t.Facts = append(t.Facts, vf)
		if f.Status == "open" {
			switch {
			case f.Kind == "suggested_action":
				view.Suggestions = append(view.Suggestions, vf)
			case pendingActionKinds[f.Kind]:
				view.PendingActions = append(view.PendingActions, vf)
			}
		}
		for _, mid := range f.EvidenceMessageIDs {
			if !evidenceSeen[mid] {
				evidenceSeen[mid] = true
				view.EvidenceMessageIDs = append(view.EvidenceMessageIDs, mid)
			}
		}
	}
	for _, id := range topicOrder {
		view.Topics = append(view.Topics, *topics[id])
	}
	return view, nil
}
