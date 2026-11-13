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

// ContextAnalysisRepository persists CONTEXT.2B's structured output (migration 000177). It never decides WHAT the
// facts are — that is contextdelta.Parse plus the caller's own alias resolution — only persists the decision, same
// split as ContextWindowStore for CONTEXT.2A.
type ContextAnalysisRepository interface {
	// WindowMessages loads the given messages (in any order; the caller already has the deterministic cursor from
	// ContextWindowStore.MessageIDs) scoped to tenantID/conversationID — a message of a different conversation (or
	// tenant) is simply absent from the result, never an error.
	WindowMessages(ctx context.Context, tenantID, conversationID uuid.UUID, messageIDs []uuid.UUID) ([]WindowMessageRow, error)
	// Save persists the analysis receipt and, when Status is "ok", every fact and its evidence, atomically.
	// previous_analysis_id is resolved internally (the conversation's most recent prior analysis, if any) — the
	// caller never needs to look it up itself.
	Save(ctx context.Context, rec AnalysisRecord) (analysisID uuid.UUID, err error)
}
