package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/engine"
	"github.com/omnira/omnira/internal/routing/overflow"
)

// fakeStarter stands in for the Flow Builder's engine: it answers what it is told to and records every request.
type fakeStarter struct {
	pool    *pgxpool.Pool
	mu      sync.Mutex
	outcome string
	sqlFail bool
	calls   []struct {
		conv, flow uuid.UUID
		key        string
	}
}

func (f *fakeStarter) StartRouted(ctx context.Context, conv, flow uuid.UUID, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct {
		conv, flow uuid.UUID
		key        string
	}{conv, flow, key})
	if f.sqlFail { // a REAL database error inside the decision's transaction: only the savepoint keeps the decision alive
		_, err := platformdb.QuerierFromContext(ctx, f.pool).Exec(ctx, `SELECT 1/0`)
		return "", err
	}
	return f.outcome, nil
}

// closed makes the queue closed right now (an emergency suspension) so the after_hours trigger blocks it.
func (e *env) closedQueue(t *testing.T, tenant uuid.UUID, name string) uuid.UUID {
	t.Helper()
	q := e.queue(t, tenant, name, "round_robin")
	cal := e.calendar(t, tenant, "cal-"+name, "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, cal)
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at, reason) VALUES ($1,$2,now()-interval '1 hour',now()+interval '3 hours','teste')`, tenant, cal)
	return q
}

func TestFlowDestinationStartsTheFlowKeepsTheConversationAndLetsTheChainContinue(t *testing.T) {
	e := newEnv(t, nil)
	starter := &fakeStarter{outcome: "started"}
	starter.pool = e.app
	e.eng.WithFlowStarter(starter)
	a := e.tenant(t)
	fin := e.closedQueue(t, a, "Financeiro")
	next := e.queue(t, a, "Suporte N2", "manual")
	flow := uuid.New()
	e.policy(t, a, fin, 4, overflow.ExhaustedManual,
		overflow.Step{Triggers: []overflow.Trigger{overflow.AfterHours}, DestKind: overflow.DestFlow, DestID: flow},
		overflow.Step{Triggers: []overflow.Trigger{overflow.AfterHours}, WaitSeconds: 3600, DestKind: overflow.DestQueue, DestID: next})
	c := e.conversation(t, a, &fin)
	d, err := e.decide(t, a, c, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(starter.calls) != 1 || starter.calls[0].flow != flow || starter.calls[0].conv != c || !strings.HasPrefix(starter.calls[0].key, "routing-flow:"+c.String()+":1:1") {
		t.Fatalf("one start request, keyed by conversation, episode and step: %+v", starter.calls)
	}
	if e.queueOf(t, c) != fin {
		t.Fatal("a flow does not move the conversation: it stays in its group while the flow talks to the customer")
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE conversation_id=$1 AND decision='EXECUTE_FLOW' AND destination=$2`, c, overflow.DestKey(overflow.DestFlow, flow)) != 1 {
		t.Fatal("the flow start is in the history")
	}
	_, step, hops, _, visited := e.stateOf(t, c)
	if step != 1 || hops != 1 || len(visited) != 2 {
		t.Fatalf("the flow counts as a step against the loop guard: step=%d hops=%d visited=%v", step, hops, visited)
	}
	// the chain kept looking: step 2 is waiting for its hour, so the decision of the call is the plain "waiting for the opening" and a timer exists
	if d.Kind != engine.WaitUntilOpen {
		t.Fatalf("after the flow started the conversation still waits for the opening: %+v", d)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='step_wait' AND step_index=2 AND state='pending'`, c) != 1 {
		t.Fatal("the next step's wait is planned")
	}
	// asking again never starts the flow twice in the episode
	if _, err := e.decide(t, a, c, ""); err != nil || len(starter.calls) != 1 {
		t.Fatalf("the flow is a visited step: %d calls, %v", len(starter.calls), err)
	}
}

func TestFlowDestinationBusyUnavailableAndFailingAreRejectedAndTheChainMovesOn(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, reason string
		fail                  bool
	}{
		{"a flow already runs", "busy", "flow_busy", false},
		{"the flow is gone", "unavailable", "flow_unavailable", false},
		{"the flow errors inside the transaction", "", "flow_error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			starter := &fakeStarter{outcome: tc.outcome, sqlFail: tc.fail}
			starter.pool = e.app
			e.eng.WithFlowStarter(starter)
			a := e.tenant(t)
			fin := e.closedQueue(t, a, "Financeiro")
			next := e.queue(t, a, "Suporte N2", "manual")
			e.policy(t, a, fin, 4, overflow.ExhaustedManual,
				overflow.Step{Triggers: []overflow.Trigger{overflow.AfterHours}, DestKind: overflow.DestFlow, DestID: uuid.New()},
				overflow.Step{Triggers: []overflow.Trigger{overflow.AfterHours}, DestKind: overflow.DestQueue, DestID: next, AllowClosed: false})
			c := e.conversation(t, a, &fin)
			d, err := e.decide(t, a, c, "")
			if err != nil {
				t.Fatalf("a refused or failing flow must never fail the routing: %v", err)
			}
			if d.Kind == "" || e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE conversation_id=$1 AND decision='EXECUTE_FLOW'`, c) != 0 {
				t.Fatalf("no flow start recorded: %+v", d)
			}
			var detail string
			_ = e.seed.QueryRow(e.ctx, `SELECT COALESCE(string_agg(detail::text, ' '), '') FROM routing_engine_decisions WHERE conversation_id=$1`, c).Scan(&detail)
			if !strings.Contains(detail, tc.reason) {
				t.Fatalf("the history says why the flow step was skipped (%s): %s", tc.reason, detail)
			}
			if e.count(t, `SELECT count(*) FROM routing_engine_state WHERE conversation_id=$1`, c) != 1 {
				t.Fatal("the transaction survived and the state was recorded")
			}
		})
	}
}

