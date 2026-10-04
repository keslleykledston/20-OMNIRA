package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

var ErrNotRoutable = errors.New("intelligence: only inbound messages are routed")

// RoutingMetrics is the minimal counter surface; the worker adapts it to the real metrics (nil is fine).
type RoutingMetrics interface {
	Decision(status string, applied bool, source string)
	Latency(d time.Duration)
}

type noRoutingMetrics struct{}

func (noRoutingMetrics) Decision(string, bool, string) {}
func (noRoutingMetrics) Latency(time.Duration)         {}

// RouteOptions carries hard evidence supplied by the caller (Wave 8's handoff token, an explicit selection).
type RouteOptions struct {
	HandoffTopic  *uuid.UUID
	ExplicitTopic *uuid.UUID
}

// RoutingOutcome reports what Route did. Replay means an applied decision already existed and nothing changed.
type RoutingOutcome struct {
	DecisionID   uuid.UUID
	Result       domain.RoutingResult
	Applied      bool
	Replay       bool
	AmbiguityID  *uuid.UUID
	CreatedTopic *uuid.UUID
}

// RoutingService places inbound messages into topics with the deterministic router. With topic_auto_routing_enabled off
// it only RECORDS what it would have done (applied=false), which is how the rule set is evaluated before it acts.
type RoutingService struct {
	routing ports.RoutingRepository
	topics  ports.TopicRepository
	flags   Flags
	cfg     domain.RoutingConfig
	metrics RoutingMetrics
	now     func() time.Time
}

func NewRoutingService(routing ports.RoutingRepository, topics ports.TopicRepository, flags Flags, cfg domain.RoutingConfig, m RoutingMetrics) *RoutingService {
	if m == nil {
		m = noRoutingMetrics{}
	}
	return &RoutingService{routing: routing, topics: topics, flags: flags, cfg: cfg, metrics: m, now: time.Now}
}

const focusTTL = 2 * time.Hour

// Route is idempotent: the same message routed twice (an at-least-once event, a retry, two workers) changes nothing the
// second time.
func (s *RoutingService) Route(ctx context.Context, ref ports.MessageRef, opts RouteOptions) (*RoutingOutcome, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("intelligence: tenant context required")
	}
	started := s.now()
	msg, err := s.routing.LoadRoutable(ctx, tc.TenantID, ref)
	if err != nil {
		return nil, err
	}
	if !msg.Inbound {
		return nil, ErrNotRoutable
	}
	if existing, err := s.routing.ActiveDecision(ctx, tc.TenantID, ref); err != nil {
		return nil, err
	} else if existing != nil {
		return &RoutingOutcome{DecisionID: existing.ID, Applied: true, Replay: true, Result: domain.RoutingResult{Status: existing.Status, Primary: existing.Selected}}, nil
	}

	// A message that already belongs to a topic (placed by a person, a handoff, an earlier decision that was later
	// overridden) is not decided again: automation never re-opens a settled placement.
	if placed, err := s.routing.TopicsOfMessage(ctx, tc.TenantID, ref); err != nil {
		return nil, err
	} else if len(placed) > 0 {
		return &RoutingOutcome{Replay: true, Result: domain.RoutingResult{Status: domain.RoutingAssigned, Primary: &placed[0]}}, nil
	}

	in, err := s.buildInput(ctx, tc.TenantID, msg, opts)
	if err != nil {
		return nil, err
	}
	res := domain.Route(s.cfg, in)

	apply := s.flags.TopicAutoRoutingEnabled
	conf := res.Confidence
	signals, _ := json.Marshal(map[string]any{"signals": res.Signals, "entities": entityStrings(res.Entities), "candidates": candidateSummary(res.Candidates)})
	rec := ports.DecisionRecord{Ref: ref, Status: res.Status, Selected: res.Primary, Source: res.Source, Applied: apply, Confidence: &conf, Signals: signals}
	decisionID, inserted, err := s.routing.SaveDecision(ctx, tc.TenantID, rec)
	if err != nil {
		return nil, err
	}
	out := &RoutingOutcome{DecisionID: decisionID, Result: res, Applied: apply}
	s.metrics.Decision(string(res.Status), apply, string(res.Source))
	s.metrics.Latency(s.now().Sub(started))
	if !apply {
		return out, nil
	}
	if !inserted { // a concurrent worker applied it first
		out.Replay = true
		return out, nil
	}
	if err := s.apply(ctx, tc.TenantID, msg, in, res, decisionID, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *RoutingService) buildInput(ctx context.Context, tenantID uuid.UUID, msg *ports.RoutableMessage, opts RouteOptions) (domain.RoutingInput, error) {
	in := domain.RoutingInput{Now: s.now().UTC(), Text: msg.Text, HandoffTopic: opts.HandoffTopic, ExplicitTopic: opts.ExplicitTopic}
	var err error
	if msg.ReplyTo != nil {
		if in.ReplyTopics, err = s.routing.TopicsOfMessage(ctx, tenantID, *msg.ReplyTo); err != nil {
			return in, err
		}
	}
	if in.EntityTopics, err = s.routing.EntityTopics(ctx, tenantID, msg.Ref, msg.ContainerID, msg.ContactID, domain.ExtractEntities(msg.Text)); err != nil {
		return in, err
	}
	if msg.ParticipantID != nil {
		if in.ParticipantTopics, err = s.routing.ParticipantTopics(ctx, tenantID, msg.Ref, msg.ContainerID, *msg.ParticipantID, 5); err != nil {
			return in, err
		}
	}
	if in.Focus, err = s.routing.Focus(ctx, tenantID, msg.Ref, msg.ContainerID, msg.ParticipantID); err != nil {
		return in, err
	}
	in.OpenTopics, err = s.routing.OpenTopics(ctx, tenantID, msg.Ref, msg.ContainerID, 20)
	return in, err
}

