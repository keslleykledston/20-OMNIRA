package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Pending lists inbox items that a round-robin pool could take: open, unassigned (as the projection last saw them), in an instance a
// round-robin pool answers for. The conversation is checked again, under lock, by Distribute; this list is only where to look.
func (s *Service) Pending(ctx context.Context, limit int) ([]Item, error) {
	if limit < 1 || limit > 500 {
		limit = 50
	}
	var out []Item
	err := platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		rows, err := platformdb.QuerierFromContext(c, s.pool).Query(c, `
			SELECT i.hub_id, i.id
			FROM hub_inbox_items i
			JOIN tenants t ON t.id = i.tenant_id AND t.status = 'active'
			WHERE i.assigned_user_id IS NULL AND i.status <> 'closed'
			  AND EXISTS (SELECT 1 FROM work_pool_instances wi JOIN work_pools p ON p.id = wi.work_pool_id
			               WHERE wi.hub_id = i.hub_id AND wi.tenant_id = i.tenant_id AND p.distribution = 'round_robin')
			ORDER BY i.last_activity_at, i.id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it Item
			if err := rows.Scan(&it.Hub, &it.ID); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	return out, err
}

// Item identifies one hub inbox item.
type Item struct{ Hub, ID uuid.UUID }

// pin takes, until the transaction ends, the rows the person's authorization depends on, one statement per table and always in the
// order the administrative paths take them (company, hub, contract, membership, user, grant). Revoking a grant, ending a contract,
// removing the person from the hub or deactivating the account each UPDATE/DELETE one of these rows, so the change either commits
// before the check that follows or waits until the assignment is written (the same rule as the reply path, Codex H1).
func pin(c context.Context, q platformdb.Querier, hub, tenant, person uuid.UUID) error {
	for _, p := range []struct {
		sql  string
		args []any
	}{
		{`SELECT 1 FROM tenants WHERE id = $1 FOR SHARE`, []any{tenant}},
		{`SELECT 1 FROM service_hubs WHERE id = $1 FOR SHARE`, []any{hub}},
		{`SELECT 1 FROM hub_tenant_service_contracts WHERE hub_id = $1 AND tenant_id = $2 FOR SHARE`, []any{hub, tenant}},
		{`SELECT 1 FROM hub_memberships WHERE hub_id = $1 AND user_id = $2 FOR SHARE`, []any{hub, person}},
		{`SELECT 1 FROM users WHERE id = $1 FOR SHARE`, []any{person}},
		{`SELECT 1 FROM effective_access_grants WHERE hub_id = $1 AND tenant_id = $2 AND user_id = $3 FOR SHARE`, []any{hub, tenant, person}},
	} {
		rows, err := q.Query(c, p.sql, p.args...)
		if err != nil {
			return fmt.Errorf("pin authorization: %w", err)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pin authorization: %w", err)
		}
	}
	return nil
}

// CanTake says, inside the caller's transaction, whether the person may be given this conversation RIGHT NOW: an active account, and a
// live reply-capable delegation covering the conversation's current queue. It pins first, so the answer holds until the transaction ends.
func CanTake(c context.Context, q platformdb.Querier, hub, tenant uuid.UUID, queue *uuid.UUID, person uuid.UUID) (bool, error) {
	if err := pin(c, q, hub, tenant, person); err != nil {
		return false, err
	}
	var ok bool
	err := q.QueryRow(c, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
	                           AND has_active_hub_access($2, $1, $3, $4, true, true)`, tenant, person, queue, hub).Scan(&ok)
	return ok, err
}

