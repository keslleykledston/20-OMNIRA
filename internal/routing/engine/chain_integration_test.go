package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/engine"
	"github.com/omnira/omnira/internal/routing/overflow"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
)

// policy inserts an overflow policy for an origin queue straight into the database (the API has its own tests).
func (e *env) policy(t *testing.T, tenant, origin uuid.UUID, maxSteps int, onExhausted string, steps ...overflow.Step) {
	t.Helper()
	var id uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `INSERT INTO overflow_policies (tenant_id, queue_id, max_steps, on_exhausted) VALUES ($1,$2,$3,$4) RETURNING id`, tenant, origin, maxSteps, onExhausted).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for i, s := range steps {
		trig := make([]string, len(s.Triggers))
		for j, x := range s.Triggers {
			trig[j] = string(x)
		}
		e.exec(t, `INSERT INTO overflow_steps (tenant_id, policy_id, position, triggers, wait_seconds, wait_business, destination_kind, destination_id, destination_queue_id, allow_closed, allow_off_shift)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, tenant, id, i+1, trig, s.WaitSeconds, s.WaitBusiness, s.DestKind, s.DestID, s.DestQueueID, s.AllowClosed, s.AllowOffShift)
	}
}

func (e *env) roster(t *testing.T, tenant uuid.UUID, name string, users ...uuid.UUID) uuid.UUID {
	t.Helper()
	// tied to a sector of its own: a roster without a sector would cover EVERY queue of the company and make these people eligible at the
	// origin, which is not what a chain step is about
	id, sector := uuid.New(), uuid.New()
	e.exec(t, `INSERT INTO sectors (id, tenant_id, name) VALUES ($1,$2,$3)`, sector, tenant, "Setor do "+name)
	e.exec(t, `INSERT INTO on_call_rosters (id, tenant_id, name, kind, sector_id) VALUES ($1,$2,$3,'on_call',$4)`, id, tenant, name, sector)
	for _, u := range users {
		e.exec(t, `INSERT INTO on_call_periods (tenant_id, roster_id, user_id, starts_at, ends_at, capacity) VALUES ($1,$2,$3,now()-interval '1 hour',now()+interval '1 hour',2)`, tenant, id, u)
	}
	return id
}

// timeTravel pretends the conversation entered its current step `ago` earlier (the engine counts waits from there).
func (e *env) timeTravel(t *testing.T, conv uuid.UUID, ago time.Duration) {
	t.Helper()
	e.exec(t, `UPDATE routing_engine_state SET step_entered_at = now() - make_interval(secs => $2), entered_queue_at = now() - make_interval(secs => $2) WHERE conversation_id=$1`, conv, ago.Seconds())
}

func (e *env) queueOf(t *testing.T, conv uuid.UUID) uuid.UUID {
	t.Helper()
	var q uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT queue_id FROM conversations WHERE id=$1`, conv).Scan(&q); err != nil {
		t.Fatal(err)
	}
	return q
}

func (e *env) stateOf(t *testing.T, conv uuid.UUID) (state string, step, hops, episode int, visited []string) {
	t.Helper()
	if err := e.seed.QueryRow(e.ctx, `SELECT state, step_index, hops, episode, visited FROM routing_engine_state WHERE conversation_id=$1`, conv).Scan(&state, &step, &hops, &episode, &visited); err != nil {
		t.Fatal(err)
	}
	return
}

