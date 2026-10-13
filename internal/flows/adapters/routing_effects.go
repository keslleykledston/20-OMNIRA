package adapters

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnira/omnira/internal/flows/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/engine"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// RoutingEffects gives the ADR-0044 routing nodes their data: it ASKS the shared services (the sector calendar, the eligibility rule,
// the routing engine) and maps the answer; it decides nothing itself. It wraps the ordinary effects, so everything else is unchanged.
type RoutingEffects struct {
	*PostgresEffects
	engine   *engine.Engine // nil when OMNIRA_ROUTING_ENGINE_ENABLED is off
	presence sectorsadapters.PresenceChecker
}

var _ ports.RoutingEffects = (*RoutingEffects)(nil)

// WithRouting enables the routing nodes. engine may be nil: schedules, availability and sectors still work; only apply_overflow needs it.
func (e *PostgresEffects) WithRouting(eng *engine.Engine, presence sectorsadapters.PresenceChecker) *RoutingEffects {
	return &RoutingEffects{PostgresEffects: e, engine: eng, presence: presence}
}

func (r *RoutingEffects) tenant(ctx context.Context) (uuid.UUID, platformdb.Querier, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, nil, errors.New("flows: tenant context required")
	}
	return tc.TenantID, r.q(ctx), nil
}

// placeQueues returns the queues a node's "place" stands for, and whether the place exists and is usable: a queue is itself; a sector is
// its queues (an inactive sector, or one with no queue, cannot attend).
func (r *RoutingEffects) placeQueues(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, kind string, id uuid.UUID) ([]uuid.UUID, bool, error) {
	if kind == "queue" {
		var one uuid.UUID
		// a queue of an INACTIVE sector is unavailable too (a deactivated sector takes the flow to its "unavailable" port, however it is reached)
		err := q.QueryRow(ctx, `SELECT qq.id FROM queues qq LEFT JOIN sectors sc ON sc.tenant_id=qq.tenant_id AND sc.id=qq.sector_id
			WHERE qq.tenant_id=$1 AND qq.id=$2 AND (qq.sector_id IS NULL OR sc.active)`, tenant, id).Scan(&one)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, false, nil
			}
			return nil, false, err
		}
		return []uuid.UUID{one}, true, nil
	}
	rows, err := q.Query(ctx, `SELECT qq.id FROM queues qq JOIN sectors sc ON sc.tenant_id=qq.tenant_id AND sc.id=qq.sector_id
		WHERE qq.tenant_id=$1 AND qq.sector_id=$2 AND sc.active ORDER BY qq.name, qq.id LIMIT 500`, tenant, id)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var qid uuid.UUID
		if err := rows.Scan(&qid); err != nil {
			return nil, false, err
		}
		out = append(out, qid)
	}
	return out, len(out) > 0, rows.Err()
}

func (r *RoutingEffects) Schedule(ctx context.Context, kind string, id uuid.UUID) (string, error) {
	tenant, q, err := r.tenant(ctx)
	if err != nil {
		return "", err
	}
	queues, ok, err := r.placeQueues(ctx, q, tenant, kind, id)
	if err != nil || !ok {
		return "unavailable", err
	}
	var now time.Time
	if err := q.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return "", err
	}
	for _, qid := range queues { // a sector is open when any of its queues is: the same inheritance the engine uses
		a, err := sectorsadapters.QueueAvailability(ctx, q, tenant, qid, now)
		if err != nil {
			return "", err
		}
		if a.Open {
			return "open", nil
		}
	}
	return "closed", nil
}

func (r *RoutingEffects) Availability(ctx context.Context, kind string, id uuid.UUID) (string, error) {
	tenant, q, err := r.tenant(ctx)
	if err != nil {
		return "", err
	}
	queues, ok, err := r.placeQueues(ctx, q, tenant, kind, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "missing", nil
	}
	var now time.Time
	if err := q.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return "", err
	}
	anyOpen := false
	for _, qid := range queues {
		snap, err := sectorsadapters.Snapshot(ctx, q, tenant, qid, now, r.presence)
		if err != nil {
			return "", err
		}
		anyOpen = anyOpen || snap.Open
		if snap.Open && snap.EligibleCnt > 0 {
			return "available", nil
		}
	}
	if !anyOpen {
		return "closed", nil
	}
	return "unavailable", nil
}

func (r *RoutingEffects) ActiveSectors(ctx context.Context) ([]ports.SectorOption, error) {
	tenant, q, err := r.tenant(ctx)
	if err != nil {
		return nil, err
	}
	// only sectors that can receive a conversation (at least one queue) are offered to the contact
	rows, err := q.Query(ctx, `SELECT s.id, s.name FROM sectors s WHERE s.tenant_id=$1 AND s.active
		AND EXISTS (SELECT 1 FROM queues qq WHERE qq.tenant_id=s.tenant_id AND qq.sector_id=s.id) ORDER BY s.name, s.id LIMIT 12`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.SectorOption
	for rows.Next() {
		var o ports.SectorOption
		if err := rows.Scan(&o.ID, &o.Name); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *RoutingEffects) AssignSector(ctx context.Context, conversationID, sectorID uuid.UUID) (string, error) {
	tenant, q, err := r.tenant(ctx)
	if err != nil {
		return "", err
	}
	_, ok, err := r.placeQueues(ctx, q, tenant, "sector", sectorID)
	if err != nil || !ok {
		return "unavailable", err
	}
	// the overflow chain is authoritative while it runs: a flow may not move a conversation the chain has already moved (that would start a
	// fresh episode and empty its loop guard). The author's "unavailable" port handles it.
	var hops int
	var state string
	switch err := q.QueryRow(ctx, `SELECT hops, state FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2`, tenant, conversationID).Scan(&hops, &state); {
	case err == nil && hops > 0 && state != "assigned" && state != "done":
		return "unavailable", nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	var now time.Time
	if err := q.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return "", err
	}
	queue := uuid.Nil
	if r.engine != nil {
		var reason string
		if queue, reason, err = r.engine.SectorQueue(ctx, sectorID, now); err != nil {
			return "", err
		}
		if reason != "" || queue == uuid.Nil {
			return "unavailable", nil
		}
	} else { // no engine: the first queue of the sector by name (the legacy router distributes inside it)
		qs, _, err := r.placeQueues(ctx, q, tenant, "sector", sectorID)
		if err != nil {
			return "", err
		}
		queue = qs[0]
	}
	if err := r.PostgresEffects.AssignQueue(ctx, conversationID, &queue); err != nil {
		return "", err
	}
	return "assigned", nil
}

func (r *RoutingEffects) ApplyOverflow(ctx context.Context, conversationID uuid.UUID) (string, *uuid.UUID, error) {
	if r.engine == nil {
		return "", nil, errors.New("the routing engine is not enabled in this environment")
	}
	d, err := r.engine.Decide(ctx, engine.Request{ConversationID: conversationID, Trigger: engine.TriggerRoute, StrategyReason: "flow"})
	if err != nil {
		return "", nil, err
	}
	switch d.Kind {
	case engine.AssignAgent:
		owner := d.AssignedUser
		return "assigned", &owner, nil
	case engine.WaitUntilOpen:
		return "waiting_open", nil, nil
	case engine.RequireManualAction, engine.Escalate:
		return "manual", nil, nil
	}
	return "queued", nil, nil
}
