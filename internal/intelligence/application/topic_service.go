// Package application orchestrates Conversation Intelligence use cases (ADR-0017). The tenant and the actor always come
// from the trusted TenantContext, never from a parameter or a payload.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// TopicService is the single entry point for creating and organising topics.
type TopicService struct {
	repo ports.TopicRepository
	now  func() time.Time
}

func NewTopicService(repo ports.TopicRepository) *TopicService {
	return &TopicService{repo: repo, now: time.Now}
}

func (s *TopicService) tenant(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("intelligence: tenant context required")
	}
	return tc, nil
}

// CreateTopicInput is what an operator supplies; the contact and origin come from the conversation.
type CreateTopicInput struct {
	ConversationID uuid.UUID
	ContactID      *uuid.UUID
	Title          string
	Intent         *string
	Category       *string
	Privacy        domain.PrivacyPolicy
	Source         domain.TopicSource
	MessageIDs     []uuid.UUID
}

// CreateTopic creates an open topic anchored to a conversation and links the given messages to it as primary, as an
// agent decision. Every referenced id must exist in the caller's tenant.
func (s *TopicService) CreateTopic(ctx context.Context, in CreateTopicInput) (*domain.TopicThread, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	source := in.Source
	if source == "" {
		source = domain.SourceManual
	}
	topic, err := domain.NewTopicThread(tc.TenantID, in.Title, source, s.now().UTC())
	if err != nil {
		return nil, err
	}
	if in.Privacy != "" {
		if !in.Privacy.Valid() {
			return nil, fmt.Errorf("%w: unknown privacy policy", domain.ErrInvalidTopic)
		}
		topic.PrivacyPolicy = in.Privacy
	}
	topic.Intent, topic.Category = in.Intent, in.Category
	topic.PrimaryContactID = in.ContactID
	if in.ConversationID != uuid.Nil {
		cid := in.ConversationID
		topic.OriginConversationID = &cid
	}
	actor := tc.ActorID
	if actor != uuid.Nil {
		topic.CreatedByUserID = &actor
	}
	if err := s.repo.CreateTopic(ctx, topic); err != nil {
		return nil, err
	}
	for _, id := range in.MessageIDs {
		link, err := domain.NewMessageTopicLink(tc.TenantID, id, topic.ID, domain.RelationPrimary, domain.DecisionAgent, nil)
		if err != nil {
			return nil, err
		}
		if err := s.repo.LinkMessage(ctx, link); err != nil {
			return nil, err
		}
	}
	return topic, nil
}

func (s *TopicService) GetTopic(ctx context.Context, id uuid.UUID) (*domain.TopicThread, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	return s.repo.GetTopic(ctx, tc.TenantID, id)
}

func (s *TopicService) ListConversationTopics(ctx context.Context, conversationID uuid.UUID) ([]ports.TopicListItem, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	return s.repo.ListConversationTopics(ctx, tc.TenantID, conversationID)
}

func (s *TopicService) ListContactTopics(ctx context.Context, contactID uuid.UUID, status *domain.TopicStatus, limit int) ([]ports.TopicListItem, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	return s.repo.ListContactTopics(ctx, tc.TenantID, contactID, status, limit)
}

// UpdateTopicInput carries only the fields to change.
type UpdateTopicInput struct {
	Title    *string
	Intent   *string
	Category *string
	Privacy  *domain.PrivacyPolicy
	// Status accepts "open" (reopen) or "resolved" (resolve); archiving is a separate, explicit operation.
	Status *domain.TopicStatus
}

