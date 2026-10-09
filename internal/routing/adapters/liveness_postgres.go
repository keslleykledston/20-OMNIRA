package adapters

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/ports"
)

// PostgresLivenessRepository is IAM4.2-B0. It never assigns anything itself —
// it only reads the durable "still needs routing" fact
// (conversations.queue_id set, assigned_to_user_id NULL, status open, queue
// mode round_robin) and re-enqueues the exact same job.routing.assign.v1
// event the original routing already publishes (internal/inbox/adapters.
// PostgresInboundStore.RouteNew), through the same outbox_events table the
// existing publisher already polls.
type PostgresLivenessRepository struct{ pool *pgxpool.Pool }

var _ ports.LivenessRepository = (*PostgresLivenessRepository)(nil)

func NewPostgresLivenessRepository(pool *pgxpool.Pool) *PostgresLivenessRepository {
	return &PostgresLivenessRepository{pool: pool}
}

// Retrigger runs as a system actor (no human actor context exists for a
// background sweep or a presence event) — same uuid.Nil/is_system_admin
// pattern already used by internal/worker/routing/postgres.go and the
// outbox publisher. FOR UPDATE OF c SKIP LOCKED is what makes this safe
// under concurrent callers (the sweep and a wakeup racing, or two worker
// replicas): each candidate row is claimed by exactly one caller, which
// bumps it forward before any other caller could see it again.
func (r *PostgresLivenessRepository) Retrigger(ctx context.Context, tenantID *uuid.UUID, queueID *uuid.UUID, limit int, backoff time.Duration) (int, error) {
	if limit <= 0 {
		limit = 1
	}
	var count int
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return platformdb.QuerierFromContext(sctx, r.pool).QueryRow(sctx, `
			WITH candidates AS (
			  SELECT c.tenant_id, c.id
			  FROM conversations c
			  JOIN queues q ON q.tenant_id = c.tenant_id AND q.id = c.queue_id
			  JOIN tenants t ON t.id = c.tenant_id AND t.status = 'active'
			  WHERE c.assigned_to_user_id IS NULL
			    AND c.status = 'open'
			    AND c.queue_id IS NOT NULL
			    AND q.mode = 'round_robin'
			    AND (c.routing_retry_at IS NULL OR c.routing_retry_at <= now())
			    AND ($1::uuid IS NULL OR c.tenant_id = $1)
			    AND ($2::uuid IS NULL OR c.queue_id = $2)
			  ORDER BY c.routing_retry_at ASC NULLS FIRST, c.id
			  LIMIT $3
			  FOR UPDATE OF c SKIP LOCKED
			), bumped AS (
			  UPDATE conversations c SET routing_retry_at = now() + make_interval(secs => $4::float8)
			  FROM candidates
			  WHERE c.tenant_id = candidates.tenant_id AND c.id = candidates.id
			  RETURNING c.tenant_id, c.id
			), enqueued AS (
			  INSERT INTO outbox_events(tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
			  SELECT tenant_id, 'job.routing.assign.v1', 'conversation', id::text, uuid_generate_v4(), '{}'::jsonb
			  FROM bumped
			  RETURNING 1
			)
			SELECT count(*) FROM enqueued`,
			tenantID, queueID, limit, backoff.Seconds()).Scan(&count)
	})
	return count, err
}

// ActiveQueuesForAgent mirrors the exact eligibility joins AssignRoundRobin
// already uses (internal/routing/adapters/postgres.go): active Membership,
// active AgentProfile, active+available queue_members, round_robin mode.
// available is queue eligibility, not presence — same distinction preserved
// here as everywhere else in IAM4.
func (r *PostgresLivenessRepository) ActiveQueuesForAgent(ctx context.Context, tenantID, agentProfileID uuid.UUID) ([]uuid.UUID, error) {
	var queueIDs []uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		rows, err := platformdb.QuerierFromContext(sctx, r.pool).Query(sctx, `
			SELECT DISTINCT qm.queue_id
			FROM queue_members qm
			JOIN memberships m ON m.tenant_id = qm.tenant_id AND m.user_id = qm.user_id AND m.status = 'active'
			JOIN agent_profiles ap ON ap.tenant_id = m.tenant_id AND ap.membership_id = m.id AND ap.status = 'active'
			JOIN queues q ON q.tenant_id = qm.tenant_id AND q.id = qm.queue_id AND q.mode = 'round_robin'
			WHERE qm.tenant_id = $1 AND ap.id = $2 AND qm.active AND qm.available`,
			tenantID, agentProfileID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			queueIDs = append(queueIDs, id)
		}
		return rows.Err()
	})
	return queueIDs, err
}
