package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
)

// ContextRow is a message of a topic as the context builder reads it (before aliasing and bounding).
type ContextRow struct {
	ID             uuid.UUID
	Kind           MessageKind
	Role           domain.Role
	ParticipantKey string // opaque, only used to hand out stable aliases inside one context
	Text           string
	At             time.Time
	Relation       domain.MessageRelation
	FromMedia      bool
}

// TopicStats are the totals of a topic across both conversation and group messages.
type TopicStats struct {
	Messages     int
	Participants int
	LastAt       *time.Time
}

// ContextRepository reads ONLY material linked to the given topic (through the link tables), under the tenant's RLS.
type ContextRepository interface {
	RecentTopicMessages(ctx context.Context, tenantID, topicID uuid.UUID, limit int) ([]ContextRow, error)
	RelevantTopicMessages(ctx context.Context, tenantID, topicID uuid.UUID, exclude []uuid.UUID, words []string, limit int) ([]ContextRow, error)
	TopicStats(ctx context.Context, tenantID, topicID uuid.UUID) (TopicStats, error)
	TopicEntities(ctx context.Context, tenantID, topicID uuid.UUID) ([]domain.EntityContext, error)
	TopicTickets(ctx context.Context, tenantID, topicID uuid.UUID) ([]domain.TicketContext, error)
	TopicMedia(ctx context.Context, tenantID, topicID uuid.UUID, limit int) ([]domain.MediaContext, error)
	// TopicMessage loads one message AS SEEN IN this topic; a message that is not linked to it is domain.ErrReferenceNotFound.
	TopicMessage(ctx context.Context, tenantID, topicID uuid.UUID, ref MessageRef) (*ContextRow, error)
}

// SummaryRepository stores versioned summaries. History is never rewritten: a change is a new version.
type SummaryRepository interface {
	// Create assigns the next version atomically and returns the stored summary.
	Create(ctx context.Context, s *domain.TopicSummary) (*domain.TopicSummary, error)
	List(ctx context.Context, tenantID, topicID uuid.UUID) ([]domain.TopicSummary, error)
	Latest(ctx context.Context, tenantID, topicID uuid.UUID) (*domain.TopicSummary, error)
	// LatestConfirmed is the newest version a person or the customer stands behind (confirmed or corrected).
	LatestConfirmed(ctx context.Context, tenantID, topicID uuid.UUID) (*domain.TopicSummary, error)
	LatestInferred(ctx context.Context, tenantID, topicID uuid.UUID) (*domain.TopicSummary, error)
	// Confirm marks a CURRENT version as confirmed; a superseded one cannot be confirmed.
	Confirm(ctx context.Context, tenantID, summaryID uuid.UUID, status domain.SummaryStatus, by *uuid.UUID) (bool, error)
	// LockForGeneration serialises summary generation for one topic until the surrounding transaction ends, so two workers
	// never both generate for the same state of the topic.
	LockForGeneration(ctx context.Context, tenantID, topicID uuid.UUID) error
	// Supersede marks one version as replaced by a newer one (its content stays).
	Supersede(ctx context.Context, tenantID, summaryID uuid.UUID) error
	// SupersedeInferredBefore marks older machine summaries superseded. Confirmed and corrected versions are never touched.
	SupersedeInferredBefore(ctx context.Context, tenantID, topicID uuid.UUID, version int) error
}

// SummaryResult is what a summarizer produced for a topic.
type SummaryResult struct {
	Text              string
	StructuredContext json.RawMessage
	Provider          string
	Model             string
	PromptVersion     string
}

// TopicSummarizer turns a topic context into a summary. The engine behind it is replaceable (ADR-0017).
type TopicSummarizer interface {
	SummarizeTopic(ctx context.Context, in domain.TopicContext) (SummaryResult, error)
}

// ErrSummarizerUnavailable: no summarizer is configured, or the provider failed; the caller degrades, never blocks.
var ErrSummarizerUnavailable = errors.New("intelligence: topic summarizer unavailable")