// A cadeia do dono: N1 (espera) -> N2 -> plantão -> contingência.
func TestTheOwnersChainN1ThenN2ThenOnCallThenContingency(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "Suporte N1", "round_robin"), e.queue(t, a, "Suporte N2", "round_robin")
	e.attendant(t, a, &n1, 1)
	b, _ := e.attendant(t, a, &n2, 1)
	oncall, _ := e.attendant(t, a, nil, 0)
	roster := e.roster(t, a, "Plantão", oncall)
	e.policy(t, a, n1, 4, overflow.ExhaustedContingency,
		overflow.Step{Triggers: []overflow.Trigger{overflow.NoEligibleAgent, overflow.CapacityExhausted}, WaitSeconds: 300, DestKind: overflow.DestQueue, DestID: n2},
		overflow.Step{Triggers: []overflow.Trigger{overflow.NoEligibleAgent, overflow.CapacityExhausted, overflow.MaxWait}, WaitSeconds: 600, DestKind: overflow.DestOnCall, DestID: roster})
	// N1's only agent and N2's only agent are busy; the on-call person is free
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	holdB := e.conversation(t, a, &n2)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, holdB, b)

	c := e.conversation(t, a, &n1)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.Enqueue || d.Reason != engine.ReasonCapacityExhausted {
		t.Fatalf("N1 is full: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='step_wait' AND step_index=1 AND state='pending' AND due_at > now()+interval '4 minutes' AND due_at < now()+interval '6 minutes'`, c) != 1 {
		t.Fatal("one durable timer at 5 minutes for step 1")
	}
	// 2 minutes: nothing yet
	e.timeTravel(t, c, 2*time.Minute)
	if d, _ := e.decide(t, a, c, engine.TriggerStepWait); d.Kind != engine.Enqueue || e.queueOf(t, c) != n1 {
		t.Fatalf("too early: %+v", d)
	}
	// 6 minutes: overflow to N2, which is also full -> queued there, and the on-call step is now waiting 10 minutes
	e.timeTravel(t, c, 6*time.Minute)
	d, err = e.decide(t, a, c, engine.TriggerStepWait)
	if err != nil || d.Kind != engine.Enqueue || d.Reason != engine.ReasonCapacityExhausted || e.queueOf(t, c) != n2 {
		t.Fatalf("step 1: %+v %v (queue %v)", d, err, e.queueOf(t, c))
	}
	state, step, hops, _, visited := e.stateOf(t, c)
	if state != "queued" || step != 1 || hops != 1 || len(visited) != 2 {
		t.Fatalf("state after step 1: %s step=%d hops=%d visited=%v", state, step, hops, visited)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE conversation_id=$1 AND decision='OVERFLOW' AND destination=$2`, c, overflow.DestKey(overflow.DestQueue, n2)) != 1 {
		t.Fatal("the transition is in the history")
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='step_wait' AND step_index=2 AND state='pending'`, c) != 1 {
		t.Fatal("the next wait is planned")
	}
	// 11 more minutes in N2: on to the on-call person, who takes it
	e.timeTravel(t, c, 11*time.Minute)
	d, err = e.decide(t, a, c, engine.TriggerStepWait)
	if err != nil || d.Kind != engine.AssignAgent || d.Reason != engine.ReasonOverflowOnCall || d.AssignedUser != oncall {
		t.Fatalf("on call: %+v %v", d, err)
	}
	if e.queueOf(t, c) != n2 {
		t.Fatal("assigning to the on-call person does not move the conversation to another queue")
	}
	if e.count(t, `SELECT count(*) FROM assignment_events WHERE conversation_id=$1 AND reason='overflow_oncall'`, c) != 1 || e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND state IN ('pending','leased')`, c) != 0 {
		t.Fatal("one history row, and no timer left alive")
	}
	// a second conversation finds the on-call person busy (capacity 2 is shared: hold two): the chain is exhausted -> contingency
	for i := 0; i < 2; i++ {
		x := e.conversation(t, a, &n2)
		e.exec(t, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, x, oncall)
	}
	c2 := e.conversation(t, a, &n1)
	e.decide(t, a, c2, "")
	e.timeTravel(t, c2, 6*time.Minute)
	e.decide(t, a, c2, engine.TriggerStepWait) // -> N2
	e.timeTravel(t, c2, 11*time.Minute)
	d, err = e.decide(t, a, c2, engine.TriggerStepWait) // on-call person is full: the step is rejected -> exhausted
	if err != nil || d.Kind != engine.Escalate || d.Reason != engine.ReasonChainExhausted {
		t.Fatalf("exhausted chain: %+v %v", d, err)
	}
	if st, _, _, _, _ := e.stateOf(t, c2); st != "manual_required" || e.owner(t, c2) != nil {
		t.Fatalf("contingency keeps the conversation and hands it to a person: state=%s", st)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND state IN ('pending','leased')`, c2) != 0 {
		t.Fatal("no timer survives the end of the chain")
	}
}

