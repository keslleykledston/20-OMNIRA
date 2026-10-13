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
	timerBatch       = 50
	timerLease       = 2 * time.Minute
	timerMaxAttempts = 8
)

type claimed struct {
	id           uuid.UUID
	tenant       uuid.UUID
	conversation uuid.UUID
	episode      int
	kind         string
	generation   int
	attempts     int
}

// RunTimers drains due timers until the context ends. A timer is a row: a worker that dies mid-way only leaves a lease that expires,
// and another worker (or the same one after a restart) takes it again.
func (e *Engine) RunTimers(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := e.RunTimersOnce(ctx); err != nil {
			log.Printf("routing engine: timers: %v", err)
		} else if n == timerBatch { // a full batch: more may be due, do not wait a whole tick
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunTimersOnce claims up to timerBatch due timers (pending and due, or leased with the lease expired), FOR UPDATE SKIP LOCKED so two
// workers never take the same row, and runs each in its own tenant session. It returns how many it claimed.
func (e *Engine) RunTimersOnce(ctx context.Context) (int, error) {
	var batch []claimed
	err := platformdb.WithTenantSession(ctx, e.pool, uuid.Nil, true, func(c context.Context) error {
		rows, err := platformdb.QuerierFromContext(c, e.pool).Query(c, `
			UPDATE routing_engine_timers t SET state='leased', lease_until=now()+$1::interval, attempts=t.attempts+1, updated_at=now()
			WHERE t.id IN (SELECT id FROM routing_engine_timers
			               WHERE (state='pending' AND due_at <= now()) OR (state='leased' AND lease_until < now())
			               ORDER BY due_at LIMIT $2 FOR UPDATE SKIP LOCKED)
			RETURNING t.id, t.tenant_id, t.conversation_id, t.episode, t.kind, t.generation, t.attempts`, fmt.Sprintf("%d seconds", int(timerLease.Seconds())), timerBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x claimed
			if err := rows.Scan(&x.id, &x.tenant, &x.conversation, &x.episode, &x.kind, &x.generation, &x.attempts); err != nil {
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
		if !active { // a suspended company is not served (ADR-0038): look again later, never act
			_, err := q.Exec(c, `UPDATE routing_engine_timers SET state='pending', lease_until=NULL, due_at=now()+interval '5 minutes', updated_at=now() WHERE id=$1 AND generation=$2`, x.id, x.generation)
			return err
		}
		var state string
		var gen, episode int
		if err := q.QueryRow(c, `SELECT t.state, t.generation, COALESCE(s.episode, t.episode) FROM routing_engine_timers t LEFT JOIN routing_engine_state s ON s.tenant_id=t.tenant_id AND s.conversation_id=t.conversation_id
			WHERE t.id=$1 FOR UPDATE OF t`, x.id).Scan(&state, &gen, &episode); err != nil {
			return err
		}
		if state != "leased" || gen != x.generation { // re-planned or cancelled while we held it: the new plan owns it
			return nil
		}
		if episode != x.episode { // a person took over and a new episode began: this timer belongs to a story that ended
			_, err := q.Exec(c, `UPDATE routing_engine_timers SET state='cancelled', updated_at=now() WHERE id=$1`, x.id)
			return err
		}
		switch x.kind {
		case "open_wakeup":
			if _, err := e.Decide(c, Request{ConversationID: x.conversation, Trigger: TriggerOpenWakeup, StrategyReason: "open_wakeup"}); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		default: // kinds of phase 5 are not produced yet; never leave an unknown timer leased forever
			_, err := q.Exec(c, `UPDATE routing_engine_timers SET state='cancelled', last_error='unsupported kind', updated_at=now() WHERE id=$1`, x.id)
			return err
		}
		_, err = q.Exec(c, `UPDATE routing_engine_timers SET state='done', lease_until=NULL, updated_at=now() WHERE id=$1 AND generation=$2 AND state='leased'`, x.id, x.generation)
		return err
	})
}

// recordFailure keeps the lease (it expires and the timer is retried) and, after timerMaxAttempts, hands the conversation to a person.
func (e *Engine) recordFailure(ctx context.Context, x claimed, cause error) {
	msg := cause.Error()
	if len(msg) > 180 {
		msg = msg[:180]
	}
	_ = platformdb.WithSystemTenantSession(ctx, e.pool, x.tenant, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, e.pool)
		if x.attempts >= timerMaxAttempts {
			if _, err := q.Exec(c, `UPDATE routing_engine_timers SET state='failed', last_error=$2, updated_at=now() WHERE id=$1 AND generation=$3`, x.id, msg, x.generation); err != nil {
				return err
			}
			_, err := q.Exec(c, `UPDATE routing_engine_state SET state='manual_required', last_decision='REQUIRE_MANUAL_ACTION', updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND episode=$3`, x.tenant, x.conversation, x.episode)
			return err
		}
		_, err := q.Exec(c, `UPDATE routing_engine_timers SET last_error=$2 WHERE id=$1 AND generation=$3`, x.id, msg, x.generation)
		return err
	})
	log.Printf("routing engine: timer %s attempt %d failed: %v", x.id, x.attempts, cause)
}
