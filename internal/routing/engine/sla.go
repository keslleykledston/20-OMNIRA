package engine

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	outboxdomain "github.com/omnira/omnira/internal/outbox/domain"
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
	if c.status != "open" {
		return res, e.closeSLA(ctx, q, tenant, conv, c, st, now)
	}
	if c.queueID == nil {
		return res, nil
	}
	queue := *c.queueID
	// clocks of an earlier episode or of a queue the conversation left stop (they are history; the cumulative ones continue). This runs
	// even when the new place has NO policy, so a clock of the queue it left never keeps running.
	if _, err := q.Exec(ctx, `UPDATE conversation_sla SET state='cancelled', updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND state='running'
		AND ((metric IN ('assignment','queue_wait') AND scope_episode<>$3) OR (metric='queue_wait' AND queue_key<>$4))`, tenant, conv, st.episode, queue); err != nil {
		return res, err
	}
	eff, err := effectiveSLA(ctx, q, tenant, queue)
	if err != nil {
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
		cl, err := e.loadClock(ctx, q, tenant, conv, m, epoch, qkey)
		if err != nil {
			return res, err
		}
		if cl == nil { // a new clock: its budget, risk point and counting are fixed NOW and never follow later policy edits
			risk, deadline, err := slaTimes(ctx, q, tenant, queue, started, b, 0)
			if err != nil {
				return res, err
			}
			if _, err := q.Exec(ctx, `INSERT INTO conversation_sla (tenant_id, conversation_id, metric, scope_episode, queue_key, started_at, budget_seconds, risk_percent, counting, calendar_queue_id, anchor_at, consumed_seconds, deadline_at, risk_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$6,0,$11,$12) ON CONFLICT DO NOTHING`, tenant, conv, m, epoch, qkey, started, b.seconds, b.risk, b.counting, queue, deadline, risk); err != nil {
				return res, err
			}
			if cl, err = e.loadClock(ctx, q, tenant, conv, m, epoch, qkey); err != nil || cl == nil {
				return res, err
			}
		}
		if cl.state != "running" && cl.state != "breached" {
			continue
		}
		bud := slaBudget{seconds: cl.budget, risk: cl.risk, counting: cl.counting} // the clock's OWN numbers
		// satisfied? A completion after the deadline is a LATE completion: it was a breach, and it is recorded as one
		metAt, err := e.slaMetAt(ctx, q, tenant, conv, m, c, cl.started, now)
		if err != nil {
			return res, err
		}
		if metAt != nil {
			if err := e.finishClock(ctx, q, tenant, conv, st, cl, m, *metAt); err != nil {
				return res, err
			}
			continue
		}
		// the conversation moved: keep the time lived, re-plan the rest on the calendar of where it is now
		if cl.state == "running" && cl.calQueue != nil && *cl.calQueue != queue && m != metricQueueWait {
			lived, err := sectorsadapters.Elapsed(ctx, q, tenant, *cl.calQueue, cl.anchor, now, bud.counting == "business")
			if err != nil {
				return res, err
			}
			consumed := time.Duration(cl.consumed)*time.Second + lived
			risk, deadline, err := slaTimes(ctx, q, tenant, queue, now, bud, consumed)
			if err != nil {
				return res, err
			}
			if _, err := q.Exec(ctx, `UPDATE conversation_sla SET calendar_queue_id=$2, anchor_at=$3, consumed_seconds=$4, deadline_at=$5, risk_at=$6, updated_at=now() WHERE id=$1`,
				cl.id, queue, now, int(consumed/time.Second), deadline, risk); err != nil {
				return res, err
			}
			cl.riskAt, cl.deadline = risk, deadline
		}
		switch {
		case cl.state == "running" && !now.Before(cl.deadline):
			if err := e.breachClock(ctx, q, tenant, conv, st, cl, m, now); err != nil {
				return res, err
			}
			res.triggers = appendUnique(res.triggers, overflow.SLABreach)
		case cl.state == "breached":
			res.triggers = appendUnique(res.triggers, overflow.SLABreach)
		case cl.state == "running" && !now.Before(cl.riskAt):
			if cl.riskNotified == nil {
				if _, err := q.Exec(ctx, `UPDATE conversation_sla SET risk_notified_at=$2, updated_at=now() WHERE id=$1`, cl.id, now); err != nil {
					return res, err
				}
				if err := e.slaEvent(ctx, q, tenant, conv, st, slaEvent{m, "sla_risk"}); err != nil {
					return res, err
				}
			}
			res.triggers = appendUnique(res.triggers, overflow.SLARisk)
			if err := planTimer(ctx, q, tenant, conv, st.episode, "sla_breach", slaStepIndex[m], cl.deadline); err != nil {
				return res, err
			}
		default: // running, before the risk point: wake up at the risk point and at the deadline
			if err := planTimer(ctx, q, tenant, conv, st.episode, "sla_risk", slaStepIndex[m], cl.riskAt); err != nil {
				return res, err
			}
			if err := planTimer(ctx, q, tenant, conv, st.episode, "sla_breach", slaStepIndex[m], cl.deadline); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

type clockRow struct {
	id           uuid.UUID
	state        string
	calQueue     *uuid.UUID
	started      time.Time
	anchor       time.Time
	consumed     int
	budget       int
	risk         int
	counting     string
	riskAt       time.Time
	deadline     time.Time
	riskNotified *time.Time
}

func (e *Engine) loadClock(ctx context.Context, q platformdb.Querier, tenant, conv uuid.UUID, metric string, epoch int, qkey uuid.UUID) (*clockRow, error) {
	var c clockRow
	err := q.QueryRow(ctx, `SELECT id, state, calendar_queue_id, started_at, anchor_at, consumed_seconds, budget_seconds, risk_percent, counting, risk_at, deadline_at, risk_notified_at
		FROM conversation_sla WHERE tenant_id=$1 AND conversation_id=$2 AND metric=$3 AND scope_episode=$4 AND queue_key=$5 FOR UPDATE`, tenant, conv, metric, epoch, qkey).
		Scan(&c.id, &c.state, &c.calQueue, &c.started, &c.anchor, &c.consumed, &c.budget, &c.risk, &c.counting, &c.riskAt, &c.deadline, &c.riskNotified)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// breachClock flips a running clock to breached (once), reporting the risk it went through if that was never reported.
func (e *Engine) breachClock(ctx context.Context, q platformdb.Querier, tenant, conv uuid.UUID, st *stateRow, cl *clockRow, metric string, at time.Time) error {
	tag, err := q.Exec(ctx, `UPDATE conversation_sla SET state='breached', breached_at=$2, updated_at=now() WHERE id=$1 AND state='running'`, cl.id, at)
	if err != nil || tag.RowsAffected() != 1 {
		return err
	}
	cl.state = "breached"
	if cl.riskNotified == nil {
		if _, err := q.Exec(ctx, `UPDATE conversation_sla SET risk_notified_at=$2 WHERE id=$1`, cl.id, at); err != nil {
			return err
		}
		if err := e.slaEvent(ctx, q, tenant, conv, st, slaEvent{metric, "sla_risk"}); err != nil {
			return err
		}
	}
	if err := e.slaEvent(ctx, q, tenant, conv, st, slaEvent{metric, "sla_breach"}); err != nil {
		return err
	}
	return cancelSLATimers(ctx, q, tenant, conv, metric)
}

// finishClock records the completion of a clock. Completed before the deadline = met; completed AFTER it = it was breached (reported if
// it never was) and stays breached, with the real completion time.
func (e *Engine) finishClock(ctx context.Context, q platformdb.Querier, tenant, conv uuid.UUID, st *stateRow, cl *clockRow, metric string, at time.Time) error {
	if cl.state == "running" && at.After(cl.deadline) {
		if err := e.breachClock(ctx, q, tenant, conv, st, cl, metric, cl.deadline); err != nil {
			return err
		}
	}
	if _, err := q.Exec(ctx, `UPDATE conversation_sla SET state=CASE WHEN state='breached' THEN 'breached' ELSE 'met' END, met_at=$2, updated_at=now() WHERE id=$1`, cl.id, at); err != nil {
		return err
	}
	return cancelSLATimers(ctx, q, tenant, conv, metric)
}

// closeSLA: a closed conversation finishes the resolution clock AT the closing instant (late = breached) and stops the others (a clock that
// was already past its deadline is breached first); every timer of the conversation ends.
func (e *Engine) closeSLA(ctx context.Context, q platformdb.Querier, tenant, conv uuid.UUID, c convRow, st *stateRow, now time.Time) error {
	closedAt := now
	if c.closedAt != nil {
		closedAt = *c.closedAt
	}
	rows, err := q.Query(ctx, `SELECT metric, scope_episode, queue_key FROM conversation_sla WHERE tenant_id=$1 AND conversation_id=$2 AND state='running' ORDER BY id FOR UPDATE`, tenant, conv)
	if err != nil {
		return err
	}
	type key struct {
		metric string
		epoch  int
		qkey   uuid.UUID
	}
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.metric, &k.epoch, &k.qkey); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	for _, k := range keys {
		cl, err := e.loadClock(ctx, q, tenant, conv, k.metric, k.epoch, k.qkey)
		if err != nil || cl == nil {
			return err
		}
		switch {
		case k.metric == metricResolution:
			if err := e.finishClock(ctx, q, tenant, conv, st, cl, k.metric, closedAt); err != nil {
				return err
			}
		case !closedAt.Before(cl.deadline): // it was already late when the conversation ended
			if err := e.breachClock(ctx, q, tenant, conv, st, cl, k.metric, cl.deadline); err != nil {
				return err
			}
		default:
			if _, err := q.Exec(ctx, `UPDATE conversation_sla SET state='cancelled', updated_at=now() WHERE id=$1`, cl.id); err != nil {
				return err
			}
		}
	}
	_, err = q.Exec(ctx, `UPDATE routing_engine_timers SET state='cancelled', claim_token=NULL, updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND state IN ('pending','leased')`, tenant, conv)
	return err
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
		if c.assigned != nil || c.held {
			t := now
			if c.assignedAt != nil {
				t = *c.assignedAt
			}
			return &t, nil
		}
	case metricFirstResponse: // the first message a PERSON sent after the clock started (automatic messages do not count)
		var at *time.Time
		if err := q.QueryRow(ctx, `SELECT min(created_at) FROM messages WHERE tenant_id=$1 AND conversation_id=$2 AND direction='outbound' AND (sent_by_user_id IS NOT NULL OR sent_by_installation_id IS NOT NULL) AND status IN ('sent','delivered','read') AND created_at >= $3`,
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
	eventType := string(outboxdomain.EventRoutingSLAAtRisk)
	if ev.kind == "sla_breach" {
		eventType = string(outboxdomain.EventRoutingSLABreached)
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