// Financeiro fora do expediente: transborda na hora para o Suporte (aberto); sem aceitar destino fechado, o encadeamento se esgota.
func TestAfterHoursOverflowsToTheOpenSectorAndRefusesAClosedDestination(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	fin, sup := e.queue(t, a, "Financeiro", "round_robin"), e.queue(t, a, "Suporte", "round_robin")
	e.attendant(t, a, &fin, 3)
	s, _ := e.attendant(t, a, &sup, 3)
	closed := e.calendar(t, a, "fechado", "24x7", "[]")
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '1 hour',now()+interval '3 hours')`, a, closed)
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, fin, closed)
	e.policy(t, a, fin, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.AfterHours}, DestKind: overflow.DestQueue, DestID: sup})
	c := e.conversation(t, a, &fin)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.AssignAgent || d.AssignedUser != s || e.queueOf(t, c) != sup {
		t.Fatalf("after hours -> Suporte: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND state IN ('pending','leased')`, c) != 0 {
		t.Fatal("the opening timer of the closed origin is cancelled once the conversation has a home")
	}
	// the destination is closed too and the step does not allow that: rejected -> exhausted -> a person
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, sup, closed)
	c2 := e.conversation(t, a, &fin)
	d, err = e.decide(t, a, c2, "")
	if err != nil || d.Kind != engine.RequireManualAction || d.Reason != engine.ReasonChainExhausted || e.queueOf(t, c2) != fin {
		t.Fatalf("closed destination: %+v %v", d, err)
	}
	// ... unless the step explicitly allows a closed destination (contingency): it moves, and waits there for the opening
	e.exec(t, `UPDATE overflow_steps SET allow_closed=true`)
	c3 := e.conversation(t, a, &fin)
	d, err = e.decide(t, a, c3, "")
	if err != nil || d.Kind != engine.AssignAgent || d.AssignedUser != s || e.queueOf(t, c3) != sup {
		t.Fatalf("allow_closed (contingency) routes into the closed sector and its agent takes it: %+v %v (queue %v)", d, err, e.queueOf(t, c3))
	}
}

