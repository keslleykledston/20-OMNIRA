package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/engine"
	"github.com/omnira/omnira/internal/routing/overflow"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
)

// slaPolicy inserts a policy straight into the database (the API has its own tests). Zero = the metric is not measured.
func (e *env) slaPolicy(t *testing.T, tenant uuid.UUID, kind string, scope *uuid.UUID, counting string, risk, first, queueWait, assignment, resolution int) {
	t.Helper()
	nz := func(v int) any {
		if v == 0 {
			return nil
		}
		return v
	}
	var sector, queue *uuid.UUID
	switch kind {
	case "sector":
		sector = scope
	case "queue":
		queue = scope
	}
	e.exec(t, `INSERT INTO sla_policies (tenant_id, scope_kind, sector_id, queue_id, first_response_seconds, queue_wait_seconds, assignment_seconds, resolution_seconds, risk_percent, counting)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, tenant, kind, sector, queue, nz(first), nz(queueWait), nz(assignment), nz(resolution), risk, counting)
}

type clock struct {
	state    string
	budget   int
	started  time.Time
	deadline time.Time
	calQueue *uuid.UUID
}

func (e *env) clock(t *testing.T, conv uuid.UUID, metric string) (clock, bool) {
	t.Helper()
	var c clock
	err := e.seed.QueryRow(e.ctx, `SELECT state, budget_seconds, started_at, deadline_at, calendar_queue_id FROM conversation_sla WHERE conversation_id=$1 AND metric=$2 ORDER BY updated_at DESC LIMIT 1`, conv, metric).
		Scan(&c.state, &c.budget, &c.started, &c.deadline, &c.calQueue)
	return c, err == nil
}

// slaShift pretends `ago` has passed for the clocks and timers of a conversation.
func (e *env) slaShift(t *testing.T, conv uuid.UUID, ago time.Duration) {
	t.Helper()
	e.exec(t, `UPDATE conversation_sla SET started_at=started_at - make_interval(secs => $2), anchor_at=anchor_at - make_interval(secs => $2), risk_at=risk_at - make_interval(secs => $2), deadline_at=deadline_at - make_interval(secs => $2) WHERE conversation_id=$1`, conv, ago.Seconds())
	e.exec(t, `UPDATE routing_engine_timers SET due_at=due_at - make_interval(secs => $2) WHERE conversation_id=$1 AND kind IN ('sla_risk','sla_breach')`, conv, ago.Seconds())
	e.exec(t, `UPDATE conversations SET created_at=created_at - make_interval(secs => $2) WHERE id=$1`, conv, ago.Seconds())
}

func TestSLAPolicyPrecedenceIsQueueThenSectorThenTenantMetricByMetric(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	sector := uuid.New()
	e.exec(t, `INSERT INTO sectors (id, tenant_id, name) VALUES ($1,$2,'Setor')`, sector, a)
	e.exec(t, `UPDATE queues SET sector_id=$2 WHERE id=$1`, q, sector)
	e.slaPolicy(t, a, "tenant", nil, "wall", 80, 3600, 3600, 3600, 86400)
	e.slaPolicy(t, a, "sector", &sector, "wall", 70, 300, 0, 0, 0)
	e.slaPolicy(t, a, "queue", &q, "wall", 60, 0, 0, 120, 0)
	c := e.conversation(t, a, &q) // no agent: stays queued, all clocks running
	e.decide(t, a, c, "")
	for metric, want := range map[string]int{"first_response": 300, "assignment": 120, "queue_wait": 3600, "resolution": 86400} {
		got, ok := e.clock(t, c, metric)
		if !ok || got.budget != want {
			t.Errorf("%s budget = %d (found=%v), want %d", metric, got.budget, ok, want)
		}
	}
	// a deactivated policy is ignored
	e.exec(t, `UPDATE sla_policies SET active=false WHERE scope_kind='queue'`)
	c2 := e.conversation(t, a, &q)
	e.decide(t, a, c2, "")
	if got, _ := e.clock(t, c2, "assignment"); got.budget != 3600 {
		t.Errorf("without the queue policy the tenant's 3600 applies, got %d", got.budget)
	}
}

func TestAssignmentMeetsTheQueueClocksAndAHumanReplyMeetsTheFirstResponse(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	u, _ := e.attendant(t, a, &q, 3)
	e.slaPolicy(t, a, "tenant", nil, "wall", 80, 600, 600, 600, 7200)
	c := e.conversation(t, a, &q)
	if d, _ := e.decide(t, a, c, ""); d.AssignedUser != u {
		t.Fatalf("%+v", d)
	}
	for _, m := range []string{"assignment", "queue_wait"} {
		if got, _ := e.clock(t, c, m); got.state != "met" {
			t.Errorf("%s = %s, want met when it is assigned", m, got.state)
		}
	}
	if got, _ := e.clock(t, c, "first_response"); got.state != "running" {
		t.Fatalf("nobody has answered yet: %s", got.state)
	}
	// an AUTOMATIC message (no person behind it) is not a first response
	e.exec(t, `INSERT INTO messages (tenant_id, conversation_id, direction, body, status, sent_by_user_id) VALUES ($1,$2,'outbound','automatico','sent',NULL)`, a, c)
	e.decide(t, a, c, "")
	if got, _ := e.clock(t, c, "first_response"); got.state != "running" {
		t.Fatalf("an automatic message must not meet the first response: %s", got.state)
	}
	e.exec(t, `INSERT INTO messages (tenant_id, conversation_id, direction, body, status, sent_by_user_id) VALUES ($1,$2,'outbound','oi, sou a Ana','sent',$3)`, a, c, u)
	e.decide(t, a, c, "")
	if got, _ := e.clock(t, c, "first_response"); got.state != "met" {
		t.Fatalf("a person answered: %s", got.state)
	}
	// closing the conversation meets the resolution clock and leaves no live timer
	e.exec(t, `UPDATE conversations SET status='closed' WHERE id=$1`, c)
	e.decide(t, a, c, "")
	if got, _ := e.clock(t, c, "resolution"); got.state != "met" {
		t.Fatalf("resolution: %s", got.state)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND state IN ('pending','leased')`, c) != 0 {
		t.Fatal("no timer outlives the conversation")
	}
}

