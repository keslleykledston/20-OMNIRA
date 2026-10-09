package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresReconciliationStore implements ReconciliationStore. It runs under
// the same cross-tenant system-context pattern already proven by
// internal/routing/adapters/liveness_postgres.go's Retrigger — the runtime
// role stays NOSUPERUSER/NOBYPASSRLS; is_system_admin()-gated RLS policies
// (already relied on by the routing sweep and the publisher) are what make a
// system-wide scan legal, not an elevated connection.
type PostgresReconciliationStore struct{ pool *pgxpool.Pool }

func NewPostgresReconciliationStore(pool *pgxpool.Pool) *PostgresReconciliationStore {
	return &PostgresReconciliationStore{pool: pool}
}

// ReconcileStrandedQueuedSends — see ReconciliationStore. One statement,
// one transaction (implicit — a single multi-CTE query is atomic):
//
//  1. candidates: messages still 'queued' whose most-recently-published
//     job.channel.send_text.v1 intent is older than the cutoff AND that have
//     no currently-unpublished intent, locked FOR UPDATE SKIP LOCKED so two
//     replicas partition the batch instead of double-reconciling a row —
//     eligibility is re-evaluated in this SAME query, so there is no gap
//     between "check" and "insert" for state to change underneath it.
//  2. latest_prev: the most recent previously-published event per candidate,
//     purely for causation_id lineage (never required for correctness).
//  3. inserted: exactly one NEW outbox_events row per candidate. Existing
//     rows are never touched — published_at, attempts and quarantined_at on
//     the original event(s) are left exactly as they were, preserving true
//     history (PILOT.4D3-B1's correction over resetting the original row).
func (s *PostgresReconciliationStore) ReconcileStrandedQueuedSends(ctx context.Context, maxAge, grace time.Duration, batchSize int) (int, error) {
	if maxAge <= 0 {
		// Defense in depth: the Reconciler already refuses to tick when
		// MaxAge<=0, but this store must never do anything destructive-by-
		// omission if ever called directly with an unsafe value.
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = ReconcileBatchSize
	}
	cutoff := time.Now().UTC().Add(-(maxAge + grace))

	var created int
	err := platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(scoped context.Context) error {
		return platformdb.QuerierFromContext(scoped, s.pool).QueryRow(scoped, `
			WITH candidates AS (
			  SELECT m.id AS message_id, m.tenant_id
			  FROM messages m
			  JOIN tenants tn ON tn.id = m.tenant_id AND tn.status = 'active'
			  WHERE m.status = 'queued'
			    AND m.direction = 'outbound'
			    AND EXISTS (
			      SELECT 1 FROM outbox_events o
			      WHERE o.aggregate_type = 'message' AND o.aggregate_id = m.id::text
			        AND o.event_type = 'job.channel.send_text.v1'
			        AND o.published_at IS NOT NULL
			      GROUP BY o.aggregate_id
			      HAVING max(o.published_at) < $1
			    )
			    AND NOT EXISTS (
			      SELECT 1 FROM outbox_events o2
			      WHERE o2.aggregate_type = 'message' AND o2.aggregate_id = m.id::text
			        AND o2.event_type = 'job.channel.send_text.v1'
			        AND o2.published_at IS NULL
			    )
			  ORDER BY m.id
			  LIMIT $2
			  FOR UPDATE OF m SKIP LOCKED
			  FOR SHARE OF tn SKIP LOCKED
			),
			latest_prev AS (
			  SELECT DISTINCT ON (o.aggregate_id) o.aggregate_id, o.id AS prev_event_id
			  FROM outbox_events o
			  WHERE o.aggregate_type = 'message'
			    AND o.event_type = 'job.channel.send_text.v1'
			    AND o.published_at IS NOT NULL
			    AND o.aggregate_id IN (SELECT message_id::text FROM candidates)
			  ORDER BY o.aggregate_id, o.published_at DESC
			),
			inserted AS (
			  INSERT INTO outbox_events
			    (tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id, payload)
			  SELECT c.tenant_id, 'job.channel.send_text.v1', 'message', c.message_id::text,
			         uuid_generate_v4(), lp.prev_event_id, '{}'::jsonb
			  FROM candidates c
			  LEFT JOIN latest_prev lp ON lp.aggregate_id = c.message_id::text
			  RETURNING 1
			)
			SELECT count(*) FROM inserted`,
			cutoff, batchSize).Scan(&created)
	})
	if err != nil {
		return 0, fmt.Errorf("channel-send reconciliation: %w", err)
	}
	return created, nil
}
