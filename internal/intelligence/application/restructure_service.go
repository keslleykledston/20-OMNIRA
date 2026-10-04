package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// RestructureService merges and splits topics on a person's decision. Neither deletes history: a merged topic is archived
// and points at its target; a split moves only the chosen messages into a new topic that remembers its origin.
type RestructureService struct {
	topics  ports.TopicRepository
	repo    ports.RestructureRepository
	routing ports.RoutingRepository
}

func NewRestructureService(topics ports.TopicRepository, repo ports.RestructureRepository, routing ports.RoutingRepository) *RestructureService {
	return &RestructureService{topics: topics, repo: repo, routing: routing}
}

func (s *RestructureService) tenant(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("intelligence: tenant context required")
	}
	return tc, nil
}

func actor(tc *tenancydomain.TenantContext) *uuid.UUID {
	if tc.ActorID == uuid.Nil {
		return nil
	}
	a := tc.ActorID
	return &a
}

// Merge folds source into target. The caller has already checked that the person may operate BOTH topics.
func (s *RestructureService) Merge(ctx context.Context, sourceID, targetID uuid.UUID) (ports.RestructureResult, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return ports.RestructureResult{}, err
	}
	for _, id := range []uuid.UUID{sourceID, targetID} {
		if _, err := s.topics.GetTopic(ctx, tc.TenantID, id); err != nil {
			return ports.RestructureResult{}, err
		}
	}
	return s.repo.Merge(ctx, tc.TenantID, sourceID, targetID, actor(tc))
}

// Split moves the chosen messages of a topic into a new one.
func (s *RestructureService) Split(ctx context.Context, sourceID uuid.UUID, title string, messages []ports.MessageRef) (ports.RestructureResult, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return ports.RestructureResult{}, err
	}
	src, err := s.topics.GetTopic(ctx, tc.TenantID, sourceID)
	if err != nil {
		return ports.RestructureResult{}, err
	}
	nt, err := domain.NewTopicThread(tc.TenantID, title, domain.SourceManual, time.Now())
	if err != nil {
		return ports.RestructureResult{}, err
	}
	nt.PrimaryContactID, nt.OriginConversationID, nt.PrivacyPolicy = src.PrimaryContactID, src.OriginConversationID, src.PrivacyPolicy
	nt.CreatedByUserID = actor(tc)
	res, err := s.repo.Split(ctx, tc.TenantID, sourceID, nt, messages, actor(tc))
	if err != nil {
		return res, err
	}
	// the new topic gets the entities its own messages name (the same deterministic extractor the router uses)
	texts, err := s.repo.MessageTexts(ctx, tc.TenantID, messages)
	if err != nil {
		return res, err
	}
	for _, m := range messages {
		if ents := domain.ExtractEntities(texts[m.ID]); len(ents) > 0 {
			id := m.ID
			if m.Kind == ports.KindGroup {
				if err := s.routing.UpsertEntities(ctx, tc.TenantID, nt.ID, ents, nil, "agent"); err != nil {
					return res, fmt.Errorf("split entities: %w", err)
				}
				continue
			}
			if err := s.routing.UpsertEntities(ctx, tc.TenantID, nt.ID, ents, &id, "agent"); err != nil {
				return res, fmt.Errorf("split entities: %w", err)
			}
		}
	}
	return res, nil
}