// Risco e violação: cada um acontece UMA vez, com evento e histórico; repetir não duplica.
func TestRiskAndBreachFireOnceWithAnEventAndAnAuditTrail(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	e.attendant(t, a, &q, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, q, e.conversation(t, a, &q))
	e.slaPolicy(t, a, "tenant", nil, "wall", 80, 0, 0, 600, 0)
	c := e.conversation(t, a, &q)
	e.decide(t, a, c, "")
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='sla_risk' AND state='pending'`, c) != 1 || e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='sla_breach' AND state='pending'`, c) != 1 {
		t.Fatal("a risk timer and a breach timer are planned")
	}
	events := func(kind string) int {
		return e.count(t, `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type=$3`, a, c.String(), kind)
	}
	e.slaShift(t, c, 9*time.Minute) // 9 of 10 minutes: past the 80% risk point, not yet the deadline
	if n, err := e.eng.RunTimersOnce(e.ctx); err != nil || n < 1 {
		t.Fatalf("the risk timer runs: %d %v", n, err)
	}
	if events("job.routing.sla_at_risk.v1") != 1 || events("job.routing.sla_breached.v1") != 0 {
		t.Fatalf("one risk event, no breach yet: %d %d", events("job.routing.sla_at_risk.v1"), events("job.routing.sla_breached.v1"))
	}
	e.decide(t, a, c, "") // asking again changes nothing
	e.decide(t, a, c, "")
	if events("job.routing.sla_at_risk.v1") != 1 {
		t.Fatal("the risk is reported once")
	}
	e.slaShift(t, c, 2*time.Minute) // now past the deadline
	e.eng.RunTimersOnce(e.ctx)
	e.decide(t, a, c, "")
	got, _ := e.clock(t, c, "assignment")
	if got.state != "breached" || events("job.routing.sla_breached.v1") != 1 || events("job.routing.sla_at_risk.v1") != 1 {
		t.Fatalf("breach: state=%s breached=%d risk=%d", got.state, events("job.routing.sla_breached.v1"), events("job.routing.sla_at_risk.v1"))
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE conversation_id=$1 AND decision='ESCALATE' AND reason IN ('sla_risk','sla_breach')`, c) != 2 {
		t.Fatal("both crossings are in the decision history, once each")
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind IN ('sla_risk','sla_breach') AND state IN ('pending','leased')`, c) != 0 {
		t.Fatal("a breached clock plans nothing more")
	}
	var payload string
	_ = e.seed.QueryRow(e.ctx, `SELECT payload::text FROM outbox_events WHERE aggregate_id=$1 AND event_type='job.routing.sla_breached.v1'`, c.String()).Scan(&payload)
	if payload != `{"metric": "assignment"}` {
		t.Fatalf("the event carries the metric and nothing else: %s", payload)
	}
}

