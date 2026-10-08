// Package hubprojector keeps hub_inbox_items (the Hub's read model) in step with the tenants' conversations.
//
// Trust model: the (hub, tenant) pairs come ONLY from persisted service contracts (and from rows the projector
// itself wrote earlier, so it can clean them up). Nothing here accepts a hub or tenant from a request, an event
// payload or a caller. Each pair runs in its own system tenant session derived from that persisted tenant.
// The projection is a cache of tenant data: the source of truth stays in conversations/messages/tickets, and
// access to the cached rows is still decided by RLS and by the Hub authorization service, never by this package.
package hubprojector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// DefaultLookbackDays bounds how long a CLOSED conversation stays in the Hub inbox. Open conversations are always projected.
const DefaultLookbackDays = 30

// Result counts what one reconcile changed.
type Result struct{ Upserted, Removed int }

func (r *Result) add(o Result) { r.Upserted += o.Upserted; r.Removed += o.Removed }

type Projector struct {
	pool         *pgxpool.Pool
	lookbackDays int
}

func New(pool *pgxpool.Pool) *Projector {
	return &Projector{pool: pool, lookbackDays: DefaultLookbackDays}
}

func (p *Projector) WithLookbackDays(days int) *Projector {
	if days > 0 {
		p.lookbackDays = days
	}
	return p
}

// reconcileSQL projects one (hub, tenant) pair, optionally limited to one conversation ($4).
//   - $1 hub, $2 tenant, $3 lookback days, $4 conversation id or NULL.
//   - src is empty unless the contract is live and the hub active, so a revoked/expired/suspended relationship
//     removes every row of the pair in the same statement.
//   - internal (staff) conversations are never projected; closed ones only inside the lookback window.
//   - unread_count = customer messages since the last successful operator reply (same rule as the tenant inbox's "waiting").
//   - sla_due_at is not projected: the platform has no contractual SLA source yet (the tenant inbox only has display thresholds).
//   - rows are rewritten only when a projected value changed, so an idempotent run does not churn version/updated_at.
const reconcileSQL = `
WITH live AS (
  SELECT 1
  FROM hub_tenant_service_contracts c
  JOIN service_hubs h ON h.id = c.hub_id
  WHERE c.hub_id = $1 AND c.tenant_id = $2
    AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
    AND h.status = 'active'
),
src AS (
  SELECT c.id AS conversation_id, c.queue_id, c.assigned_to_user_id AS assigned_user_id,
         COALESCE(ct.display_name, '') AS customer_name,
         COALESCE(cc.channel, '') AS channel,
         c.status AS status,
         COALESCE(tk.priority, 'normal') AS priority,
         COALESCE(lm.created_at, c.created_at) AS last_activity_at,
         COALESCE(w.unread, 0) AS unread_count
  FROM conversations c
  LEFT JOIN contacts ct ON ct.tenant_id = c.tenant_id AND ct.id = c.contact_id
  LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id
  LEFT JOIN LATERAL (SELECT m.created_at FROM messages m
        WHERE m.tenant_id = c.tenant_id AND m.conversation_id = c.id
        ORDER BY m.created_at DESC, m.id DESC LIMIT 1) lm ON true
  LEFT JOIN LATERAL (SELECT CASE tk.priority WHEN 'critical' THEN 'urgent' WHEN 'high' THEN 'high' WHEN 'low' THEN 'low' ELSE 'normal' END AS priority
        FROM tickets tk
        WHERE tk.tenant_id = c.tenant_id AND tk.conversation_id = c.id AND tk.status IN ('open', 'in_progress', 'waiting')
        ORDER BY tk.created_at DESC LIMIT 1) tk ON true
  LEFT JOIN LATERAL (SELECT count(*) AS unread FROM messages m
        WHERE m.tenant_id = c.tenant_id AND m.conversation_id = c.id AND m.direction = 'inbound'
          AND m.created_at > COALESCE((SELECT max(o.created_at) FROM messages o
                WHERE o.tenant_id = c.tenant_id AND o.conversation_id = c.id AND o.direction = 'outbound' AND o.status <> 'failed'),
                '-infinity'::timestamptz)) w ON true
  WHERE c.tenant_id = $2
    AND c.conversation_kind <> 'internal'
    AND ($4::uuid IS NULL OR c.id = $4::uuid)
    AND (c.status = 'open' OR COALESCE(lm.created_at, c.created_at) > now() - make_interval(days => $3))
    AND EXISTS (SELECT 1 FROM live)
),
removed AS (
  DELETE FROM hub_inbox_items h
  WHERE h.hub_id = $1 AND h.tenant_id = $2
    AND ($4::uuid IS NULL OR h.conversation_id = $4::uuid)
    AND NOT EXISTS (SELECT 1 FROM src s WHERE s.conversation_id = h.conversation_id)
  RETURNING 1
),
upserted AS (
  INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id, queue_id, assigned_user_id, customer_name, channel,
                               status, priority, last_activity_at, unread_count)
  SELECT $1, $2, s.conversation_id, s.queue_id, s.assigned_user_id, s.customer_name, s.channel,
         s.status, s.priority, s.last_activity_at, s.unread_count
  FROM src s
  ON CONFLICT (hub_id, tenant_id, conversation_id) DO UPDATE SET
    queue_id = EXCLUDED.queue_id, assigned_user_id = EXCLUDED.assigned_user_id, customer_name = EXCLUDED.customer_name,
    channel = EXCLUDED.channel, status = EXCLUDED.status, priority = EXCLUDED.priority,
    last_activity_at = EXCLUDED.last_activity_at, unread_count = EXCLUDED.unread_count,
    version = hub_inbox_items.version + 1, updated_at = now()
  WHERE (hub_inbox_items.queue_id, hub_inbox_items.assigned_user_id, hub_inbox_items.customer_name, hub_inbox_items.channel,
         hub_inbox_items.status, hub_inbox_items.priority, hub_inbox_items.last_activity_at, hub_inbox_items.unread_count)
        IS DISTINCT FROM
        (EXCLUDED.queue_id, EXCLUDED.assigned_user_id, EXCLUDED.customer_name, EXCLUDED.channel,
         EXCLUDED.status, EXCLUDED.priority, EXCLUDED.last_activity_at, EXCLUDED.unread_count)
  RETURNING 1
)
SELECT (SELECT count(*) FROM upserted), (SELECT count(*) FROM removed)`