func TestDestinationsThatBecameUnavailableAreSkippedAndLoopsAreImpossible(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2, n3 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin"), e.queue(t, a, "N3", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.attendant(t, a, &n2, 3)
	three, _ := e.attendant(t, a, &n3, 3)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	e.policy(t, a, n1, 4, overflow.ExhaustedManual,
		overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestQueue, DestID: n2},
		overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestQueue, DestID: n3})
	// N2's sector was deactivated after the policy was saved: the live check skips it and the next step is used
	sec := uuid.New()
	e.exec(t, `INSERT INTO sectors (id, tenant_id, name, active) VALUES ($1,$2,'Inativo',false)`, sec, a)
	e.exec(t, `UPDATE queues SET sector_id=$2 WHERE id=$1`, n2, sec)
	c := e.conversation(t, a, &n1)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.AssignAgent || d.AssignedUser != three || e.queueOf(t, c) != n3 {
		t.Fatalf("N2 is unavailable, N3 takes it: %+v %v", d, err)
	}
	if _, step, hops, _, visited := e.stateOf(t, c); step != 2 || hops != 1 || len(visited) != 2 {
		t.Fatalf("skipping is not a hop: step=%d hops=%d visited=%v", step, hops, visited)
	}
	var rejected string
	if err := e.seed.QueryRow(e.ctx, `SELECT COALESCE(detail->'rejected'->0->>'reason', '') FROM routing_engine_decisions WHERE conversation_id=$1 AND decision='ASSIGN_AGENT'`, c).Scan(&rejected); err != nil || rejected != "destination_unavailable" {
		t.Fatalf("the refused destination and its reason are in the history: %q %v", rejected, err)
	}
	// loop guard: a destination already visited in this episode is never entered again, even if a step names it
	c2 := e.conversation(t, a, &n1)
	e.exec(t, `INSERT INTO routing_engine_state (tenant_id, conversation_id, state, queue_id, origin_queue_id, visited) VALUES ($1,$2,'queued',$3,$3,$4) ON CONFLICT DO NOTHING`,
		a, c2, n1, []string{overflow.DestKey(overflow.DestQueue, n1), overflow.DestKey(overflow.DestQueue, n3)})
	e.exec(t, `UPDATE sectors SET active=true WHERE id=$1`, sec)
	d, err = e.decide(t, a, c2, "")
	if err != nil || e.queueOf(t, c2) != n2 {
		t.Fatalf("N3 was visited, N2 (active again) is next: %+v %v", d, err)
	}
	// hop limit: max_steps 1 allows ONE hop even though two fresh destinations exist
	e.exec(t, `UPDATE overflow_policies SET max_steps=1`)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n2, e.conversation(t, a, &n2))
	e.exec(t, `UPDATE queue_members SET capacity=1 WHERE queue_id IN ($1,$2)`, n2, n3)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n3, e.conversation(t, a, &n3))
	c3 := e.conversation(t, a, &n1)
	d, err = e.decide(t, a, c3, "")
	if err != nil || d.Kind != engine.RequireManualAction || d.Reason != engine.ReasonChainExhausted {
		t.Fatalf("the hop limit ends the chain: %+v %v", d, err)
	}
	if _, _, hops, _, _ := e.stateOf(t, c3); hops != 1 {
		t.Fatalf("exactly one hop: %d", hops)
	}
}

// Eventos duplicados e workers concorrentes: a transição acontece uma vez.
func TestConcurrentWorkersTransitionAConversationOnce(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	u2, _ := e.attendant(t, a, &n2, 5)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, WaitSeconds: 60, DestKind: overflow.DestQueue, DestID: n2})
	c := e.conversation(t, a, &n1)
	e.decide(t, a, c, "")
	e.timeTravel(t, c, 2*time.Minute)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.decide(t, a, c, engine.TriggerStepWait); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if o := e.owner(t, c); o == nil || *o != u2 || e.queueOf(t, c) != n2 {
		t.Fatalf("moved to N2 and assigned to its agent: %v queue=%v", o, e.queueOf(t, c))
	}
	if _, _, hops, _, _ := e.stateOf(t, c); hops != 1 {
		t.Fatalf("one transition, got %d hops", hops)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE conversation_id=$1 AND decision='OVERFLOW'`, c) != 1 || e.count(t, `SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, c) != 1 {
		t.Fatal("one OVERFLOW decision and one assignment, whatever the number of duplicates")
	}
}