func (s *RoutingService) apply(ctx context.Context, tenantID uuid.UUID, msg *ports.RoutableMessage, in domain.RoutingInput, res domain.RoutingResult, decisionID uuid.UUID, out *RoutingOutcome) error {
	conf := res.Confidence
	link := func(topic uuid.UUID, rel domain.MessageRelation) error {
		if err := s.routing.LinkMessage(ctx, tenantID, msg.Ref, topic, rel, res.Source, &conf, &decisionID); err != nil {
			return err
		}
		if err := s.routing.LinkContainer(ctx, tenantID, msg.Ref, msg.ContainerID, topic); err != nil {
			return err
		}
		return s.routing.TouchTopic(ctx, tenantID, topic)
	}
	focus := func(topic uuid.UUID) error {
		if res.Confidence < s.cfg.AutoAssign {
			return nil
		}
		return s.routing.SetFocus(ctx, tenantID, msg.Ref, msg.ContainerID, msg.ParticipantID, topic, "router", res.Confidence, s.now().Add(focusTTL))
	}
	switch res.Status {
	case domain.RoutingAssigned:
		if err := link(*res.Primary, domain.RelationPrimary); err != nil {
			return err
		}
		if err := s.routing.UpsertEntities(ctx, tenantID, *res.Primary, res.Entities, entityMessage(msg), "rule"); err != nil {
			return err
		}
		return focus(*res.Primary)
	case domain.RoutingMultiTopic:
		if err := link(*res.Primary, domain.RelationPrimary); err != nil {
			return err
		}
		for _, t := range res.Additional {
			if err := link(t, domain.RelationSecondary); err != nil {
				return err
			}
		}
		for _, e := range res.Entities { // each named subject is recorded on the topic that already holds it
			for _, t := range in.EntityTopics[e.String()] {
				if err := s.routing.UpsertEntities(ctx, tenantID, t, []domain.Entity{e}, entityMessage(msg), "rule"); err != nil {
					return err
				}
			}
		}
		return nil
	case domain.RoutingNewTopic:
		topic, err := domain.NewTopicThread(tenantID, res.NewTopic.Title, domain.SourceRule, s.now().UTC())
		if err != nil {
			return err
		}
		topic.PrimaryContactID = msg.ContactID
		if msg.Ref.Kind == ports.KindConversation {
			c := msg.ContainerID
			topic.OriginConversationID = &c
		}
		if err := s.topics.CreateTopic(ctx, topic); err != nil {
			return err
		}
		res.Source = domain.DecisionEntity
		if err := link(topic.ID, domain.RelationPrimary); err != nil {
			return err
		}
		if err := s.routing.UpsertEntities(ctx, tenantID, topic.ID, res.NewTopic.Entities, entityMessage(msg), "rule"); err != nil {
			return err
		}
		out.CreatedTopic = &topic.ID
		if msg.ParticipantID != nil {
			return s.routing.SetFocus(ctx, tenantID, msg.Ref, msg.ContainerID, msg.ParticipantID, topic.ID, "router", s.cfg.AutoAssign, s.now().Add(focusTTL))
		}
		return nil
	case domain.RoutingAmbiguous:
		titles := map[uuid.UUID]string{}
		for _, b := range in.OpenTopics {
			titles[b.ID] = b.Title
		}
		payload, _ := json.Marshal(candidateWithTitles(res.Candidates, titles))
		id, err := s.routing.CreateAmbiguity(ctx, tenantID, msg.Ref, payload)
		if err != nil {
			return err
		}
		out.AmbiguityID = &id
	}
	return nil
}