func (s *TopicService) UpdateTopic(ctx context.Context, id uuid.UUID, in UpdateTopicInput) (*domain.TopicThread, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	topic, err := s.repo.GetTopic(ctx, tc.TenantID, id)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if in.Title != nil {
		t, err := domain.NormalizeTitle(*in.Title)
		if err != nil {
			return nil, err
		}
		topic.Title = t
	}
	if in.Intent != nil {
		topic.Intent = in.Intent
	}
	if in.Category != nil {
		topic.Category = in.Category
	}
	if in.Privacy != nil {
		if !in.Privacy.Valid() {
			return nil, fmt.Errorf("%w: unknown privacy policy", domain.ErrInvalidTopic)
		}
		topic.PrivacyPolicy = *in.Privacy
	}
	if in.Status != nil && *in.Status != topic.Status {
		switch *in.Status {
		case domain.TopicResolved:
			err = topic.Resolve(now)
		case domain.TopicOpen:
			err = topic.Reopen(now)
		default:
			err = fmt.Errorf("%w: status %q cannot be set here", domain.ErrInvalidTransition, *in.Status)
		}
		if err != nil {
			return nil, err
		}
	}
	topic.UpdatedAt = now
	if err := s.repo.UpdateTopic(ctx, topic); err != nil {
		return nil, err
	}
	return topic, nil
}

// LinkMessage associates a message with a topic as an agent decision.
func (s *TopicService) LinkMessage(ctx context.Context, topicID, messageID uuid.UUID, relation domain.MessageRelation) error {
	tc, err := s.tenant(ctx)
	if err != nil {
		return err
	}
	if _, err := s.repo.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return err
	}
	link, err := domain.NewMessageTopicLink(tc.TenantID, messageID, topicID, relation, domain.DecisionAgent, nil)
	if err != nil {
		return err
	}
	return s.repo.LinkMessage(ctx, link)
}

func (s *TopicService) UnlinkMessage(ctx context.Context, topicID, messageID uuid.UUID) error {
	tc, err := s.tenant(ctx)
	if err != nil {
		return err
	}
	if _, err := s.repo.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return err
	}
	removed, err := s.repo.UnlinkMessage(ctx, tc.TenantID, messageID, topicID)
	if err != nil {
		return err
	}
	if !removed {
		return domain.ErrReferenceNotFound
	}
	return nil
}

// LinkConversation relates a topic to another conversation (the same subject continuing elsewhere).
func (s *TopicService) LinkConversation(ctx context.Context, topicID, conversationID uuid.UUID, relation domain.ConversationRelation) error {
	tc, err := s.tenant(ctx)
	if err != nil {
		return err
	}
	if !relation.Valid() {
		return fmt.Errorf("%w: unknown conversation relation", domain.ErrInvalidTopic)
	}
	if _, err := s.repo.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return err
	}
	return s.repo.LinkConversation(ctx, &domain.TopicConversationLink{TenantID: tc.TenantID, TopicThreadID: topicID, ConversationID: conversationID, Relation: relation})
}

// LinkTicket relates an existing ticket of the tenant to a topic. It never creates a ticket.
func (s *TopicService) LinkTicket(ctx context.Context, topicID, ticketID uuid.UUID, relation domain.TicketRelation) error {
	tc, err := s.tenant(ctx)
	if err != nil {
		return err
	}
	if _, err := s.repo.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return err
	}
	var by *uuid.UUID
	if tc.ActorID != uuid.Nil {
		a := tc.ActorID
		by = &a
	}
	link, err := domain.NewTopicTicketLink(tc.TenantID, topicID, ticketID, relation, domain.TicketLinkAgent, by)
	if err != nil {
		return err
	}
	return s.repo.LinkTicket(ctx, link)
}

func (s *TopicService) ListTopicMessages(ctx context.Context, topicID uuid.UUID, cursor *pagination.Cursor, limit int) ([]ports.TopicMessage, bool, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, false, err
	}
	if _, err := s.repo.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return nil, false, err
	}
	return s.repo.ListTopicMessages(ctx, tc.TenantID, topicID, cursor, limit)
}

func (s *TopicService) ListTopicTickets(ctx context.Context, topicID uuid.UUID) ([]ports.TopicTicket, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return nil, err
	}
	return s.repo.ListTopicTickets(ctx, tc.TenantID, topicID)
}