// Fila manual com max_wait; espera em horário útil (suspensão não conta); ação humana abre novo episódio.
func TestManualQueueMaxWaitBusinessWaitsAndHumanActionStartsANewEpisode(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "Manual", "manual"), e.queue(t, a, "N2", "round_robin")
	u2, _ := e.attendant(t, a, &n2, 5)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.MaxWait}, WaitSeconds: 600, DestKind: overflow.DestQueue, DestID: n2})
	c := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c, ""); d.Kind != engine.Enqueue || d.Reason != engine.ReasonManualQueue {
		t.Fatalf("manual: %+v", d)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='step_wait' AND state='pending'`, c) != 1 {
		t.Fatal("a manual queue with a max_wait step gets its timer")
	}
	e.timeTravel(t, c, 11*time.Minute)
	if d, err := e.decide(t, a, c, engine.TriggerStepWait); err != nil || d.Kind != engine.AssignAgent || d.AssignedUser != u2 {
		t.Fatalf("nobody took it in 10 minutes: %+v %v", d, err)
	}
	// human action: the person puts it back in the first queue -> new episode, empty loop guard
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=NULL, queue_id=$2 WHERE id=$1`, c, n1)
	d, _ := e.decide(t, a, c, "")
	_, step, hops, episode, visited := e.stateOf(t, c)
	if d.Kind != engine.Enqueue || episode != 2 || step != 0 || hops != 0 || len(visited) != 1 {
		t.Fatalf("a person's action starts a new episode: %+v episode=%d step=%d hops=%d visited=%v", d, episode, step, hops, visited)
	}
	// business time: a calendar open 24x7 but suspended for the last hour: only the time BEFORE the suspension counts
	cal := e.calendar(t, a, "util", "24x7", "[]")
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '2 hours',now()+interval '1 hour')`, a, cal)
	n3 := e.queue(t, a, "Util", "manual")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, n3, cal)
	e.policy(t, a, n3, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.MaxWait}, WaitSeconds: 5400, WaitBusiness: true, DestKind: overflow.DestQueue, DestID: n2})
	c3 := e.conversation(t, a, &n3)
	e.decide(t, a, c3, "")
	e.timeTravel(t, c3, 3*time.Hour) // entered 3h ago: wall elapsed 3h, but only 1h was open time (before the suspension)
	if d, _ := e.decide(t, a, c3, engine.TriggerStepWait); e.queueOf(t, c3) != n3 || d.Kind == engine.AssignAgent {
		t.Fatalf("1h of open time is less than the 90 min asked: %+v", d)
	}
	var due time.Time
	if err := e.seed.QueryRow(e.ctx, `SELECT due_at FROM routing_engine_timers WHERE conversation_id=$1 AND kind='step_wait' AND state='pending'`, c3).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(due); d < 80*time.Minute || d > 100*time.Minute { // 30 more open minutes after the suspension ends in 1h
		t.Fatalf("a business wait is due when the open time adds up: in %v, want ~1h30", d)
	}
}

// Ao mudar de fila, os timers da fila antiga morrem: não sobra um "abrir expediente" do Financeiro acordando uma conversa que já mora no Suporte.
func TestEnteringAStepCancelsTheTimersOfThePlaceItLeft(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	fin, sup := e.queue(t, a, "Financeiro", "round_robin"), e.queue(t, a, "Suporte", "round_robin")
	e.attendant(t, a, &fin, 3)
	e.attendant(t, a, &sup, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, sup, e.conversation(t, a, &sup)) // Suporte is full
	closed := e.calendar(t, a, "fechado", "24x7", "[]")
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '1 hour',now()+interval '5 hours')`, a, closed)
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, fin, closed)
	e.policy(t, a, fin, 4, overflow.ExhaustedKeepWaiting, overflow.Step{Triggers: []overflow.Trigger{overflow.AfterHours}, WaitSeconds: 300, DestKind: overflow.DestQueue, DestID: sup})
	c := e.conversation(t, a, &fin)
	if d, _ := e.decide(t, a, c, ""); d.Kind != engine.WaitUntilOpen {
		t.Fatalf("first: %+v", d)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND state='pending'`, c) != 2 {
		t.Fatal("while it waits in the closed origin: the opening timer and the step timer")
	}
	e.timeTravel(t, c, 6*time.Minute)
	d, err := e.decide(t, a, c, engine.TriggerStepWait)
	if err != nil || d.Kind != engine.Enqueue || e.queueOf(t, c) != sup {
		t.Fatalf("moved to the open sector, which is full: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='open_wakeup' AND state IN ('pending','leased')`, c) != 0 {
		t.Fatal("the opening timer of the sector it left must not survive")
	}
}

