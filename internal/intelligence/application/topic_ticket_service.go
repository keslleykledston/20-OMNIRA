package application

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// TopicTicketService applies the ticket policy (ADR-0017 Wave 7). Tickets stay conversation-scoped (one active per
// conversation); the topic<->ticket link is how a subject gets its operational process. Automation (flag
// auto_ticket_policy_enabled, off by default) only does the two unambiguous things: adopt the conversation's unowned active
// ticket, or open one when the conversation has none. Everything else is advice for a person.
type TopicTicketService struct {
	topics  ports.TopicRepository
	policy  ports.TicketPolicyRepository
	routing ports.RoutingRepository
	creator ports.LocalTicketCreator
	topicSv *TopicService
	flags   Flags
}

func NewTopicTicketService(topics ports.TopicRepository, policy ports.TicketPolicyRepository, routing ports.RoutingRepository, creator ports.LocalTicketCreator, topicSv *TopicService, flags Flags) *TopicTicketService {
	return &TopicTicketService{topics: topics, policy: policy, routing: routing, creator: creator, topicSv: topicSv, flags: flags}
}

func (s *TopicTicketService) tenant(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("intelligence: tenant context required")
	}
	return tc, nil
}

type policyState struct {
	advice domain.TicketAdvice
	conv   uuid.UUID // the single conversation, or Nil
	topic  *domain.TopicThread
}

func (s *TopicTicketService) evaluate(ctx context.Context, tenantID, topicID uuid.UUID) (*policyState, error) {
	topic, err := s.topics.GetTopic(ctx, tenantID, topicID)
	if err != nil {
		return nil, err
	}
	f, err := s.policy.TopicPolicyFacts(ctx, tenantID, topicID)
	if err != nil {
		return nil, err
	}
	in := domain.TicketPolicyInput{TopicID: topicID, TopicOpen: f.Open, HasPrimaryTicket: f.HasPrimary, Conversations: len(f.Conversations),
		InGroupOnly: len(f.Conversations) == 0 && f.GroupLinks > 0, OtherOpenTopics: f.OtherOpenTopics, MessageCount: f.MessageCount}
	st := &policyState{topic: topic}
	if len(f.Conversations) == 1 {
		st.conv = f.Conversations[0]
		if in.ActiveTicket, err = s.policy.ActiveTicket(ctx, tenantID, st.conv); err != nil {
			return nil, err
		}
	}
	st.advice = domain.DecideTicket(in)
	return st, nil
}

// Advise is read-only: what the policy would do for the topic right now.
func (s *TopicTicketService) Advise(ctx context.Context, topicID uuid.UUID) (domain.TicketAdvice, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return domain.TicketAdvice{}, err
	}
	st, err := s.evaluate(ctx, tc.TenantID, topicID)
	if err != nil {
		return domain.TicketAdvice{}, err
	}
	return st.advice, nil
}

// AppliedTicket is what Apply did.
type AppliedTicket struct {
	Action   domain.TicketAction
	TicketID uuid.UUID
	Relation domain.TicketRelation
	Created  bool
}