func TestFlowDestinationWithoutAFlowStarterIsRefusedNotCrashed(t *testing.T) {
	e := newEnv(t, nil) // no starter wired (flows disabled in this worker)
	a := e.tenant(t)
	fin := e.closedQueue(t, a, "Financeiro")
	e.policy(t, a, fin, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.AfterHours}, DestKind: overflow.DestFlow, DestID: uuid.New()})
	c := e.conversation(t, a, &fin)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind == engine.ExecuteFlow {
		t.Fatalf("flows_unavailable must be a refusal, not a start: %+v %v", d, err)
	}
	var detail string
	_ = e.seed.QueryRow(e.ctx, `SELECT COALESCE(string_agg(detail::text, ' '), '') FROM routing_engine_decisions WHERE conversation_id=$1`, c).Scan(&detail)
	if !strings.Contains(detail, "flows_unavailable") {
		t.Fatalf("the history names the reason: %s", detail)
	}
	_ = errors.New
}

// The standard sectors are MANUAL queues (agents take the work). The schedule must still govern them: out of hours the customer is told once,
// the after_hours step of the chain can move the conversation, the SLA clocks run, and at the opening the conversation simply waits for a person.
func TestManualQueuesFollowTheirScheduleNoticeChainAndOpening(t *testing.T) {
	e := newEnv(t, nil)
	n := &recNotifier{pool: e.app}
	e.eng.WithNotifier(n, t.Logf)
	a := e.tenant(t)
	fin := e.queue(t, a, "Financeiro", "manual")
	cal := e.calendar(t, a, "24x7", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2, closed_notice='Fora do expediente. Voltamos {{proxima_abertura}}.' WHERE id=$1`, fin, cal)
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at, reason) VALUES ($1,$2,now()-interval '1 hour',now()+interval '2 hours','teste')`, a, cal)
	c := e.conversation(t, a, &fin)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.WaitUntilOpen || d.Reason != engine.ReasonSectorClosed {
		t.Fatalf("a manual queue out of hours waits for the opening: %+v %v", d, err)
	}
	if len(n.sent) != 1 || !strings.HasPrefix(n.sent[0].text, "Fora do expediente. Voltamos ") {
		t.Fatalf("the customer is told once: %+v", n.sent)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='open_wakeup' AND state='pending'`, c) != 1 {
		t.Fatal("the opening is a durable timer")
	}
	// the suspension ends: at the opening the conversation is just queued for a person (nobody is assigned in a manual queue)
	e.exec(t, `DELETE FROM calendar_suspensions WHERE calendar_id=$1`, cal)
	d, err = e.decide(t, a, c, engine.TriggerOpenWakeup)
	if err != nil || d.Kind != engine.Enqueue || d.Reason != engine.ReasonManualQueue || e.owner(t, c) != nil {
		t.Fatalf("open and manual: queued, never auto-assigned: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_state WHERE conversation_id=$1 AND state='queued'`, c) != 1 {
		t.Fatal("state is queued")
	}

	// the chain applies too: after_hours moves a conversation of a closed manual queue to another group
	next := e.queue(t, a, "Plantão geral", "manual")
	closedQ := e.closedQueue(t, a, "Comercial")
	e.policy(t, a, closedQ, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.AfterHours}, DestKind: overflow.DestQueue, DestID: next})
	c2 := e.conversation(t, a, &closedQ)
	if _, err := e.decide(t, a, c2, ""); err != nil {
		t.Fatal(err)
	}
	if e.queueOf(t, c2) != next {
		t.Fatal("the after_hours step of the chain moved the conversation of a closed MANUAL queue")
	}
}