// Uma violação alimenta a cadeia (gatilho sla_breach) quando a conversa está sem dono; com dono, só avisa — nunca reatribui sozinha.
func TestABreachMovesAnUnassignedConversationThroughTheChainButNeverReassignsAnOwnedOne(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	u2, _ := e.attendant(t, a, &n2, 5)
	e.slaPolicy(t, a, "tenant", nil, "wall", 80, 0, 0, 600, 0)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.SLABreach}, DestKind: overflow.DestQueue, DestID: n2})
	c := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c, ""); d.Kind != engine.Enqueue || e.queueOf(t, c) != n1 {
		t.Fatalf("full queue, no breach yet: %+v", d)
	}
	e.slaShift(t, c, 11*time.Minute)
	d, err := e.decide(t, a, c, engine.TriggerStepWait)
	if err != nil || d.AssignedUser != u2 || e.queueOf(t, c) != n2 {
		t.Fatalf("the breach sends it to N2 where someone takes it: %+v %v", d, err)
	}
	if got, _ := e.clock(t, c, "assignment"); got.state != "breached" {
		t.Fatalf("the breach stays on record even though it was then assigned: %s", got.state)
	}
	// an OWNED conversation whose first response breaches: the supervisor is told, nobody is reassigned
	owner, _ := e.attendant(t, a, &n2, 5)
	e.slaPolicy(t, a, "queue", &n2, "wall", 80, 300, 0, 0, 0)
	c2 := e.conversation(t, a, &n2)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, c2, owner)
	e.decide(t, a, c2, "")
	e.slaShift(t, c2, 10*time.Minute)
	e.eng.RunTimersOnce(e.ctx)
	e.decide(t, a, c2, "")
	if o := e.owner(t, c2); o == nil || *o != owner {
		t.Fatal("an SLA never reassigns an owned conversation")
	}
	if e.count(t, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='job.routing.sla_breached.v1'`, c2.String()) != 1 {
		t.Fatal("but the breach is announced")
	}
}

// Transferir não esconde atraso: o tempo já vivido fica; só o que resta é replanejado, no calendário de onde a conversa está agora.
func TestATransferKeepsTheTimeAlreadyLivedAndReplansTheRestOnTheDestinationCalendar(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	e.attendant(t, a, &n2, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n2, e.conversation(t, a, &n2))
	e.slaPolicy(t, a, "tenant", nil, "wall", 80, 3600, 3600, 0, 0)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestQueue, DestID: n2})
	// ... but make the step wait so the conversation first lives some time in N1
	e.exec(t, `UPDATE overflow_steps SET wait_seconds=60`)
	c := e.conversation(t, a, &n1)
	e.decide(t, a, c, "")
	before, _ := e.clock(t, c, "first_response")
	e.slaShift(t, c, 30*time.Minute) // 30 minutes lived in N1 (the first-response clock and the conversation's age)
	e.timeTravel(t, c, 2*time.Minute)
	if d, _ := e.decide(t, a, c, engine.TriggerStepWait); e.queueOf(t, c) != n2 {
		t.Fatalf("moved to N2: %+v", d)
	}
	after, _ := e.clock(t, c, "first_response")
	wantDeadline := before.started.Add(-30 * time.Minute).Add(time.Hour)
	if diff := after.deadline.Sub(wantDeadline); diff < -2*time.Second || diff > 2*time.Second {
		t.Fatalf("the first-response deadline must stay at the ORIGINAL start + 1h (%v), got %v", wantDeadline, after.deadline)
	}
	if after.calQueue == nil || *after.calQueue != n2 {
		t.Fatalf("the clock now counts on the destination's calendar: %v", after.calQueue)
	}
	// queue_wait is the wait in the CURRENT queue: a fresh clock for N2 (the N1 one is history)
	if e.count(t, `SELECT count(*) FROM conversation_sla WHERE conversation_id=$1 AND metric='queue_wait'`, c) != 2 || e.count(t, `SELECT count(*) FROM conversation_sla WHERE conversation_id=$1 AND metric='queue_wait' AND state='cancelled'`, c) != 1 {
		t.Fatal("queue_wait: the clock of the queue it left is cancelled, a new one runs for N2")
	}
}

func TestBusinessCountingKeepsTheLivedTimeAndSkipsTheDestinationsClosedHours(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	e.attendant(t, a, &n2, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n2, e.conversation(t, a, &n2))
	open24 := e.calendar(t, a, "aberto", "24x7", "[]")
	suspended := e.calendar(t, a, "suspenso", "24x7", "[]")
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '1 minute',now()+interval '2 hours')`, a, suspended)
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, n1, open24)
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, n2, suspended)
	e.slaPolicy(t, a, "tenant", nil, "business", 80, 3600, 0, 0, 0)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, WaitSeconds: 60, DestKind: overflow.DestQueue, DestID: n2, AllowClosed: true})
	c := e.conversation(t, a, &n1)
	e.decide(t, a, c, "")
	e.slaShift(t, c, 30*time.Minute) // 30 of 60 business minutes lived in N1 (open all day)
	e.timeTravel(t, c, 2*time.Minute)
	e.decide(t, a, c, engine.TriggerStepWait)
	if e.queueOf(t, c) != n2 {
		t.Fatal("moved into the suspended sector (the step allows a closed destination)")
	}
	got, _ := e.clock(t, c, "first_response")
	// 30 business minutes are left, but N2 is closed for the next ~2h: the deadline is ~2h30 from now, not 30 minutes
	if d := time.Until(got.deadline); d < 2*time.Hour+20*time.Minute || d > 2*time.Hour+40*time.Minute {
		t.Fatalf("remaining business time counted on the destination calendar: deadline in %v", d)
	}
}