// Etapas são ordenadas: uma etapa posterior pronta por OUTRO gatilho não fura a fila de uma anterior que ainda espera.
func TestALaterStepReadyByAnotherTriggerDoesNotJumpAnEarlierWaitingStep(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2, n3 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin"), e.queue(t, a, "N3", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.attendant(t, a, &n2, 3)
	e.attendant(t, a, &n3, 3)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	e.policy(t, a, n1, 4, overflow.ExhaustedManual,
		overflow.Step{Triggers: []overflow.Trigger{overflow.NoEligibleAgent, overflow.CapacityExhausted}, WaitSeconds: 600, DestKind: overflow.DestQueue, DestID: n2},
		overflow.Step{Triggers: []overflow.Trigger{overflow.MaxWait}, WaitSeconds: 60, DestKind: overflow.DestQueue, DestID: n3})
	c := e.conversation(t, a, &n1)
	e.decide(t, a, c, "")
	e.timeTravel(t, c, 2*time.Minute) // step 2 (max_wait 1 min) is ready; step 1 (capacity, 10 min) is not
	d, _ := e.decide(t, a, c, engine.TriggerStepWait)
	if e.queueOf(t, c) != n1 || d.Kind == engine.AssignAgent {
		t.Fatalf("step 2 must wait for step 1: queue=%v %+v", e.queueOf(t, c), d)
	}
	e.timeTravel(t, c, 11*time.Minute)
	if d, _ := e.decide(t, a, c, engine.TriggerStepWait); e.queueOf(t, c) != n2 || d.Kind != engine.AssignAgent {
		t.Fatalf("now step 1 goes first: queue=%v %+v", e.queueOf(t, c), d)
	}
}

// Plantão substitui a escala pessoal; a política de uma etapa relaxa só o que nomeia, dentro da fila de destino.
func TestOnCallReplacesTheShiftAndStepRelaxationsApplyInsideTheDestination(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "N2", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	closedShift := e.calendar(t, a, "turno fechado", "custom", `[{"day":"mon","intervals":[]}]`)
	// the on-call person's own shift is closed, and it does not matter: the on-call period is their shift
	oncall, _ := e.attendant(t, a, nil, 0)
	e.exec(t, `INSERT INTO agent_shifts (tenant_id, user_id, calendar_id) VALUES ($1,$2,$3)`, a, oncall, closedShift)
	roster := e.roster(t, a, "Plantão", oncall)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestOnCall, DestID: roster})
	c := e.conversation(t, a, &n1)
	if d, err := e.decide(t, a, c, ""); err != nil || d.AssignedUser != oncall {
		t.Fatalf("on call overrides the closed personal shift: %+v %v", d, err)
	}
	// destination queue whose only member is off shift: assigned only when the step says allow_off_shift
	off, _ := e.attendant(t, a, &n2, 3)
	e.exec(t, `INSERT INTO agent_shifts (tenant_id, user_id, calendar_id) VALUES ($1,$2,$3)`, a, off, closedShift)
	e.exec(t, `DELETE FROM overflow_policies`)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestQueue, DestID: n2})
	c2 := e.conversation(t, a, &n1)
	d, err := e.decide(t, a, c2, "")
	if err != nil || d.Kind != engine.Enqueue || e.queueOf(t, c2) != n2 || e.owner(t, c2) != nil {
		t.Fatalf("moved to N2 but its only agent is off shift: %+v %v", d, err)
	}
	e.exec(t, `DELETE FROM overflow_policies`)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestQueue, DestID: n2, AllowOffShift: true})
	c3 := e.conversation(t, a, &n1)
	if d, err := e.decide(t, a, c3, ""); err != nil || d.AssignedUser != off {
		t.Fatalf("allow_off_shift lets the contingency step use the off-shift agent: %+v %v", d, err)
	}
}

