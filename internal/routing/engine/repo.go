package engine

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	"github.com/omnira/omnira/internal/routing/ports"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
)

// Repository is the engine behind the port the routing service already uses: the legacy repository keeps the manual claim, the engine
// replaces only the automatic assignment. Enabled by OMNIRA_ROUTING_ENGINE_ENABLED (default off).
type Repository struct {
	*routingadapters.PostgresAssignmentRepository
	engine *Engine
}

var _ ports.AssignmentRepository = (*Repository)(nil)

func NewRepository(pool *pgxpool.Pool, presence ports.PresenceChecker) *Repository {
	var p sectorsadapters.PresenceChecker
	if presence != nil {
		p = presence
	}
	return &Repository{PostgresAssignmentRepository: routingadapters.NewPostgresAssignmentRepository(pool, presence), engine: New(pool, p)}
}

// AssignRoundRobin keeps the port's contract: (user, true) when somebody was assigned (or already owned it), (Nil, false) when not.
func (r *Repository) AssignRoundRobin(ctx context.Context, conversationID uuid.UUID, reason string) (uuid.UUID, bool, error) {
	d, err := r.engine.Decide(ctx, Request{ConversationID: conversationID, Trigger: TriggerRoute, StrategyReason: reason})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	if d.Kind == AssignAgent {
		return d.AssignedUser, true, nil
	}
	return uuid.Nil, false, nil
}

// Engine exposes the engine behind the repository (the worker also runs its timers).
func (r *Repository) Engine() *Engine { return r.engine }
