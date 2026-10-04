package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
)

// MessageKind: a routable message is a conversation message or a WhatsApp group message (ADR-0015 keeps them apart).
type MessageKind string

const (
	KindConversation MessageKind = "conversation"
	KindGroup        MessageKind = "group"
)

type MessageRef struct {
	Kind MessageKind
	ID   uuid.UUID
}

// RoutableMessage is what the router needs about one message, loaded under the tenant's RLS.
type RoutableMessage struct {
	Ref           MessageRef
	ContainerID   uuid.UUID  // conversation id or group id
	ContactID     *uuid.UUID // conversation messages only
	ParticipantID *uuid.UUID
	Text          string
	Inbound       bool
	CreatedAt     time.Time
	// ReplyTo is the message this one replies to or quotes, resolved inside the same container, if any.
	ReplyTo *MessageRef
}

// StoredDecision is a persisted routing decision.
type StoredDecision struct {
	ID       uuid.UUID
	Status   domain.RoutingStatus
	Selected *uuid.UUID
	Applied  bool
}

// DecisionRecord is what is written for a decision (applied or only proposed).
type DecisionRecord struct {
	Ref           MessageRef
	Status        domain.RoutingStatus
	Selected      *uuid.UUID
	Source        domain.DecisionSource
	Applied       bool
	Confidence    *float64
	Signals       json.RawMessage
	ModelProvider *string
	ModelName     *string
	PromptVersion *string
	InputTokens   *int
	OutputTokens  *int
	LatencyMS     *int
}

// RoutingRepository is the storage the routing use case needs. Every method runs inside the caller's tenant session.
type RoutingRepository interface {
	LoadRoutable(ctx context.Context, tenantID uuid.UUID, ref MessageRef) (*RoutableMessage, error)
	ActiveDecision(ctx context.Context, tenantID uuid.UUID, ref MessageRef) (*StoredDecision, error)
	// TopicsOfMessage: the topics a message already belongs to (used to inherit them through a reply).
	TopicsOfMessage(ctx context.Context, tenantID uuid.UUID, ref MessageRef) ([]uuid.UUID, error)
	// EntityTopics maps Entity.String() to the OPEN topics that already hold it (ticket entities also match the
	// external id of tickets linked to a topic).
	EntityTopics(ctx context.Context, tenantID uuid.UUID, entities []domain.Entity) (map[string][]uuid.UUID, error)
	ParticipantTopics(ctx context.Context, tenantID uuid.UUID, ref MessageRef, container, participant uuid.UUID, limit int) ([]domain.ParticipantTopic, error)
	Focus(ctx context.Context, tenantID uuid.UUID, ref MessageRef, container uuid.UUID, participant *uuid.UUID) ([]domain.FocusHint, error)
	OpenTopics(ctx context.Context, tenantID uuid.UUID, ref MessageRef, container uuid.UUID, limit int) ([]domain.TopicBrief, error)

	// ProposalExists: a not-applied proposal of this source (an AI shadow decision, a dry run) already exists for the message.
	ProposalExists(ctx context.Context, tenantID uuid.UUID, ref MessageRef, source domain.DecisionSource) (bool, error)
	// SaveDecision persists a decision. An applied decision for a message that already has an active one returns the
	// existing id with inserted=false (a replay never creates a second). A proposal (not applied) refreshes the
	// previous proposal of the same source.
	SaveDecision(ctx context.Context, tenantID uuid.UUID, d DecisionRecord) (id uuid.UUID, inserted bool, err error)
	LinkMessage(ctx context.Context, tenantID uuid.UUID, ref MessageRef, topicID uuid.UUID, relation domain.MessageRelation, source domain.DecisionSource, confidence *float64, decisionID *uuid.UUID) error
	LinkContainer(ctx context.Context, tenantID uuid.UUID, ref MessageRef, container, topicID uuid.UUID) error
	UpsertEntities(ctx context.Context, tenantID, topicID uuid.UUID, entities []domain.Entity, sourceMessage *uuid.UUID, source string) error
	SetFocus(ctx context.Context, tenantID uuid.UUID, ref MessageRef, container uuid.UUID, participant *uuid.UUID, topicID uuid.UUID, source string, confidence float64, expires time.Time) error
	TouchTopic(ctx context.Context, tenantID, topicID uuid.UUID) error

	CreateAmbiguity(ctx context.Context, tenantID uuid.UUID, ref MessageRef, candidates json.RawMessage) (uuid.UUID, error)
	GetAmbiguity(ctx context.Context, tenantID, id uuid.UUID) (*Ambiguity, error)
	ListOpenAmbiguities(ctx context.Context, tenantID uuid.UUID, kind MessageKind, container uuid.UUID) ([]Ambiguity, error)
	ResolveAmbiguity(ctx context.Context, tenantID, id, topicID uuid.UUID, source string, byUser *uuid.UUID) (bool, error)
	MarkDecisionOverridden(ctx context.Context, tenantID uuid.UUID, ref MessageRef, byUser *uuid.UUID) error
}

// Ambiguity is an open question for a person: which topic does this message belong to?
type Ambiguity struct {
	ID         uuid.UUID
	Ref        MessageRef
	Container  uuid.UUID
	Status     string
	Candidates json.RawMessage
	CreatedAt  time.Time
	ResolvedTo *uuid.UUID
	AssignedTo *uuid.UUID // conversation attendant, for the authorization rule
}
