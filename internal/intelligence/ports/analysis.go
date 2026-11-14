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

// MediaPipelines says which of ADR-0016's two derived-text pipelines are actually running right now: transcription
// (audio, internal/media/application.TranscriptionProcessor, needs OMNIRA_WHISPER_URL) and vision (image/PDF,
// VisionProcessor, needs OMNIRA_MULTIMODAL_ANALYSIS_ENABLED — and even then, a tenant must separately opt in via
// tenant_ai_integrations before EnqueueVision ever looks at its files). Note the Go zero value is {false, false}
// (neither live) — callers that have no real flag to pass use AllMediaPipelinesLive instead, the assumption every
// caller in this package made before this type existed (Codex review round 7).
type MediaPipelines struct {
	TranscriptionEnabled bool
	VisionEnabled        bool
	// VisionLookback must match VisionProcessor.Lookback (7 days by default, internal/media/application/vision.go):
	// EnqueueVision only enqueues files created within this window, so a clean, unanalyzed image/PDF older than
	// it will NEVER get a receipt no matter how long a window waits (Codex review round 8). Zero means "use the
	// same 7-day default VisionProcessor itself uses unless main.go configures otherwise" (see AllMediaPipelinesLive).
	VisionLookback time.Duration
}

// DefaultVisionLookback mirrors VisionProcessor's own default (internal/media/application/vision.go) — the two
// must stay in sync, or HasPendingMediaAnalysis waits for files EnqueueVision itself would already have given up on.
const DefaultVisionLookback = 7 * 24 * time.Hour

// AllMediaPipelinesLive is MediaPipelines{true, true, DefaultVisionLookback} — the pre-round-7 assumption, for a
// caller with no real flag to pass (tests, or any session with no worker config in reach). NOT the type's zero value.
var AllMediaPipelinesLive = MediaPipelines{TranscriptionEnabled: true, VisionEnabled: true, VisionLookback: DefaultVisionLookback}

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
	// HasPendingMediaAnalysis reports whether any of the given messages has media whose outcome is not known yet
	// — a transcript/description/document_text row still 'pending', a file still being virus-scanned, or an
	// already-clean image/PDF not yet picked up by the next vision-enqueue tick. WindowAnalyzer checks this
	// before treating a window with no usable text as definitively "empty": that media-only window must be
	// retried, not completed forever with nothing analyzed (Codex review round 4).
	//
	// pipelines says which of the two media pipelines is actually LIVE right now: without it, this would wait
	// forever for a tenant that never opted into vision, or a deployment with transcription turned off, since
	// neither of those ever produces a terminal outcome on its own (Codex review round 7). A kind this message's
	// media is NOT eligible for (the wrong pipeline disabled, the tenant not opted into vision, or the file
	// already purged) never counts as pending, however long it has sat unanalyzed.
	HasPendingMediaAnalysis(ctx context.Context, tenantID uuid.UUID, messageIDs []uuid.UUID, pipelines MediaPipelines) (bool, error)
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
