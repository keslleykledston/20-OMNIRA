package adapters

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	flowsports "github.com/omnira/omnira/internal/flows/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// FlowEffects is the Flow Builder's way into the module (ADR-0049 phase 6): it implements the flow engine's ServiceFlow capability on the service, inside the
// step's own transaction. Every call runs under a SAVEPOINT: a refusal of the module (an unknown code, a closed conversation, a constraint) rolls back only
// that call, so the node can take its `error` port and the run goes on in a usable transaction.
type FlowEffects struct {
	pool *pgxpool.Pool
	svc  *application.Service
}

var _ flowsports.ServiceFlow = (*FlowEffects)(nil)

func NewFlowEffects(pool *pgxpool.Pool, svc *application.Service) *FlowEffects {
	return &FlowEffects{pool: pool, svc: svc}
}

func (f *FlowEffects) guard(ctx context.Context, fn func(context.Context) error) error {
	return platformdb.WithSavepoint(ctx, f.pool, fn)
}

func (f *FlowEffects) ServiceStart(ctx context.Context, conversationID, runID uuid.UUID, seq int) (protocol string, created bool, err error) {
	err = f.guard(ctx, func(ctx context.Context) error {
		s, e := f.svc.FlowStart(ctx, conversationID, runID, seq)
		protocol, created = s.Protocol, s.Created
		return e
	})
	return
}

func (f *FlowEffects) ServiceClassify(ctx context.Context, conversationID, runID uuid.UUID, seq int, categoryCode string) error {
	return f.guard(ctx, func(ctx context.Context) error {
		return f.svc.FlowClassify(ctx, conversationID, runID, seq, categoryCode)
	})
}

func (f *FlowEffects) ServiceTask(ctx context.Context, conversationID, runID uuid.UUID, seq int, title, description, priority string) error {
	return f.guard(ctx, func(ctx context.Context) error {
		_, e := f.svc.FlowTask(ctx, conversationID, runID, seq, title, description, domain.Priority(priority))
		return e
	})
}

func (f *FlowEffects) ServiceOpenCase(ctx context.Context, conversationID, runID uuid.UUID, seq int, title, description, priority string) (number string, err error) {
	err = f.guard(ctx, func(ctx context.Context) error {
		n, e := f.svc.FlowOpenCase(ctx, conversationID, runID, seq, title, description, domain.Priority(priority))
		number = n
		return e
	})
	return
}

func (f *FlowEffects) ServiceClose(ctx context.Context, conversationID, runID uuid.UUID, seq int, resultCode, note string) (closed bool, err error) {
	err = f.guard(ctx, func(ctx context.Context) error {
		c, e := f.svc.FlowClose(ctx, conversationID, runID, seq, resultCode, note)
		closed = c
		return e
	})
	return
}
