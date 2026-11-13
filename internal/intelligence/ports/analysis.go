package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// WindowMessageRow is one message of a closed context_analysis_window, reduced to what CONTEXT.2B needs. Text is
// messages.body, falling back to a DONE message_media_analysis row when the body is empty (an image/audio/PDF with
// nothing typed) — same convention the rest of ADR-0016 already uses. Empty means neither exists yet; the caller
// excludes it rather than sending a blank line to the model.
type WindowMessageRow struct {
	ID        uuid.UUID
	Inbound   bool
	Text      string
	At        time.Time
	FromMedia bool
}

// ResolvedFact is one contextdelta.Fact after its topic_alias was validated against the offered candidates and its
// evidence aliases resolved to real message ids — by the caller, never by this package. TopicThreadID is always a
// real, already-existing-or-just-created topic by the time Save is called (CONTEXT.2B never defers that decision
// to storage).
type ResolvedFact struct {
	TopicThreadID      uuid.UUID
	Kind               string
	Text               string
	EvidenceMessageIDs []uuid.UUID
}

// AnalysisRecord is one window's analysis outcome, ready to persist. ConversationID/WindowID/AnalyzedMessageIDs are
// the audit trail CONTEXT.2 asks for; Facts is empty exactly when Status is "empty" or "failed".
type AnalysisRecord struct {
	TenantID           uuid.UUID
	ConversationID     uuid.UUID
	WindowID           uuid.UUID
	Status             string // "ok" | "empty" | "failed"
	SummaryText        string
	ModelProvider      string
	ModelName          string
	PromptVersion      string
	InputTokens        int
	OutputTokens       int
	LatencyMS          int
	AnalyzedMessageIDs []uuid.UUID // oldest first; may be empty only when Status is "failed" before any message loaded
	ErrorClass         string
	Facts              []ResolvedFact
}

// ContextViewFact is one proposed fact as the CONTEXT.2C read model exposes it: joined with the topic it was filed
// under (title/status included so the caller never needs a second round trip). It is never rewritten in place —
// only Status/confirmation can change after creation (the migration 000177 trigger enforces that in the database).
type ContextViewFact struct {
	ID                 uuid.UUID
	TopicThreadID      uuid.UUID
	TopicTitle         string
	TopicStatus        string
	Kind               string
	Text               string
	TruthLevel         string
	Status             string
	EvidenceMessageIDs []uuid.UUID
	CreatedAt          time.Time
}

// ContextViewRow is the conversation's most recent analysis (whatever its Status) and its facts — the raw material
// behind GET .../context-view (CONTEXT.2C). LatestForConversation returns (nil, nil) when none exists yet.
type ContextViewRow struct {
	AnalysisID            uuid.UUID
	Status                string
	SummaryText           string
	CreatedAt             time.Time
	AnalyzedMessageCount  int
	AnalyzedLastMessageAt *time.Time
	Facts                 []ContextViewFact
}

// ContextAnalysisRepository persists CONTEXT.2B's structured output (migration 000177) and, for CONTEXT.2C, reads it
// back as an aggregated view. It never decides WHAT the facts are — that is contextdelta.Parse plus the caller's own
// alias resolution — only persists the decision, same split as ContextWindowStore for CONTEXT.2A.
type ContextAnalysisRepository interface {
	// WindowMessages loads the given messages (in any order; the caller already has the deterministic cursor from
	// ContextWindowStore.MessageIDs) scoped to tenantID/conversationID — a message of a different conversation (or
	// tenant) is simply absent from the result, never an error.
	WindowMessages(ctx context.Context, tenantID, conversationID uuid.UUID, messageIDs []uuid.UUID) ([]WindowMessageRow, error)
	// HasPendingMediaAnalysis reports whether any of the given messages has a media analysis still pending (not
	// done, failed, skipped or empty). WindowAnalyzer checks this before treating a window with no usable text as
	// definitively "empty": a media-only window whose transcript/description simply has not finished yet must be
	// retried, not completed forever with nothing analyzed (Codex review round 4).
	HasPendingMediaAnalysis(ctx context.Context, tenantID uuid.UUID, messageIDs []uuid.UUID) (bool, error)
	// Save persists the analysis receipt and, when Status is "ok", every fact and its evidence, atomically.
	// previous_analysis_id is resolved internally (the conversation's most recent prior analysis, if any) — the
	// caller never needs to look it up itself.
	Save(ctx context.Context, rec AnalysisRecord) (analysisID uuid.UUID, err error)
	// ExistsForWindow reports whether a window already has an analysis receipt. The runner's own Complete (a
	// separate call after Save commits) can fail to run at all if the worker crashes in between; WindowAnalyzer
	// checks this FIRST so a window reclaimed after such a crash recognizes its already-persisted result instead
	// of calling the model again and failing Save's UNIQUE(tenant_id, window_id) (Codex review round 2).
	ExistsForWindow(ctx context.Context, tenantID, windowID uuid.UUID) (bool, error)
	// LatestForConversation returns the conversation's most recent analysis and its facts, or (nil, nil) when
	// nothing has ever been analyzed for it — never an error for "nothing yet".
	LatestForConversation(ctx context.Context, tenantID, conversationID uuid.UUID) (*ContextViewRow, error)
	// ConversationCoverage reports how much of the conversation's inbound traffic exists right now (count, and the
	// newest inbound message's time) — shown in the API as coverage, but never used to decide staleness (two
	// messages sharing one timestamp, or an older message landing in a later window, defeat a timestamp compare).
	// total=0 means the conversation has no inbound message yet.
	ConversationCoverage(ctx context.Context, tenantID, conversationID uuid.UUID) (total int, lastMessageAt *time.Time, err error)
	// IsConversationFullyAnalyzed reports whether every inbound message of the conversation belongs to a window
	// that already has an analysis receipt (any status) — real message/window membership, the staleness boundary
	// CONTEXT.2C actually needs (Codex review round 2 flagged the timestamp-only version as missing same-timestamp
	// and out-of-order messages). Vacuously true when the conversation has no inbound message yet.
	IsConversationFullyAnalyzed(ctx context.Context, tenantID, conversationID uuid.UUID) (bool, error)
}
