package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

const (
	timerBatch        = 50
	timerPerTenant    = 10 // one tenant with a huge backlog cannot take a whole batch: the others are served in the same tick
	timerBurst        = 4  // consecutive full batches before waiting for the next tick
	timerLease        = 2 * time.Minute
	timerMaxAttempts  = 8
	timerLeaseSeconds = int(timerLease / time.Second)
)

type claimed struct {
	id           uuid.UUID
	tenant       uuid.UUID
	conversation uuid.UUID
	episode      int
	kind         string
	generation   int
	attempts     int
	token        uuid.UUID // fencing: every write about this timer requires THIS token, so a worker whose lease expired cannot touch a newer claim
}

// RunTimers drains due timers until the context ends. A timer is a row: a worker that dies mid-way only leaves a lease that expires,
// and another worker (or the same one after a restart) takes it again.
func (e *Engine) RunTimers(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for burst := 0; burst < timerBurst; burst++ {
			n, err := e.RunTimersOnce(ctx)
			if err != nil {
				log.Printf("routing engine: timers: %v", err)
				break
			}
			if n < timerBatch { // not a full batch: nothing more is due
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunTimersOnce claims up to timerBatch due timers (pending and due, or leased with the lease expired), at most timerPerTenant per tenant,
// FOR UPDATE SKIP LOCKED so two workers never take the same row, and runs each in its own tenant session. It returns how many it claimed.
func (e *Engine) RunTimersOnce(ctx context.Context) (int, error) {
	var batch []claimed
	err := platformdb.WithTenantSession(ctx, e.pool, uuid.Nil, true, func(c context.Context) error {
		rows, err := platformdb.QuerierFromContext(c, e.pool).Query(c, `
			WITH due AS (
			  SELECT id, row_number() OVER (PARTITION BY tenant_id ORDER BY due_at) AS rn
			  FROM routing_engine_timers
			  WHERE (state='pending' AND due_at <= now()) OR (state='leased' AND lease_until < now())
			), cand AS (
			  SELECT t.id FROM routing_engine_timers t JOIN due d ON d.id = t.id
			  WHERE d.rn <= $3 AND ((t.state='pending' AND t.due_at <= now()) OR (t.state='leased' AND t.lease_until < now()))
			  ORDER BY t.due_at LIMIT $2 FOR UPDATE OF t SKIP LOCKED
			)
			UPDATE routing_engine_timers t SET state='leased', lease_until=now()+make_interval(secs => $1), attempts=t.attempts+1, claim_token=gen_random_uuid(), updated_at=now()
			WHERE t.id IN (SELECT id FROM cand)
			RETURNING t.id, t.tenant_id, t.conversation_id, t.episode, t.kind, t.generation, t.attempts, t.claim_token`, timerLeaseSeconds, timerBatch, timerPerTenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x claimed
			if err := rows.Scan(&x.id, &x.tenant, &x.conversation, &x.episode, &x.kind, &x.generation, &x.attempts, &x.token); err != nil {
				return err
			}
			batch = append(batch, x)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	for _, x := range batch {
		if err := e.runTimer(ctx, x); err != nil {
			e.recordFailure(ctx, x, err)
		}
	}
	return len(batch), nil
}

func (e *Engine) runTimer(ctx context.Context, x claimed) error {
	return platformdb.WithSystemTenantSession(ctx, e.pool, x.tenant, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, e.pool)
		active, err := platformdb.LockTenantActive(c, q, x.tenant)
		if err != nil {
			return err
		}
		// lock order is the same everywhere: conversation -> routing state -> timer
		var found int
		if err := q.QueryRow(c, `SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, x.tenant, x.conversation).Scan(&found); err != nil {
			return err // the timer references a conversation by foreign key: not finding it is an error to surface, not to swallow
		}
		// documented order: conversation -> routing state -> timer
		if _, err := q.Exec(c, `SELECT 1 FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2 FOR UPDATE`, x.tenant, x.conversation); err != nil {
			return err
		}
		var state string
		var gen int
		var token *uuid.UUID
		if err := q.QueryRow(c, `SELECT state, generation, claim_token FROM routing_engine_timers WHERE id=$1 FOR UPDATE`, x.id).Scan(&state, &gen, &token); err != nil {
			return err
		}
		if state != "leased" || gen != x.generation || token == nil || *token != x.token { // re-planned, cancelled or claimed by somebody else: not ours any more
			return nil
		}
		if !active { // a suspended company is not served (ADR-0038): look again later, never act
			_, err := q.Exec(c, `UPDATE routing_engine_timers SET state='pending', lease_until=NULL, claim_token=NULL, due_at=now()+interval '5 minutes', updated_at=now() WHERE id=$1 AND claim_token=$2`, x.id, x.token)
			return err
		}
		var episode int
		if err := q.QueryRow(c, `SELECT COALESCE((SELECT episode FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2), $3)`, x.tenant, x.conversation, x.episode).Scan(&episode); err != nil {
			return err
		}
		if episode != x.episode { // a person took over and a new episode began: this timer belongs to a story that ended
			_, err := q.Exec(c, `UPDATE routing_engine_timers SET state='cancelled', claim_token=NULL, updated_at=now() WHERE id=$1 AND claim_token=$2`, x.id, x.token)
			return err
		}
		switch x.kind {
		case "open_wakeup", "step_wait", "retry", "sla_risk", "sla_breach":
			trigger := TriggerOpenWakeup
			if x.kind == "sla_risk" || x.kind == "sla_breach" {
				trigger = x.kind
			} else if x.kind != "open_wakeup" { // a step wait ran out, or a policy changed and the conversation must be looked at again
				trigger = TriggerStepWait
			}
			if _, err := e.Decide(c, Request{ConversationID: x.conversation, Trigger: trigger, StrategyReason: trigger}); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		default: // kinds of later phases (sla_*) are not produced yet; never leave an unknown timer leased forever
			_, err := q.Exec(c, `UPDATE routing_engine_timers SET state='cancelled', last_error='unsupported kind', claim_token=NULL, updated_at=now() WHERE id=$1 AND claim_token=$2`, x.id, x.token)
			return err
		}
		// Decide may have moved the timer (still closed) or cancelled it (assigned): the token no longer matches then, and that is right
		_, err = q.Exec(c, `UPDATE routing_engine_timers SET state='done', lease_until=NULL, claim_token=NULL, updated_at=now() WHERE id=$1 AND claim_token=$2 AND state='leased'`, x.id, x.token)
		return err
	})
}

// recordFailure keeps the lease (it expires and the timer is retried) and, after timerMaxAttempts, hands the conversation to a person.
// Only the holder of the current token may do either.
// recordFailure takes the CONVERSATION lock first, like every other writer, and only then touches the timer and the state. The stated order is
// conversation -> state -> timer; here the timer is updated before the state because the state change depends on this worker still holding the
// claim. That cannot form a cycle: every path that takes both rows holds the conversation lock first (so they never run together for one
// conversation), and RunTimersOnce, which locks timers without the conversation, never touches the state.
func (e *Engine) recordFailure(ctx context.Context, x claimed, cause error) {
	msg := cause.Error()
	if len(msg) > 180 {
		msg = msg[:180]
	}
	_ = platformdb.WithSystemTenantSession(ctx, e.pool, x.tenant, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, e.pool)
		var found int
		if err := q.QueryRow(c, `SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, x.tenant, x.conversation).Scan(&found); err != nil {
			return err
		}
		if x.attempts >= timerMaxAttempts {
			tag, err := q.Exec(c, `UPDATE routing_engine_timers SET state='failed', last_error=$2, claim_token=NULL, updated_at=now() WHERE id=$1 AND claim_token=$3 AND state='leased'`, x.id, msg, x.token)
			if err != nil || tag.RowsAffected() != 1 { // somebody else holds a newer claim: it is theirs to judge
				return err
			}
			_, err = q.Exec(c, `UPDATE routing_engine_state SET state='manual_required', last_decision='REQUIRE_MANUAL_ACTION', updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND episode=$3 AND state='waiting_open'`, x.tenant, x.conversation, x.episode)
			return err
		}
		_, err := q.Exec(c, `UPDATE routing_engine_timers SET last_error=$2 WHERE id=$1 AND claim_token=$3 AND state='leased'`, x.id, msg, x.token)
		return err
	})
	log.Printf("routing engine: timer %s attempt %d failed: %v", x.id, x.attempts, fmt.Errorf("%w", cause))
}