// ReconcileTenant projects every conversation of one tenant into one hub (and removes rows that no longer apply).
func (p *Projector) ReconcileTenant(ctx context.Context, hubID, tenantID uuid.UUID) (Result, error) {
	return p.run(ctx, hubID, tenantID, uuid.Nil)
}

// ProjectConversation re-projects a single conversation for every hub that serves its tenant. It is the entry
// point an event-driven trigger can call later; the tenant is read from the PERSISTED conversation, not from the caller.
func (p *Projector) ProjectConversation(ctx context.Context, conversationID uuid.UUID) (Result, error) {
	if conversationID == uuid.Nil {
		return Result{}, errors.New("hubprojector: conversation id is required")
	}
	var pairs [][2]uuid.UUID
	err := p.system(ctx, func(c context.Context) error {
		rows, err := platformdb.QuerierFromContext(c, p.pool).Query(c, `
			SELECT DISTINCT x.hub_id, x.tenant_id FROM (
			  SELECT ct.hub_id, ct.tenant_id FROM hub_tenant_service_contracts ct
			    JOIN conversations cv ON cv.tenant_id = ct.tenant_id WHERE cv.id = $1
			  UNION
			  SELECT i.hub_id, i.tenant_id FROM hub_inbox_items i WHERE i.conversation_id = $1
			) x`, conversationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pr [2]uuid.UUID
			if err := rows.Scan(&pr[0], &pr[1]); err != nil {
				return err
			}
			pairs = append(pairs, pr)
		}
		return rows.Err()
	})
	if err != nil {
		return Result{}, fmt.Errorf("hubprojector: resolve hubs of conversation: %w", err)
	}
	var total Result
	var errs []error
	for _, pr := range pairs {
		res, err := p.run(ctx, pr[0], pr[1], conversationID)
		total.add(res)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

// ReconcileAll reconciles every (hub, tenant) pair that has a contract (live or not, so revocations are cleaned up)
// or still holds projected rows (so a deleted contract cannot leave personal data behind). A failing pair does not
// stop the others; the errors are joined.
func (p *Projector) ReconcileAll(ctx context.Context) (Result, error) {
	var pairs [][2]uuid.UUID
	err := p.system(ctx, func(c context.Context) error {
		rows, err := platformdb.QuerierFromContext(c, p.pool).Query(c, `
			SELECT hub_id, tenant_id FROM hub_tenant_service_contracts
			UNION
			SELECT hub_id, tenant_id FROM hub_inbox_items`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pr [2]uuid.UUID
			if err := rows.Scan(&pr[0], &pr[1]); err != nil {
				return err
			}
			pairs = append(pairs, pr)
		}
		return rows.Err()
	})
	if err != nil {
		return Result{}, fmt.Errorf("hubprojector: list hub/tenant pairs: %w", err)
	}
	var total Result
	var errs []error
	for _, pr := range pairs {
		res, err := p.run(ctx, pr[0], pr[1], uuid.Nil)
		total.add(res)
		if err != nil {
			errs = append(errs, fmt.Errorf("hub %s tenant %s: %w", pr[0], pr[1], err))
		}
	}
	return total, errors.Join(errs...)
}

// Run reconciles on a fixed interval until ctx ends. The first pass runs immediately.
func (p *Projector) Run(ctx context.Context, interval time.Duration) {
	if interval < time.Second {
		interval = time.Minute
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if res, err := p.ReconcileAll(ctx); err != nil {
			log.Printf("hub projector: %v", err)
		} else if res.Upserted+res.Removed > 0 {
			log.Printf("hub projector: %d upserted, %d removed", res.Upserted, res.Removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (p *Projector) run(ctx context.Context, hubID, tenantID, conversationID uuid.UUID) (Result, error) {
	if hubID == uuid.Nil || tenantID == uuid.Nil {
		return Result{}, errors.New("hubprojector: hub and tenant ids are required")
	}
	var conv any
	if conversationID != uuid.Nil {
		conv = conversationID
	}
	var res Result
	err := platformdb.WithSystemTenantSession(ctx, p.pool, tenantID, func(c context.Context) error {
		var up, rm int64
		if err := platformdb.QuerierFromContext(c, p.pool).QueryRow(c, reconcileSQL, hubID, tenantID, p.lookbackDays, conv).Scan(&up, &rm); err != nil {
			return err
		}
		res = Result{Upserted: int(up), Removed: int(rm)}
		return nil
	})
	return res, err
}

func (p *Projector) system(ctx context.Context, fn func(context.Context) error) error {
	return platformdb.WithTenantSession(ctx, p.pool, uuid.Nil, true, fn)
}