func entityMessage(msg *ports.RoutableMessage) *uuid.UUID {
	if msg.Ref.Kind == ports.KindConversation { // topic_entities.source_message_id references conversation messages
		id := msg.Ref.ID
		return &id
	}
	return nil
}

func entityStrings(es []domain.Entity) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.String())
	}
	return out
}

type candidateJSON struct {
	TopicID uuid.UUID       `json:"topic_id"`
	Title   string          `json:"title,omitempty"`
	Score   float64         `json:"score"`
	Signals []domain.Signal `json:"signals,omitempty"`
}

func candidateSummary(cs []domain.TopicCandidate) []candidateJSON {
	return candidateWithTitles(cs, nil)
}

func candidateWithTitles(cs []domain.TopicCandidate, titles map[uuid.UUID]string) []candidateJSON {
	out := make([]candidateJSON, 0, len(cs))
	for _, c := range cs {
		out = append(out, candidateJSON{TopicID: c.TopicID, Title: titles[c.TopicID], Score: c.Score, Signals: c.Signals})
	}
	return out
}

// ResolveAmbiguity lets a person (or, with source "customer", the customer's own answer) decide an open ambiguity. The
// message joins the chosen topic as a human decision, which no later automated decision can overwrite.
func (s *RoutingService) ResolveAmbiguity(ctx context.Context, ambiguityID uuid.UUID, topicID *uuid.UUID, newTitle string, source domain.DecisionSource) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("intelligence: tenant context required")
	}
	if source != domain.DecisionAgent && source != domain.DecisionCustomer {
		return uuid.Nil, fmt.Errorf("%w: only an agent or the customer resolves an ambiguity", domain.ErrInvalidTopic)
	}
	a, err := s.routing.GetAmbiguity(ctx, tc.TenantID, ambiguityID)
	if err != nil {
		return uuid.Nil, err
	}
	if a.Status != "open" {
		return uuid.Nil, fmt.Errorf("%w: ambiguity already %s", domain.ErrInvalidTransition, a.Status)
	}
	var chosen uuid.UUID
	switch {
	case topicID != nil && newTitle == "":
		t, err := s.topics.GetTopic(ctx, tc.TenantID, *topicID)
		if err != nil {
			return uuid.Nil, err
		}
		chosen = t.ID
	case topicID == nil && newTitle != "":
		topic, err := domain.NewTopicThread(tc.TenantID, newTitle, domain.SourceManual, s.now().UTC())
		if err != nil {
			return uuid.Nil, err
		}
		if a.Ref.Kind == ports.KindConversation {
			c := a.Container
			topic.OriginConversationID = &c
			if msg, err := s.routing.LoadRoutable(ctx, tc.TenantID, a.Ref); err == nil {
				topic.PrimaryContactID = msg.ContactID
			}
		}
		if tc.ActorID != uuid.Nil {
			u := tc.ActorID
			topic.CreatedByUserID = &u
		}
		if err := s.topics.CreateTopic(ctx, topic); err != nil {
			return uuid.Nil, err
		}
		chosen = topic.ID
	default:
		return uuid.Nil, fmt.Errorf("%w: choose an existing topic or give a title for a new one", domain.ErrInvalidTopic)
	}
	conf := 1.0
	if err := s.routing.LinkMessage(ctx, tc.TenantID, a.Ref, chosen, domain.RelationPrimary, source, &conf, nil); err != nil {
		return uuid.Nil, err
	}
	if err := s.routing.LinkContainer(ctx, tc.TenantID, a.Ref, a.Container, chosen); err != nil {
		return uuid.Nil, err
	}
	var by *uuid.UUID
	if tc.ActorID != uuid.Nil {
		u := tc.ActorID
		by = &u
	}
	resolution := string(source)
	if ok, err := s.routing.ResolveAmbiguity(ctx, tc.TenantID, ambiguityID, chosen, resolution, by); err != nil {
		return uuid.Nil, err
	} else if !ok {
		return uuid.Nil, fmt.Errorf("%w: ambiguity already resolved", domain.ErrInvalidTransition)
	}
	_ = s.routing.MarkDecisionOverridden(ctx, tc.TenantID, a.Ref, by)
	_ = s.routing.TouchTopic(ctx, tc.TenantID, chosen)
	return chosen, nil
}