// Salvar, trocar ou apagar a política reavalia as conversas que já esperam (inclusive em fila manual, que não tem job de roteamento).
func TestChangingThePolicyReevaluatesConversationsAlreadyWaiting(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "Manual", "manual"), e.queue(t, a, "N2", "round_robin")
	u2, _ := e.attendant(t, a, &n2, 5)
	c := e.conversation(t, a, &n1)
	e.decide(t, a, c, "") // no policy yet: it just waits, no timer
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1`, c) != 0 {
		t.Fatal("no policy, no timer")
	}
	e.timeTravel(t, c, 20*time.Minute)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.MaxWait}, WaitSeconds: 600, DestKind: overflow.DestQueue, DestID: n2})
	kick := func() {
		if err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(c2 context.Context) error {
			_, err := sectorsadapters.KickQueue(c2, platformdb.QuerierFromContext(c2, e.app), a, n1)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	kick() // what saving the policy does, in the same transaction
	if n, err := e.eng.RunTimersOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("the kicked conversation runs: %d %v", n, err)
	}
	if o := e.owner(t, c); o == nil || *o != u2 || e.queueOf(t, c) != n2 {
		t.Fatalf("waiting 20 minutes under a 10-minute rule, it moves as soon as the policy exists: %v", o)
	}
	// deleting the policy: pending step timers are kicked, run once, and the conversation is simply left where it is
	c2 := e.conversation(t, a, &n1)
	e.decide(t, a, c2, "")
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='step_wait' AND state='pending'`, c2) != 1 {
		t.Fatal("the policy plans a step timer for the conversation")
	}
	e.exec(t, `DELETE FROM overflow_policies`)
	kick()
	e.exec(t, `UPDATE routing_engine_timers SET due_at=now() WHERE conversation_id=$1`, c2)
	e.eng.RunTimersOnce(e.ctx)
	if e.queueOf(t, c2) != n1 || e.owner(t, c2) != nil {
		t.Fatal("with no policy the conversation stays where it is")
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND state IN ('pending','leased')`, c2) != 0 {
		t.Fatal("and no timer is left alive")
	}
}

// Mudança de calendário recalcula também as esperas em horário útil (não só a abertura).
func TestACalendarChangeMovesPendingBusinessStepWaits(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, n2 := e.queue(t, a, "Util", "manual"), e.queue(t, a, "N2", "round_robin")
	e.attendant(t, a, &n2, 3)
	cal := e.calendar(t, a, "util", "24x7", "[]")
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '2 hours',now()+interval '1 hour')`, a, cal)
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, n1, cal)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.MaxWait}, WaitSeconds: 5400, WaitBusiness: true, DestKind: overflow.DestQueue, DestID: n2})
	c := e.conversation(t, a, &n1)
	e.decide(t, a, c, "")
	e.timeTravel(t, c, 3*time.Hour)
	e.decide(t, a, c, engine.TriggerStepWait)
	var due time.Time
	_ = e.seed.QueryRow(e.ctx, `SELECT due_at FROM routing_engine_timers WHERE conversation_id=$1 AND kind='step_wait' AND state='pending'`, c).Scan(&due)
	if time.Until(due) < 80*time.Minute {
		t.Fatalf("with the suspension the wait ends in ~1h30: %v", time.Until(due))
	}
	e.exec(t, `DELETE FROM calendar_suspensions WHERE calendar_id=$1`, cal) // the suspension is lifted: 3 open hours have passed, the wait is over
	if err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(c2 context.Context) error {
		_, err := sectorsadapters.ReplanWaiting(c2, platformdb.QuerierFromContext(c2, e.app), a)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = e.seed.QueryRow(e.ctx, `SELECT due_at FROM routing_engine_timers WHERE conversation_id=$1 AND kind='step_wait' AND state='pending'`, c).Scan(&due)
	if time.Until(due) > time.Minute {
		t.Fatalf("the business wait must be re-planned to the past: %v", time.Until(due))
	}
}
