package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ErrHandoffDisabled: the feature flag is off.
var ErrHandoffDisabled = errors.New("intelligence: private handoff is disabled")

// HandoffMetrics counts redemption outcomes (redeemed / rejected). Never the token.
type HandoffMetrics interface{ Handoff(outcome string) }

type noHandoffMetrics struct{}

func (noHandoffMetrics) Handoff(string) {}

// HandoffService creates and redeems private-handoff invitations (ADR-0017 Wave 8). The tenant always comes from the
// session; the token is shown once and only its hash is stored; redemption is a single atomic statement.
type HandoffService struct {
	repo    ports.HandoffRepository
	routing ports.RoutingRepository
	flags   Flags
	metrics HandoffMetrics
	now     func() time.Time
}

func NewHandoffService(repo ports.HandoffRepository, routing ports.RoutingRepository, flags Flags, m HandoffMetrics) *HandoffService {
	if m == nil {
		m = noHandoffMetrics{}
	}
	return &HandoffService{repo: repo, routing: routing, flags: flags, metrics: m, now: time.Now}
}

func (s *HandoffService) tenant(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("intelligence: tenant context required")
	}
	return tc, nil
}

// Create returns the invitation and its token. The token cannot be read again.
func (s *HandoffService) Create(ctx context.Context, topicID uuid.UUID, ttl time.Duration) (*domain.TopicHandoff, string, error) {
	if !s.flags.PrivateHandoffEnabled {
		return nil, "", ErrHandoffDisabled
	}
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, "", err
	}
	group, err := s.repo.SourceGroup(ctx, tc.TenantID, topicID)
	if err != nil {
		return nil, "", err
	}
	token, hash, err := domain.NewHandoffToken()
	if err != nil {
		return nil, "", err
	}
	now := s.now().UTC()
	h := &domain.TopicHandoff{TenantID: tc.TenantID, TopicThreadID: topicID, SourceGroupID: group, ExpiresAt: now.Add(domain.ClampHandoffTTL(ttl))}
	if tc.ActorID != uuid.Nil {
		a := tc.ActorID
		h.CreatedByUserID = &a
	}
	if err := s.repo.Create(ctx, h, hash, now); err != nil {
		return nil, "", err
	}
	return h, token, nil
}

func (s *HandoffService) List(ctx context.Context, topicID uuid.UUID) ([]domain.TopicHandoff, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	return s.repo.List(ctx, tc.TenantID, topicID)
}

func (s *HandoffService) Revoke(ctx context.Context, topicID, handoffID uuid.UUID) error {
	tc, err := s.tenant(ctx)
	if err != nil {
		return err
	}
	ok, err := s.repo.Revoke(ctx, tc.TenantID, topicID, handoffID, s.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrInvalidTransition
	}
	return nil
}

// TryRedeem looks for a handoff token in an inbound PRIVATE message. A valid one binds the conversation and the message
// to the topic with the strongest evidence (decision_source=handoff). A token that is invalid, expired, already used or
// for a closed topic does nothing and tells the sender nothing; the message is then routed like any other. A token in a
// group message is ignored on purpose: the handoff exists to move the subject to a private chat.
func (s *HandoffService) TryRedeem(ctx context.Context, ref ports.MessageRef) (redeemed bool, err error) {
	if s == nil || !s.flags.PrivateHandoffEnabled || ref.Kind != ports.KindConversation {
		return false, nil
	}
	tc, err := s.tenant(ctx)
	if err != nil {
		return false, err
	}
	msg, err := s.routing.LoadRoutable(ctx, tc.TenantID, ref)
	if err != nil {
		return false, err
	}
	if !msg.Inbound {
		return false, nil
	}
	token, ok := domain.FindHandoffToken(msg.Text)
	if !ok {
		return false, nil
	}
	if placed, err := s.routing.TopicsOfMessage(ctx, tc.TenantID, ref); err != nil || len(placed) > 0 {
		return false, err // a replayed job: the message is already placed
	}
	h, err := s.repo.Redeem(ctx, tc.TenantID, domain.HashHandoffToken(token), ref.ID, msg.ContainerID, s.now().UTC())
	if err != nil {
		return false, err
	}
	if h == nil {
		s.metrics.Handoff("rejected")
		return false, nil
	}
	conf := 1.0
	id, _, err := s.routing.SaveDecision(ctx, tc.TenantID, ports.DecisionRecord{Ref: ref, Status: domain.RoutingAssigned, Selected: &h.TopicThreadID,
		Source: domain.DecisionHandoff, Applied: true, Confidence: &conf, Signals: []byte(`{"signals":["handoff"]}`)})
	if err != nil {
		return false, err
	}
	if err := s.routing.LinkMessage(ctx, tc.TenantID, ref, h.TopicThreadID, domain.RelationPrimary, domain.DecisionHandoff, &conf, &id); err != nil {
		return false, err
	}
	if err := s.routing.LinkContainer(ctx, tc.TenantID, ref, msg.ContainerID, h.TopicThreadID); err != nil {
		return false, err
	}
	if err := s.routing.TouchTopic(ctx, tc.TenantID, h.TopicThreadID); err != nil {
		return false, err
	}
	s.metrics.Handoff("redeemed")
	return true, nil
}

// HandoffPipeline redeems a handoff token (if the message carries one) BEFORE routing, so the message is placed by the
// strongest evidence and the router then sees it as already placed.
type HandoffPipeline struct {
	Next     Pipeline
	Handoffs *HandoffService
}

func (p HandoffPipeline) Process(ctx context.Context, job ports.Job) error {
	if _, err := p.Handoffs.TryRedeem(ctx, job.Ref); err != nil {
		return err
	}
	return p.Next.Process(ctx, job)
}