// Codex: concluir DEPOIS do prazo é violação (não "met"); fechar também.
func TestLateCompletionIsABreachAndKeepsItsRealCompletionTime(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "manual")
	u, _ := e.attendant(t, a, &q, 3)
	e.slaPolicy(t, a, "tenant", nil, "wall", 80, 0, 0, 600, 600)
	events := func(conv uuid.UUID, kind string) int { // the assignment clock's events only (the resolution clock has its own)
		return e.count(t, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type=$2 AND payload->>'metric'='assignment'`, conv.String(), kind)
	}
	// assigned by a person AFTER the 10-minute deadline, with no timer having run in between
	c := e.conversation(t, a, &q)
	e.decide(t, a, c, "")
	e.slaShift(t, c, 11*time.Minute)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=$2, assigned_at=now() WHERE id=$1`, c, u)
	e.decide(t, a, c, "")
	var state string
	var metAt *time.Time
	_ = e.seed.QueryRow(e.ctx, `SELECT state, met_at FROM conversation_sla WHERE conversation_id=$1 AND metric='assignment'`, c).Scan(&state, &metAt)
	if state != "breached" || metAt == nil || events(c, "job.routing.sla_breached.v1") != 1 || events(c, "job.routing.sla_at_risk.v1") != 1 {
		t.Fatalf("late assignment: state=%s met=%v breached=%d risk=%d", state, metAt, events(c, "job.routing.sla_breached.v1"), events(c, "job.routing.sla_at_risk.v1"))
	}
	// closed late: the resolution clock is breached with the closing time; closed in time: met
	e.exec(t, `UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, c)
	e.decide(t, a, c, "")
	_ = e.seed.QueryRow(e.ctx, `SELECT state FROM conversation_sla WHERE conversation_id=$1 AND metric='resolution'`, c).Scan(&state)
	if state != "breached" {
		t.Fatalf("resolution closed after the deadline: %s", state)
	}
	c2 := e.conversation(t, a, &q)
	e.decide(t, a, c2, "")
	e.exec(t, `UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, c2)
	e.decide(t, a, c2, "")
	_ = e.seed.QueryRow(e.ctx, `SELECT state FROM conversation_sla WHERE conversation_id=$1 AND metric='resolution'`, c2).Scan(&state)
	if state != "met" {
		t.Fatalf("resolution closed in time: %s", state)
	}
	// a clock that was already past its deadline when the conversation ended is breached, not silently cancelled
	c3 := e.conversation(t, a, &q)
	e.decide(t, a, c3, "")
	e.slaShift(t, c3, 11*time.Minute)
	e.exec(t, `UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, c3)
	e.decide(t, a, c3, "")
	_ = e.seed.QueryRow(e.ctx, `SELECT state FROM conversation_sla WHERE conversation_id=$1 AND metric='assignment'`, c3).Scan(&state)
	if state != "breached" || events(c3, "job.routing.sla_breached.v1") < 1 {
		t.Fatalf("an unassigned clock past its deadline when the conversation closed: %s", state)
	}
}

func TestClosingEndsEveryTimerAndAFailedHumanSendDoesNotMeetTheFirstResponse(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	u, _ := e.attendant(t, a, &q, 3)
	e.slaPolicy(t, a, "tenant", nil, "wall", 80, 600, 0, 0, 0)
	closed := e.calendar(t, a, "fechado", "24x7", "[]")
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '1 hour',now()+interval '3 hours')`, a, closed)
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, closed)
	c := e.conversation(t, a, &q)
	e.decide(t, a, c, "")
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND state='pending'`, c) < 2 {
		t.Fatal("an opening timer and the SLA timers exist")
	}
	e.exec(t, `UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, c)
	e.decide(t, a, c, "")
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND state IN ('pending','leased')`, c) != 0 {
		t.Fatal("a closed conversation keeps no timer of any kind")
	}
	// a human send that FAILED is not a response
	open := e.queue(t, a, "Aberta", "round_robin")
	e.attendant(t, a, &open, 3)
	c2 := e.conversation(t, a, &open)
	e.decide(t, a, c2, "")
	e.exec(t, `INSERT INTO messages (tenant_id, conversation_id, direction, body, status, sent_by_user_id) VALUES ($1,$2,'outbound','x','failed',$3)`, a, c2, u)
	e.exec(t, `INSERT INTO messages (tenant_id, conversation_id, direction, body, status, sent_by_user_id) VALUES ($1,$2,'outbound','x','queued',$3)`, a, c2, u)
	e.decide(t, a, c2, "")
	if got, _ := e.clock(t, c2, "first_response"); got.state != "running" {
		t.Fatalf("failed/queued sends do not count: %s", got.state)
	}
	e.exec(t, `INSERT INTO messages (tenant_id, conversation_id, direction, body, status, sent_by_user_id) VALUES ($1,$2,'outbound','ok','delivered',$3)`, a, c2, u)
	e.decide(t, a, c2, "")
	if got, _ := e.clock(t, c2, "first_response"); got.state != "met" {
		t.Fatalf("a delivered send does: %s", got.state)
	}
}

// Codex: o relógio carrega os próprios números; editar a política depois não muda um relógio em curso, nem numa transferência.
func TestEditingThePolicyNeverChangesARunningClockEvenAcrossATransfer(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	e.attendant(t, a, &n2, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n2, e.conversation(t, a, &n2))
	e.slaPolicy(t, a, "tenant", nil, "wall", 80, 3600, 0, 0, 0)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, WaitSeconds: 60, DestKind: overflow.DestQueue, DestID: n2})
	c := e.conversation(t, a, &n1)
	e.decide(t, a, c, "")
	e.exec(t, `UPDATE sla_policies SET counting='business', risk_percent=10, first_response_seconds=60`) // an administrator edits the policy meanwhile
	e.timeTravel(t, c, 2*time.Minute)
	e.decide(t, a, c, engine.TriggerStepWait)
	var counting string
	var risk, budget int
	var riskAt, deadline time.Time
	if err := e.seed.QueryRow(e.ctx, `SELECT counting, risk_percent, budget_seconds, risk_at, deadline_at FROM conversation_sla WHERE conversation_id=$1 AND metric='first_response'`, c).Scan(&counting, &risk, &budget, &riskAt, &deadline); err != nil {
		t.Fatal(err)
	}
	if counting != "wall" || risk != 80 || budget != 3600 {
		t.Fatalf("the clock keeps ITS numbers: %s %d%% %ds", counting, risk, budget)
	}
	if left, riskLeft := time.Until(deadline), time.Until(riskAt); left < 55*time.Minute || riskLeft < 45*time.Minute || riskLeft > 50*time.Minute {
		t.Fatalf("deadline in %v, risk in %v (80%% of 1h)", left, riskLeft)
	}
}

// Codex: mudar o calendário replaneja também os relógios em horário útil que seguem na mesma fila.
func TestACalendarChangeReplansRunningBusinessClocksAndTheirTimers(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "manual")
	cal := e.calendar(t, a, "util", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, cal)
	e.slaPolicy(t, a, "tenant", nil, "business", 80, 0, 0, 3600, 0)
	c := e.conversation(t, a, &q)
	e.decide(t, a, c, "")
	before, _ := e.clock(t, c, "assignment")
	// a suspension of the next 2 hours is added: the business hour cannot be consumed meanwhile
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()+interval '1 minute',now()+interval '2 hours')`, a, cal)
	if err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(c2 context.Context) error {
		_, err := sectorsadapters.ReplanWaiting(c2, platformdb.QuerierFromContext(c2, e.app), a)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := e.clock(t, c, "assignment")
	if !after.deadline.After(before.deadline.Add(100 * time.Minute)) {
		t.Fatalf("the deadline moves past the suspension: %v -> %v", before.deadline, after.deadline)
	}
	var due time.Time
	_ = e.seed.QueryRow(e.ctx, `SELECT due_at FROM routing_engine_timers WHERE conversation_id=$1 AND kind='sla_breach' AND state='pending'`, c).Scan(&due)
	if !due.Equal(after.deadline) {
		t.Fatalf("the breach timer follows the clock: %v vs %v", due, after.deadline)
	}
}

