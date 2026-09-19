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

type Service struct {
	repo   ports.AssignmentRepository
	claims metric.Int64Counter
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
