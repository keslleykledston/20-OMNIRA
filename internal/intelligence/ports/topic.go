// Package ports declares what Conversation Intelligence needs from storage and from the rest of OMNIRA.
package ports

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/platform/pagination"
)

// TopicListItem is a topic with the counts a sidebar needs.
type TopicListItem struct {
	Topic         domain.TopicThread
	MessageCount  int
	TicketCount   int
	LastMessageAt *time.Time
}

// TopicMessage is a message as seen through a topic (the logical timeline).
type TopicMessage struct {
	ID             uuid.UUID
	ConversationID uuid.UUID
	Direction      string
	MessageType    string
	Body           string
	MimeType       string
	Status         string
	CreatedAt      time.Time
	Relation       domain.MessageRelation
	Confidence     *float64
	DecisionSource domain.DecisionSource
}

// TopicTicket is a ticket as seen through a topic.
type TopicTicket struct {
	ID               uuid.UUID
	Status           string
	Priority         string
	Subject          string
	Provider         *string
	ExternalTicketID *string
	Relation         domain.TicketRelation
	LinkedAt         time.Time
}

// ConversationInfo is the little the Intelligence module needs to know about a conversation.
type ConversationInfo struct {
	Exists     bool
	ContactID  uuid.UUID
	AssignedTo *uuid.UUID
}

// TopicRepository persists topics and their links. Every method runs inside the caller's tenant session (RLS) and takes
// the tenant explicitly as defence in depth. A reference that does not exist in the tenant is domain.ErrReferenceNotFound.
type TopicRepository interface {
	CreateTopic(ctx context.Context, t *domain.TopicThread) error
	GetTopic(ctx context.Context, tenantID, id uuid.UUID) (*domain.TopicThread, error)
	UpdateTopic(ctx context.Context, t *domain.TopicThread) error

	ListConversationTopics(ctx context.Context, tenantID, conversationID uuid.UUID) ([]TopicListItem, error)
	ListContactTopics(ctx context.Context, tenantID, contactID uuid.UUID, status *domain.TopicStatus, limit int) ([]TopicListItem, error)

	// LinkMessage is idempotent. An existing link is replaced only by a decision of equal or higher authority, so a
	// replayed or later AI decision can never overwrite what an agent or the customer decided.
	LinkMessage(ctx context.Context, l *domain.MessageTopicLink) error
	UnlinkMessage(ctx context.Context, tenantID, messageID, topicID uuid.UUID) (bool, error)
	LinkConversation(ctx context.Context, l *domain.TopicConversationLink) error
	LinkTicket(ctx context.Context, l *domain.TopicTicketLink) error

	ListTopicMessages(ctx context.Context, tenantID, topicID uuid.UUID, cursor *pagination.Cursor, limit int) ([]TopicMessage, bool, error)
	ListTopicTickets(ctx context.Context, tenantID, topicID uuid.UUID) ([]TopicTicket, error)

	// ConversationInfo reports whether the conversation exists in the tenant, its contact and who handles it.
	ConversationInfo(ctx context.Context, tenantID, conversationID uuid.UUID) (ConversationInfo, error)
	// ActorOperatesTopic: the actor is the assignee of a conversation linked to the topic.
	ActorOperatesTopic(ctx context.Context, tenantID, topicID, userID uuid.UUID) (bool, error)
}