func TestLeavingForAQueueWithoutPolicyStopsTheOldQueueWaitClockAndRowsNeverChangeCompany(t *testing.T) {
	e := newEnv(t, nil)
	a, b := e.tenant(t), e.tenant(t)
	n1, n2 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	e.slaPolicy(t, a, "queue", &n1, "wall", 80, 0, 3600, 0, 0) // only N1 has a policy
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestQueue, DestID: n2})
	c := e.conversation(t, a, &n1)
	e.decide(t, a, c, "") // N2 has nobody: the conversation moves there and waits; N2 has no SLA policy at all
	if e.queueOf(t, c) != n2 {
		t.Fatal("moved")
	}
	if got, _ := e.clock(t, c, "queue_wait"); got.state != "cancelled" {
		t.Fatalf("the clock of the queue it left must not keep running: %s", got.state)
	}
	// a row never changes company, whoever writes (the owner role included)
	e.exec(t, `INSERT INTO sla_policies (tenant_id, scope_kind, assignment_seconds) VALUES ($1,'tenant',60)`, a)
	// rows with no composite foreign key to hold them back, so only the trigger can refuse
	if _, err := e.seed.Exec(e.ctx, `UPDATE sla_policies SET tenant_id=$2 WHERE tenant_id=$1 AND scope_kind='tenant'`, a, b); err == nil {
		t.Fatal("moving a policy to another company must be refused")
	}
	cal := e.calendar(t, a, "movel", "24x7", "[]")
	if _, err := e.seed.Exec(e.ctx, `UPDATE calendars SET tenant_id=$2 WHERE id=$1`, cal, b); err == nil {
		t.Fatal("moving a calendar to another company must be refused")
	}
}
