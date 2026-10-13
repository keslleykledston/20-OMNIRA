package engine

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/overflow"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
)

// SLA (ADR-0044 phase 5c). Four metrics, each with its own budget; the policy that applies is the most specific one (queue, then sector,
// then tenant), metric by metric. A clock is NEVER restarted to hide a delay: when the conversation moves, the time already lived is kept
// and only what is left is re-planned, on the calendar of the place it is in now. queue_wait is the one deliberate exception: by
// definition it is the wait in the CURRENT queue.
const (
	metricFirstResponse = "first_response"
	metricQueueWait     = "queue_wait"
	metricAssignment    = "assignment"
	metricResolution    = "resolution"
)

var slaMetrics = []string{metricFirstResponse, metricQueueWait, metricAssignment, metricResolution}

// the step_index of a metric's timers (the unique key of routing_engine_timers carries it)
var slaStepIndex = map[string]int{metricFirstResponse: 1, metricQueueWait: 2, metricAssignment: 3, metricResolution: 4}

type slaBudget struct {
	seconds  int
	risk     int
	counting string
}

// effectiveSLA resolves the budgets that apply to a queue: queue policy, else sector policy, else tenant policy, per metric.
func effectiveSLA(ctx context.Context, q platformdb.Querier, tenant, queue uuid.UUID) (map[string]slaBudget, error) {
	rows, err := q.Query(ctx, `SELECT first_response_seconds, queue_wait_seconds, assignment_seconds, resolution_seconds, risk_percent, counting
		FROM sla_policies p
		WHERE p.tenant_id=$1 AND p.active AND (
		  (p.scope_kind='queue' AND p.queue_id=$2)
		  OR (p.scope_kind='sector' AND p.sector_id=(SELECT sector_id FROM queues WHERE tenant_id=$1 AND id=$2))
		  OR p.scope_kind='tenant')
		ORDER BY CASE p.scope_kind WHEN 'queue' THEN 0 WHEN 'sector' THEN 1 ELSE 2 END`, tenant, queue)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]slaBudget{}
	for rows.Next() {
		var fr, qw, as, rs *int
		var risk int
		var counting string
		if err := rows.Scan(&fr, &qw, &as, &rs, &risk, &counting); err != nil {
			return nil, err
		}
		for m, v := range map[string]*int{metricFirstResponse: fr, metricQueueWait: qw, metricAssignment: as, metricResolution: rs} {
			if _, set := out[m]; !set && v != nil {
				out[m] = slaBudget{seconds: *v, risk: risk, counting: counting}
			}
		}
	}
	return out, rows.Err()
}

type slaEvent struct {
	metric string
	kind   string // "sla_risk" | "sla_breach"
}

type slaResult struct {
	triggers []overflow.Trigger // what the overflow chain should also consider (risk / breach of a clock that is still open)
}

// times plans the risk and deadline instants. `consumed` is the time already lived (re-plan after a move); the rest is counted from
// `from` on the calendar of calQueue (business counting) or the wall clock.
func slaTimes(ctx context.Context, q platformdb.Querier, tenant, calQueue uuid.UUID, from time.Time, b slaBudget, consumed time.Duration) (risk, deadline time.Time, err error) {
	total := time.Duration(b.seconds) * time.Second
	riskBudget := total * time.Duration(b.risk) / 100
	left := func(d time.Duration) time.Duration {
		if d -= consumed; d < 0 {
			return 0
		}
		return d
	}
	business := b.counting == "business"
	if deadline, err = sectorsadapters.DueAfter(ctx, q, tenant, calQueue, from, left(total), business); err != nil {
		return
	}
	risk, err = sectorsadapters.DueAfter(ctx, q, tenant, calQueue, from, left(riskBudget), business)
	return
}

