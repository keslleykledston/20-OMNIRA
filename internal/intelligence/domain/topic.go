// Package domain holds the pure model of Conversation Intelligence (ADR-0017): TopicThread and its links. A
// Conversation is where a message happened; a TopicThread is the logical subject being handled (it can span
// conversations and channels); a Ticket is the operational process. Nothing here touches the network or a database.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type TopicStatus string

const (
	TopicOpen     TopicStatus = "open"
	TopicResolved TopicStatus = "resolved"
	TopicArchived TopicStatus = "archived"
)

type PrivacyPolicy string

const (
	PrivacyPublic             PrivacyPolicy = "public"
	PrivacyPrivateRecommended PrivacyPolicy = "private_recommended"
	PrivacyPrivateRequired    PrivacyPolicy = "private_required"
)

type TopicSource string

const (
	SourceManual         TopicSource = "manual"
	SourceRule           TopicSource = "rule"
	SourceAI             TopicSource = "ai"
	SourceHandoff        TopicSource = "handoff"
	SourceLegacyBackfill TopicSource = "legacy_backfill"
)

const MaxTitleRunes = 200

var (
	ErrTopicNotFound      = errors.New("topic not found")
	ErrInvalidTopic       = errors.New("invalid topic")
	ErrInvalidTransition  = errors.New("invalid topic status transition")
	ErrPrimaryTicketTaken = errors.New("topic already has a primary ticket")
	// ErrReferenceNotFound: a message, conversation, contact or ticket that does not exist in this tenant. Cross-tenant ids
	// land here too, so the answer never reveals whether the id exists elsewhere.
	ErrReferenceNotFound = errors.New("referenced entity not found")
)

// TopicThread is the logical subject of one or more messages.
type TopicThread struct {
	ID                   uuid.UUID
	TenantID             uuid.UUID
	PrimaryContactID     *uuid.UUID
	OriginConversationID *uuid.UUID
	Title                string
	Intent               *string
	Category             *string
	Status               TopicStatus
	PrivacyPolicy        PrivacyPolicy
	Source               TopicSource
	RoutingConfidence    *float64
	LegacyUnsegmented    bool
	LastActivityAt       time.Time
	CreatedByUserID      *uuid.UUID
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ResolvedAt           *time.Time
	// MergedIntoTopicID is set on a topic that was merged into another (it is archived, its history stays).
	MergedIntoTopicID *uuid.UUID
	// SplitFromTopicID is set on a topic created by splitting messages out of another.
	SplitFromTopicID *uuid.UUID
}

func (s TopicStatus) Valid() bool {
	return s == TopicOpen || s == TopicResolved || s == TopicArchived
}

func (p PrivacyPolicy) Valid() bool {
	return p == PrivacyPublic || p == PrivacyPrivateRecommended || p == PrivacyPrivateRequired
}

func (s TopicSource) Valid() bool {
	switch s {
	case SourceManual, SourceRule, SourceAI, SourceHandoff, SourceLegacyBackfill:
		return true
	}
	return false
}

// NormalizeTitle trims and bounds a title; it never invents one.
func NormalizeTitle(title string) (string, error) {
	t := strings.TrimSpace(title)
	if t == "" {
		return "", fmt.Errorf("%w: title is required", ErrInvalidTopic)
	}
	if utf8.RuneCountInString(t) > MaxTitleRunes {
		return "", fmt.Errorf("%w: title is longer than %d characters", ErrInvalidTopic, MaxTitleRunes)
	}
	return t, nil
}

