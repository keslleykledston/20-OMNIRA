package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var ErrAlreadyAssigned = errors.New("routing: conversation already assigned")
var ErrNoEligibleAgent = errors.New("routing: no eligible agent")

type Service struct {
	repo   ports.AssignmentRepository
	claims metric.Int64Counter
}

// AssignRoundRobin runs only for trusted system work reconstructed from a
// persisted conversation reference. Tenant ownership comes from context.
func (s *Service) AssignRoundRobin(ctx context.Context, conversationID uuid.UUID) (uuid.UUID, error) {
	if s == nil || s.repo == nil || conversationID == uuid.Nil {
		return uuid.Nil, errors.New("routing: valid assignment is required")
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.Source != tenancydomain.AccessSourceSystem || tc.ActorID != uuid.Nil {
		return uuid.Nil, errors.New("routing: system tenant context required")
	}
	userID, assigned, err := s.repo.AssignRoundRobin(ctx, conversationID, "round_robin")
	status := "success"
	switch {
	case errors.Is(err, ports.ErrPresenceUnavailable):
		// Distinct from "no eligible agent": the presence backend itself
		// could not be consulted (IAM4.2-B1). Never folded into "unavailable".
		status = "presence_unavailable"
	case err != nil:
		status = "error"
	case !assigned:
		status = "unavailable"
	}
	if s.claims != nil {
		s.claims.Add(ctx, 1, metric.WithAttributes(attribute.String("operation", "round_robin"), attribute.String("status", status)))
	}
	if err != nil {
		return uuid.Nil, err
	}
	if !assigned {
		return uuid.Nil, ErrNoEligibleAgent
	}
	return userID, nil
}

func NewService(repo ports.AssignmentRepository) *Service {
	counter, _ := otel.Meter("omnira/routing").Int64Counter("routing_assignment_total")
	return &Service{repo: repo, claims: counter}
}

// ClaimOwn atomically assigns an unowned conversation to the authenticated
// actor. Neither tenant nor target user is accepted from request payload.
func (s *Service) ClaimOwn(ctx context.Context, conversationID uuid.UUID) error {
	if s == nil || s.repo == nil || conversationID == uuid.Nil {
		return errors.New("routing: valid claim is required")
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.Source == tenancydomain.AccessSourceSystem || tc.ActorID == uuid.Nil {
		return errors.New("routing: human tenant context required")
	}
	claimed, err := s.repo.ClaimUnassigned(ctx, conversationID, tc.ActorID, "manual_claim")
	status := "success"
	if err != nil {
		status = "error"
	} else if !claimed {
		status = "conflict"
	}
	if s.claims != nil {
		s.claims.Add(ctx, 1, metric.WithAttributes(attribute.String("operation", "manual_claim"), attribute.String("status", status)))
	}
	if err != nil {
		return err
	}
	if !claimed {
		return ErrAlreadyAssigned
	}
	return nil
}