// Apply performs an action chosen by a person (or by the automation). The server re-evaluates the policy under a
// per-conversation lock and refuses (ErrInvalidTransition) anything the policy does not currently allow: the client's
// view of the state is never trusted.
func (s *TopicTicketService) Apply(ctx context.Context, topicID uuid.UUID, action domain.TicketAction, origin domain.TicketLinkOrigin) (*AppliedTicket, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	pre, err := s.evaluate(ctx, tc.TenantID, topicID)
	if err != nil {
		return nil, err
	}
	if pre.conv == uuid.Nil {
		return nil, fmt.Errorf("%w: %s", domain.ErrInvalidTransition, pre.advice.Reason)
	}
	if err := s.policy.LockConversation(ctx, tc.TenantID, pre.conv); err != nil {
		return nil, err
	}
	st, err := s.evaluate(ctx, tc.TenantID, topicID) // again, now that no other policy action can interleave
	if err != nil {
		return nil, err
	}
	if !st.advice.CanApply(action) {
		return nil, fmt.Errorf("%w: %s", domain.ErrInvalidTransition, st.advice.Reason)
	}
	var by *uuid.UUID
	if tc.ActorID != uuid.Nil {
		a := tc.ActorID
		by = &a
	}
	link := func(ticketID uuid.UUID, rel domain.TicketRelation) error {
		l, err := domain.NewTopicTicketLink(tc.TenantID, topicID, ticketID, rel, origin, by)
		if err != nil {
			return err
		}
		return s.topics.LinkTicket(ctx, l)
	}
	switch action {
	case domain.TicketActionAdoptActive:
		if err := link(*st.advice.TicketID, domain.TicketPrimary); err != nil {
			return nil, err
		}
		return &AppliedTicket{Action: action, TicketID: *st.advice.TicketID, Relation: domain.TicketPrimary}, nil
	case domain.TicketActionShareActive:
		if st.advice.TicketID == nil {
			return nil, fmt.Errorf("%w: the conversation has no active ticket to share", domain.ErrInvalidTransition)
		}
		if err := link(*st.advice.TicketID, domain.TicketRelated); err != nil {
			return nil, err
		}
		return &AppliedTicket{Action: action, TicketID: *st.advice.TicketID, Relation: domain.TicketRelated}, nil
	case domain.TicketActionCreate:
		if s.creator == nil {
			return nil, fmt.Errorf("%w: ticket creation is not available", domain.ErrInvalidTransition)
		}
		t, err := ticketdomain.NewTicket(tc.TenantID, st.conv, st.topic.Title)
		if err != nil {
			return nil, err
		}
		if err := s.creator.Store(ctx, t); err != nil {
			return nil, fmt.Errorf("%w: could not open the ticket: %v", domain.ErrInvalidTransition, err)
		}
		if err := link(t.ID, domain.TicketPrimary); err != nil {
			return nil, err
		}
		return &AppliedTicket{Action: action, TicketID: t.ID, Relation: domain.TicketPrimary, Created: true}, nil
	}
	return nil, fmt.Errorf("%w: unknown action", domain.ErrInvalidTopic)
}

// BackfillLegacy gives a conversation's pre-existing active ticket a topic on demand (source legacy_backfill). It never
// classifies history and never touches a ticket that already has a link; calling it again changes nothing.
func (s *TopicTicketService) BackfillLegacy(ctx context.Context, conversationID uuid.UUID, contactID *uuid.UUID) (*domain.TopicThread, bool, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := s.policy.LockConversation(ctx, tc.TenantID, conversationID); err != nil {
		return nil, false, err
	}
	legacy, err := s.policy.LegacyTicket(ctx, tc.TenantID, conversationID)
	if err != nil || legacy == nil {
		return nil, false, err
	}
	title := legacy.Subject
	if title == "" {
		title = "Atendimento"
	}
	topic, err := s.topicSv.CreateTopic(ctx, CreateTopicInput{ConversationID: conversationID, ContactID: contactID, Title: title, Source: domain.SourceLegacyBackfill})
	if err != nil {
		return nil, false, err
	}
	l, err := domain.NewTopicTicketLink(tc.TenantID, topic.ID, legacy.ID, domain.TicketPrimary, domain.TicketLinkLegacyBackfill, nil)
	if err != nil {
		return nil, false, err
	}
	if err := s.topics.LinkTicket(ctx, l); err != nil {
		return nil, false, err
	}
	return topic, true, nil
}

// AutoForMessage is the optional pipeline step: for each topic the message belongs to, do the unambiguous policy action.
// A problem is logged and swallowed; it never fails or delays message processing.
func (s *TopicTicketService) AutoForMessage(ctx context.Context, ref ports.MessageRef) {
	if s == nil || !s.flags.AutoTicketPolicyEnabled {
		return
	}
	tc, err := s.tenant(ctx)
	if err != nil {
		return
	}
	topics, err := s.routing.TopicsOfMessage(ctx, tc.TenantID, ref)
	if err != nil {
		log.Printf("intelligence: ticket policy step could not list topics: %v", err)
		return
	}
	for _, topicID := range topics {
		adv, err := s.Advise(ctx, topicID)
		if err != nil || (adv.Action != domain.TicketActionAdoptActive && adv.Action != domain.TicketActionCreate) {
			continue
		}
		if _, err := s.Apply(ctx, topicID, adv.Action, domain.TicketLinkRule); err != nil && !errors.Is(err, domain.ErrInvalidTransition) {
			log.Printf("intelligence: ticket policy skipped (%v)", err)
		}
	}
}

// TicketPolicyPipeline runs the ticket policy after the rest of the pipeline.
type TicketPolicyPipeline struct {
	Next    Pipeline
	Tickets *TopicTicketService
}

func (p TicketPolicyPipeline) Process(ctx context.Context, job ports.Job) error {
	if err := p.Next.Process(ctx, job); err != nil {
		return err
	}
	p.Tickets.AutoForMessage(ctx, job.Ref)
	return nil
}