// Distribute gives ONE open, unassigned conversation to the least loaded member of the round-robin pool that answers for its
// instance and queue (ties: whoever has waited longest, then by id). It returns who got it, or nil when nothing was assigned
// (already assigned or closed, busy, suspended company, no pool, nobody available). Idempotent and safe to run from several workers:
// the conversation row is locked SKIP LOCKED, so a second worker, a person claiming, or a transfer simply wins or loses cleanly.
func (s *Service) Distribute(ctx context.Context, hub, item uuid.UUID) (assigned *uuid.UUID, err error) {
	var tenant, conversation uuid.UUID
	err = platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		return platformdb.QuerierFromContext(c, s.pool).QueryRow(c,
			`SELECT tenant_id, conversation_id FROM hub_inbox_items WHERE id = $1 AND hub_id = $2 AND status <> 'closed'`, item, hub).Scan(&tenant, &conversation)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = platformdb.WithSystemTenantSession(ctx, s.pool, tenant, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, s.pool)
		// a suspended company is not served (ADR-0038): nothing is assigned in it, and the suspension waits for this transaction
		active, err := platformdb.LockTenantActive(c, q, tenant)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		var queue, holder *uuid.UUID
		var status string
		err = q.QueryRow(c, `SELECT queue_id, status, assigned_to_user_id FROM conversations WHERE tenant_id = $1 AND id = $2 FOR UPDATE SKIP LOCKED`,
			tenant, conversation).Scan(&queue, &status, &holder)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // somebody else holds the row right now: they decide, we try again later
		}
		if err != nil {
			return err
		}
		if status == "closed" || holder != nil {
			return nil
		}
		var pool uuid.UUID
		err = q.QueryRow(c, `
			SELECT p.id FROM work_pool_instances wi JOIN work_pools p ON p.id = wi.work_pool_id
			WHERE wi.hub_id = $1 AND wi.tenant_id = $2 AND (wi.queue_id IS NOT DISTINCT FROM $3 OR wi.queue_id IS NULL)
			  AND p.distribution = 'round_robin'
			ORDER BY (wi.queue_id IS NULL) LIMIT 1`, hub, tenant, queue).Scan(&pool)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// Capacity is checked against what each person holds NOW, and two workers (or two replicas) must not both see room for the last
		// slot - not even through DIFFERENT pools that share a person (Codex review): the automatic assignments of one hub are serialized
		// on a per-hub lock until this transaction ends, and so is every edit of a pool (lockHub). Manual claims and transfers are
		// deliberately not counted against this soft limit and do not take the lock: capacity only decides who the AUTOMATIC distribution
		// picks, and at the very instant of a manual claim a person may hold one more than their capacity until the next round sees it.
		if err := lockHub(c, q, hub); err != nil {
			return err
		}
		// candidates in rotation order; the authorization of each one is re-proven (pinned) before anybody is chosen
		rows, err := q.Query(c, `
			SELECT m.user_id
			FROM work_pool_members m
			CROSS JOIN LATERAL (
			  SELECT count(*) AS load FROM conversations c
			  WHERE c.assigned_to_user_id = m.user_id AND c.status <> 'closed'
			    AND c.tenant_id IN (SELECT tenant_id FROM hub_tenant_service_contracts WHERE hub_id = $2)) l
			WHERE m.work_pool_id = $1 AND l.load < m.max_open
			ORDER BY l.load, m.last_assigned_at NULLS FIRST, m.user_id`, pool, hub)
		if err != nil {
			return err
		}
		var candidates []uuid.UUID
		for rows.Next() {
			var u uuid.UUID
			if err := rows.Scan(&u); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, u)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, person := range candidates {
			ok, err := CanTake(c, q, hub, tenant, queue, person)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if _, err := q.Exec(c, `UPDATE conversations SET assigned_to_user_id = $3, assigned_at = now(), updated_at = now() WHERE tenant_id = $1 AND id = $2`,
				tenant, conversation, person); err != nil {
				return fmt.Errorf("assign: %w", err)
			}
			if _, err := q.Exec(c, `INSERT INTO assignment_events (tenant_id, conversation_id, from_user_id, to_user_id, changed_by, reason, actor_source)
			                        VALUES ($1, $2, NULL, $3, NULL, 'hub_pool', 'system')`, tenant, conversation, person); err != nil {
				return fmt.Errorf("assignment history: %w", err)
			}
			tag, err := q.Exec(c, `UPDATE work_pool_members SET last_assigned_at = now() WHERE work_pool_id = $1 AND user_id = $2`, pool, person)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("work pool member vanished during the assignment") // the whole assignment rolls back
			}
			raw, err := json.Marshal(map[string]any{"hub_id": hub, "conversation_id": conversation, "work_pool_id": pool, "via": "omnira-work-pools"})
			if err != nil {
				return err
			}
			if _, err := q.Exec(c, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
			                        VALUES ($1, $2, NULL, 'hub.conversation.auto_assigned', 'conversation', $3, 'success', $4, $5)`,
				uuid.New(), tenant, conversation.String(), uuid.NewString(), raw); err != nil {
				return err
			}
			chosen := person
			assigned = &chosen
			return nil
		}
		return nil
	})
	return assigned, err
}