// syncSLA creates, re-plans, closes and evaluates the clocks of one conversation, in the decision's transaction. It returns the
// triggers the overflow chain should also consider. Events (risk / breach) are written once: the state change is what makes them once.
func (e *Engine) syncSLA(ctx context.Context, q platformdb.Querier, tenant, conv uuid.UUID, c convRow, st *stateRow, now time.Time) (slaResult, error) {
	var res slaResult
	if c.status != "open" { // closing finishes the resolution clock and cancels the rest; nothing keeps running
		if _, err := q.Exec(ctx, `UPDATE conversation_sla SET state=CASE WHEN metric='resolution' THEN 'met' ELSE 'cancelled' END,
			met_at=CASE WHEN metric='resolution' THEN $3::timestamptz END, updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND state='running'`, tenant, conv, now); err != nil {
			return res, err
		}
		_, err := q.Exec(ctx, `UPDATE routing_engine_timers SET state='cancelled', claim_token=NULL, updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND kind IN ('sla_risk','sla_breach') AND state IN ('pending','leased')`, tenant, conv)
		return res, err
	}
	if c.queueID == nil {
		return res, nil
	}
	queue := *c.queueID
	eff, err := effectiveSLA(ctx, q, tenant, queue)
	if err != nil {
		return res, err
	}
	if len(eff) == 0 {
		return res, nil
	}
	// clocks of an earlier episode or of a queue the conversation left stop (they are history); the cumulative ones continue
	if _, err := q.Exec(ctx, `UPDATE conversation_sla SET state='cancelled', updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND state='running'
		AND ((metric IN ('assignment','queue_wait') AND scope_episode<>$3) OR (metric='queue_wait' AND queue_key<>$4))`, tenant, conv, st.episode, queue); err != nil {
		return res, err
	}
	for _, m := range slaMetrics {
		b, ok := eff[m]
		if !ok {
			continue
		}
		epoch, qkey := 0, uuid.Nil
		started := c.createdAt
		switch m {
		case metricAssignment:
			epoch, started = st.episode, st.enteredQueue
		case metricQueueWait:
			epoch, qkey, started = st.episode, queue, st.stepEntered
		}
		var id uuid.UUID
		var state string
		var calQueue *uuid.UUID
		var startedAt time.Time
		var budget int
		var riskNotified *time.Time
		err := q.QueryRow(ctx, `SELECT id, state, calendar_queue_id, started_at, budget_seconds, risk_notified_at FROM conversation_sla
			WHERE tenant_id=$1 AND conversation_id=$2 AND metric=$3 AND scope_episode=$4 AND queue_key=$5 FOR UPDATE`, tenant, conv, m, epoch, qkey).
			Scan(&id, &state, &calQueue, &startedAt, &budget, &riskNotified)
		if errors.Is(err, pgx.ErrNoRows) {
			risk, deadline, err := slaTimes(ctx, q, tenant, queue, started, b, 0)
			if err != nil {
				return res, err
			}
			if _, err := q.Exec(ctx, `INSERT INTO conversation_sla (tenant_id, conversation_id, metric, scope_episode, queue_key, started_at, budget_seconds, risk_percent, counting, calendar_queue_id, deadline_at, risk_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`, tenant, conv, m, epoch, qkey, started, b.seconds, b.risk, b.counting, queue, deadline, risk); err != nil {
				return res, err
			}
			if err = q.QueryRow(ctx, `SELECT id, state, calendar_queue_id, started_at, budget_seconds, risk_notified_at FROM conversation_sla
				WHERE tenant_id=$1 AND conversation_id=$2 AND metric=$3 AND scope_episode=$4 AND queue_key=$5 FOR UPDATE`, tenant, conv, m, epoch, qkey).
				Scan(&id, &state, &calQueue, &startedAt, &budget, &riskNotified); err != nil {
				return res, err
			}
		} else if err != nil {
			return res, err
		}
		if state != "running" && state != "breached" {
			continue
		}
		// satisfied?
		metAt, err := e.slaMetAt(ctx, q, tenant, conv, m, c, startedAt, now)
		if err != nil {
			return res, err
		}
		if metAt != nil {
			if _, err := q.Exec(ctx, `UPDATE conversation_sla SET state=CASE WHEN state='breached' THEN 'breached' ELSE 'met' END, met_at=$2, updated_at=now() WHERE id=$1`, id, *metAt); err != nil {
				return res, err
			}
			if err := cancelSLATimers(ctx, q, tenant, conv, m); err != nil {
				return res, err
			}
			continue
		}
		// the conversation moved: keep the time lived, re-plan the rest on the calendar of where it is now
		if state == "running" && calQueue != nil && *calQueue != queue && m != metricQueueWait {
			consumed, err := sectorsadapters.Elapsed(ctx, q, tenant, *calQueue, startedAt, now, b.counting == "business")
			if err != nil {
				return res, err
			}
			bb := slaBudget{seconds: budget, risk: b.risk, counting: b.counting}
			risk, deadline, err := slaTimes(ctx, q, tenant, queue, now, bb, consumed)
			if err != nil {
				return res, err
			}
			if _, err := q.Exec(ctx, `UPDATE conversation_sla SET calendar_queue_id=$2, deadline_at=$3, risk_at=$4, updated_at=now() WHERE id=$1`, id, queue, deadline, risk); err != nil {
				return res, err
			}
		}
		var riskAt, deadline time.Time
		if err := q.QueryRow(ctx, `SELECT risk_at, deadline_at FROM conversation_sla WHERE id=$1`, id).Scan(&riskAt, &deadline); err != nil {
			return res, err
		}
		switch {
		case state == "running" && !now.Before(deadline):
			if _, err := q.Exec(ctx, `UPDATE conversation_sla SET state='breached', breached_at=$2, updated_at=now() WHERE id=$1 AND state='running'`, id, now); err != nil {
				return res, err
			}
			if riskNotified == nil { // a clock found already past its deadline still reports the risk it went through
				if err := e.slaEvent(ctx, q, tenant, conv, st, slaEvent{m, "sla_risk"}); err != nil {
					return res, err
				}
				if _, err := q.Exec(ctx, `UPDATE conversation_sla SET risk_notified_at=$2 WHERE id=$1`, id, now); err != nil {
					return res, err
				}
			}
			if err := e.slaEvent(ctx, q, tenant, conv, st, slaEvent{m, "sla_breach"}); err != nil {
				return res, err
			}
			res.triggers = appendUnique(res.triggers, overflow.SLABreach)
			if err := cancelSLATimers(ctx, q, tenant, conv, m); err != nil {
				return res, err
			}
		case state == "breached":
			res.triggers = appendUnique(res.triggers, overflow.SLABreach)
		case state == "running" && !now.Before(riskAt):
			if riskNotified == nil {
				if _, err := q.Exec(ctx, `UPDATE conversation_sla SET risk_notified_at=$2, updated_at=now() WHERE id=$1`, id, now); err != nil {
					return res, err
				}
				if err := e.slaEvent(ctx, q, tenant, conv, st, slaEvent{m, "sla_risk"}); err != nil {
					return res, err
				}
			}
			res.triggers = appendUnique(res.triggers, overflow.SLARisk)
			if err := planTimer(ctx, q, tenant, conv, st.episode, "sla_breach", slaStepIndex[m], deadline); err != nil {
				return res, err
			}
		default: // running, before the risk point: wake up at the risk point and at the deadline
			if err := planTimer(ctx, q, tenant, conv, st.episode, "sla_risk", slaStepIndex[m], riskAt); err != nil {
				return res, err
			}
			if err := planTimer(ctx, q, tenant, conv, st.episode, "sla_breach", slaStepIndex[m], deadline); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

func appendUnique(in []overflow.Trigger, t overflow.Trigger) []overflow.Trigger {
	for _, x := range in {
		if x == t {
			return in
		}
	}
	return append(in, t)
}

// slaMetAt says when (if) the metric was satisfied: nil while it is still open.
func (e *Engine) slaMetAt(ctx context.Context, q platformdb.Querier, tenant, conv uuid.UUID, metric string, c convRow, started, now time.Time) (*time.Time, error) {
	switch metric {
	case metricAssignment, metricQueueWait:
		if c.assigned != nil {
			t := now
			if c.assignedAt != nil {
				t = *c.assignedAt
			}
			return &t, nil
		}
	case metricFirstResponse: // the first message a PERSON sent after the clock started (automatic messages do not count)
		var at *time.Time
		if err := q.QueryRow(ctx, `SELECT min(created_at) FROM messages WHERE tenant_id=$1 AND conversation_id=$2 AND direction='outbound' AND sent_by_user_id IS NOT NULL AND created_at >= $3`,
			tenant, conv, started).Scan(&at); err != nil {
			return nil, err
		}
		return at, nil
	}
	return nil, nil
}

// slaEvent records the crossing once (the decision key makes a repeat a no-op) and announces it for whoever must act on it. The
// payload carries ids and the metric, never message content. A conversation somebody already owns is NOT reassigned by an SLA: the
// owner's supervisor is told.
func (e *Engine) slaEvent(ctx context.Context, q platformdb.Querier, tenant, conv uuid.UUID, st *stateRow, ev slaEvent) error {
	reason := ev.kind
	if _, err := q.Exec(ctx, `INSERT INTO routing_engine_decisions (tenant_id, conversation_id, episode, step_index, trigger, decision, destination, reason, detail)
		VALUES ($1,$2,$3,$4,$5,'ESCALATE',$6,$7, jsonb_build_object('metric', $8::text))
		ON CONFLICT (tenant_id, conversation_id, episode, step_index, trigger) DO NOTHING`,
		tenant, conv, st.episode, st.step, ev.kind+":"+ev.metric, queueKey(st), reason, ev.metric); err != nil {
		return err
	}
	eventType := "routing.sla.at_risk.v1"
	if ev.kind == "sla_breach" {
		eventType = "routing.sla.breached.v1"
	}
	_, err := q.Exec(ctx, `INSERT INTO outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
		VALUES ($1,$2,$3,'conversation',$4,$5, jsonb_build_object('metric', $6::text))`, uuid.New(), tenant, eventType, conv.String(), uuid.New(), ev.metric)
	return err
}

func queueKey(st *stateRow) string {
	if st.queue == nil {
		return ""
	}
	return overflow.DestKey(overflow.DestQueue, *st.queue)
}

func cancelSLATimers(ctx context.Context, q platformdb.Querier, tenant, conv uuid.UUID, metric string) error {
	_, err := q.Exec(ctx, `UPDATE routing_engine_timers SET state='cancelled', claim_token=NULL, updated_at=now()
		WHERE tenant_id=$1 AND conversation_id=$2 AND kind IN ('sla_risk','sla_breach') AND step_index=$3 AND state IN ('pending','leased')`, tenant, conv, slaStepIndex[metric])
	return err
}