// NewTopicThread builds an open topic. tenantID is mandatory and comes from the trusted tenant context.
func NewTopicThread(tenantID uuid.UUID, title string, source TopicSource, now time.Time) (*TopicThread, error) {
	if tenantID == uuid.Nil {
		return nil, fmt.Errorf("%w: tenant is required", ErrInvalidTopic)
	}
	if !source.Valid() {
		return nil, fmt.Errorf("%w: unknown source %q", ErrInvalidTopic, source)
	}
	t, err := NormalizeTitle(title)
	if err != nil {
		return nil, err
	}
	return &TopicThread{
		ID: uuid.New(), TenantID: tenantID, Title: t, Status: TopicOpen, PrivacyPolicy: PrivacyPublic,
		Source: source, LastActivityAt: now, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// Resolve closes an open topic. Resolving twice is an error so a retry is visible, not silent.
func (t *TopicThread) Resolve(now time.Time) error {
	if t.Status != TopicOpen {
		return fmt.Errorf("%w: %s -> resolved", ErrInvalidTransition, t.Status)
	}
	t.Status, t.ResolvedAt, t.UpdatedAt = TopicResolved, &now, now
	return nil
}

// Reopen turns a resolved topic back to open. An archived topic is final.
func (t *TopicThread) Reopen(now time.Time) error {
	if t.Status != TopicResolved {
		return fmt.Errorf("%w: %s -> open", ErrInvalidTransition, t.Status)
	}
	t.Status, t.ResolvedAt, t.UpdatedAt, t.LastActivityAt = TopicOpen, nil, now, now
	return nil
}

func (t *TopicThread) Archive(now time.Time) error {
	if t.Status == TopicArchived {
		return fmt.Errorf("%w: already archived", ErrInvalidTransition)
	}
	t.Status, t.UpdatedAt = TopicArchived, now
	return nil
}

// --- links ---

type MessageRelation string

const (
	RelationPrimary    MessageRelation = "primary"
	RelationSecondary  MessageRelation = "secondary"
	RelationSupporting MessageRelation = "supporting"
	RelationAmbiguous  MessageRelation = "ambiguous"
)

func (r MessageRelation) Valid() bool {
	switch r {
	case RelationPrimary, RelationSecondary, RelationSupporting, RelationAmbiguous:
		return true
	}
	return false
}

type DecisionSource string

const (
	DecisionExplicit DecisionSource = "explicit"
	DecisionHandoff  DecisionSource = "handoff"
	DecisionReply    DecisionSource = "reply"
	DecisionEntity   DecisionSource = "entity"
	DecisionRule     DecisionSource = "rule"
	DecisionAI       DecisionSource = "ai"
	DecisionAgent    DecisionSource = "agent"
	DecisionCustomer DecisionSource = "customer"
	DecisionLegacy   DecisionSource = "legacy"
)

func (d DecisionSource) Valid() bool {
	switch d {
	case DecisionExplicit, DecisionHandoff, DecisionReply, DecisionEntity, DecisionRule, DecisionAI, DecisionAgent, DecisionCustomer, DecisionLegacy:
		return true
	}
	return false
}

// MessageTopicLink says a message belongs to a topic. A message may have several (one per topic).
type MessageTopicLink struct {
	TenantID          uuid.UUID
	MessageID         uuid.UUID
	TopicThreadID     uuid.UUID
	Relation          MessageRelation
	Confidence        *float64
	DecisionSource    DecisionSource
	RoutingDecisionID *uuid.UUID
	CreatedAt         time.Time
}

func NewMessageTopicLink(tenantID, messageID, topicID uuid.UUID, relation MessageRelation, source DecisionSource, confidence *float64) (*MessageTopicLink, error) {
	if tenantID == uuid.Nil || messageID == uuid.Nil || topicID == uuid.Nil {
		return nil, fmt.Errorf("%w: tenant, message and topic are required", ErrInvalidTopic)
	}
	if !relation.Valid() {
		return nil, fmt.Errorf("%w: unknown relation %q", ErrInvalidTopic, relation)
	}
	if !source.Valid() {
		return nil, fmt.Errorf("%w: unknown decision source %q", ErrInvalidTopic, source)
	}
	if confidence != nil && (*confidence < 0 || *confidence > 1) {
		return nil, fmt.Errorf("%w: confidence must be between 0 and 1", ErrInvalidTopic)
	}
	return &MessageTopicLink{TenantID: tenantID, MessageID: messageID, TopicThreadID: topicID, Relation: relation, DecisionSource: source, Confidence: confidence}, nil
}

type ConversationRelation string

const (
	ConversationOrigin  ConversationRelation = "origin"
	ConversationActive  ConversationRelation = "active"
	ConversationRelated ConversationRelation = "related"
	ConversationHandoff ConversationRelation = "handoff"
)

func (r ConversationRelation) Valid() bool {
	switch r {
	case ConversationOrigin, ConversationActive, ConversationRelated, ConversationHandoff:
		return true
	}
	return false
}

type TopicConversationLink struct {
	TenantID        uuid.UUID
	TopicThreadID   uuid.UUID
	ConversationID  uuid.UUID
	Relation        ConversationRelation
	FirstActivityAt time.Time
	LastActivityAt  time.Time
	CreatedAt       time.Time
}

type TicketRelation string

const (
	TicketPrimary TicketRelation = "primary"
	TicketRelated TicketRelation = "related"
	TicketChild   TicketRelation = "child"
	TicketMerged  TicketRelation = "merged"
)

func (r TicketRelation) Valid() bool {
	return r == TicketPrimary || r == TicketRelated || r == TicketChild || r == TicketMerged
}

type TicketLinkOrigin string

const (
	TicketLinkAgent          TicketLinkOrigin = "agent"
	TicketLinkRule           TicketLinkOrigin = "rule"
	TicketLinkAI             TicketLinkOrigin = "ai"
	TicketLinkSystem         TicketLinkOrigin = "system"
	TicketLinkLegacyBackfill TicketLinkOrigin = "legacy_backfill"
)

func (o TicketLinkOrigin) Valid() bool {
	switch o {
	case TicketLinkAgent, TicketLinkRule, TicketLinkAI, TicketLinkSystem, TicketLinkLegacyBackfill:
		return true
	}
	return false
}

type TopicTicketLink struct {
	TenantID        uuid.UUID
	TopicThreadID   uuid.UUID
	TicketID        uuid.UUID
	Relation        TicketRelation
	CreatedBy       TicketLinkOrigin
	CreatedByUserID *uuid.UUID
	CreatedAt       time.Time
}

func NewTopicTicketLink(tenantID, topicID, ticketID uuid.UUID, relation TicketRelation, by TicketLinkOrigin, userID *uuid.UUID) (*TopicTicketLink, error) {
	if tenantID == uuid.Nil || topicID == uuid.Nil || ticketID == uuid.Nil {
		return nil, fmt.Errorf("%w: tenant, topic and ticket are required", ErrInvalidTopic)
	}
	if !relation.Valid() || !by.Valid() {
		return nil, fmt.Errorf("%w: invalid ticket link relation or origin", ErrInvalidTopic)
	}
	return &TopicTicketLink{TenantID: tenantID, TopicThreadID: topicID, TicketID: ticketID, Relation: relation, CreatedBy: by, CreatedByUserID: userID}, nil
}

// --- summaries ---

type SummaryStatus string

const (
	SummaryAIInferred        SummaryStatus = "ai_inferred"
	SummaryCustomerConfirmed SummaryStatus = "customer_confirmed"
	SummaryAgentConfirmed    SummaryStatus = "agent_confirmed"
	SummaryCorrected         SummaryStatus = "corrected"
	SummarySuperseded        SummaryStatus = "superseded"
)

func (s SummaryStatus) Valid() bool {
	switch s {
	case SummaryAIInferred, SummaryCustomerConfirmed, SummaryAgentConfirmed, SummaryCorrected, SummarySuperseded:
		return true
	}
	return false
}

// TopicSummary is one immutable version of a topic summary; a change is always a new version.
type TopicSummary struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	TopicThreadID     uuid.UUID
	Version           int
	SummaryText       string
	StructuredContext []byte // JSON object
	Status            SummaryStatus
	SourceSummaryID   *uuid.UUID
	ModelProvider     *string
	ModelName         *string
	PromptVersion     *string
	CreatedByUserID   *uuid.UUID
	CreatedAt         time.Time
	ConfirmedAt       *time.Time
}
