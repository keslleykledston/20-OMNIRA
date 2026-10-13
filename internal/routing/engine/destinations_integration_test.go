package engine_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/engine"
	"github.com/omnira/omnira/internal/routing/overflow"
)

type hubWorld struct{ hub, contract, op uuid.UUID }

// hub: a Hub with one operator who has a live reply grant on a contract with the company. queueScope nil = the contract covers every queue.
func (e *env) hub(t *testing.T, tenant uuid.UUID, queueScope []uuid.UUID) hubWorld {
	t.Helper()
	w := hubWorld{hub: uuid.New(), contract: uuid.New(), op: uuid.New()}
	e.exec(t, `INSERT INTO service_hubs (id, name) VALUES ($1,$2)`, w.hub, "Hub "+w.hub.String()[:6])
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM service_hubs WHERE id=$1`, w.hub) })
	e.exec(t, `INSERT INTO users (id, external_subject, email, display_name, status) VALUES ($1,$2,$3,'op','active')`, w.op, w.op.String(), w.op.String()+"@example.com")
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, w.op) })
	e.exec(t, `INSERT INTO hub_memberships (hub_id, user_id, role_id) SELECT $1,$2,id FROM roles WHERE key='hub_agent' AND tenant_id IS NULL`, w.hub, w.op)
	scope := `{}`
	if queueScope != nil {
		ids := ""
		for i, q := range queueScope {
			if i > 0 {
				ids += ","
			}
			ids += `"` + q.String() + `"`
		}
		scope = `{"queue_ids":[` + ids + `]}`
	}
	e.exec(t, `INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, service_scope) VALUES ($1,$2,$3,$4::jsonb)`, w.contract, w.hub, tenant, scope)
	e.exec(t, `INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, can_reply) VALUES ($1,$2,$3,$4,true)`, w.hub, w.op, tenant, w.contract)
	return w
}

// both answers in a system session: the readiness function and the oracle it must equal (has_active_hub_access, reply required, scope checked)
func (e *env) readiness(t *testing.T, tenant uuid.UUID, w hubWorld, queue uuid.UUID) (ready, oracle bool) {
	t.Helper()
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, tenant, func(c context.Context) error {
		return platformdb.QuerierFromContext(c, e.app).QueryRow(c,
			`SELECT hub_destination_ready($1,$2,$3), has_active_hub_access($4,$1,$3,$5,true,true)`, tenant, w.contract, queue, w.op, w.hub).Scan(&ready, &oracle)
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

// Oracle: hub_destination_ready must answer exactly as has_active_hub_access does, for every state of contract, grant, hub, tenant and user.
func TestHubDestinationReadyEqualsTheLiveAccessOracleInEveryState(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	inScope, outScope := e.queue(t, a, "Dentro", "manual"), e.queue(t, a, "Fora", "manual")
	w := e.hub(t, a, []uuid.UUID{inScope})
	reset := func() {
		e.exec(t, `UPDATE hub_tenant_service_contracts SET status='active', valid_from=now()-interval '1 day', valid_until=NULL, service_scope=$2::jsonb WHERE id=$1`, w.contract, fmt.Sprintf(`{"queue_ids":["%s"]}`, inScope))
		e.exec(t, `UPDATE effective_access_grants SET status='active', can_reply=true, valid_from=now()-interval '1 day', valid_until=NULL WHERE service_contract_id=$1`, w.contract)
		e.exec(t, `UPDATE service_hubs SET status='active' WHERE id=$1`, w.hub)
		e.exec(t, `UPDATE tenants SET status='active' WHERE id=$1`, a)
		e.exec(t, `UPDATE users SET status='active' WHERE id=$1`, w.op)
	}
	reset()
	cases := map[string]string{
		"baseline":              `UPDATE hub_tenant_service_contracts SET status=status WHERE id=$1`,
		"contract suspended":    `UPDATE hub_tenant_service_contracts SET status='suspended' WHERE id=$1`,
		"contract revoked":      `UPDATE hub_tenant_service_contracts SET status='revoked' WHERE id=$1`,
		"contract expired":      `UPDATE hub_tenant_service_contracts SET valid_from=now()-interval '2 days', valid_until=now()-interval '1 day' WHERE id=$1`,
		"contract not started":  `UPDATE hub_tenant_service_contracts SET valid_from=now()+interval '1 day' WHERE id=$1`,
		"scope absent (all)":    `UPDATE hub_tenant_service_contracts SET service_scope='{}'::jsonb WHERE id=$1`,
		"scope empty list":      `UPDATE hub_tenant_service_contracts SET service_scope='{"queue_ids":[]}'::jsonb WHERE id=$1`,
		"scope malformed":       `UPDATE hub_tenant_service_contracts SET service_scope='{"queue_ids":"x"}'::jsonb WHERE id=$1`,
		"grant suspended":       `UPDATE effective_access_grants SET status='suspended' WHERE service_contract_id=$1`,
		"grant revoked":         `UPDATE effective_access_grants SET status='revoked' WHERE service_contract_id=$1`,
		"grant read only":       `UPDATE effective_access_grants SET can_reply=false WHERE service_contract_id=$1`,
		"grant expired":         `UPDATE effective_access_grants SET valid_from=now()-interval '2 days', valid_until=now()-interval '1 day' WHERE service_contract_id=$1`,
		"grant not started":     `UPDATE effective_access_grants SET valid_from=now()+interval '1 day' WHERE service_contract_id=$1`,
		"hub suspended":         `UPDATE service_hubs SET status='suspended' WHERE id=(SELECT hub_id FROM hub_tenant_service_contracts WHERE id=$1)`,
		"tenant suspended":      `UPDATE tenants SET status='suspended' WHERE id=(SELECT tenant_id FROM hub_tenant_service_contracts WHERE id=$1)`,
		"operator deactivated":  `UPDATE users SET status='inactive' WHERE id=(SELECT user_id FROM effective_access_grants WHERE service_contract_id=$1)`,
		"operator left the hub": `DELETE FROM hub_memberships WHERE user_id=(SELECT user_id FROM effective_access_grants WHERE service_contract_id=$1)`,
	}
	sawTrue, sawFalse := false, false
	for name, stmt := range cases {
		reset()
		if name == "operator left the hub" { // cascades the grant away; restore it for the next cases
			defer func() {}()
		}
		e.exec(t, stmt, w.contract)
		for _, q := range []uuid.UUID{inScope, outScope} {
			ready, oracle := e.readiness(t, a, w, q)
			if ready != oracle {
				t.Errorf("%s (queue %v): hub_destination_ready=%v but has_active_hub_access=%v", name, q == inScope, ready, oracle)
			}
			sawTrue = sawTrue || ready
			sawFalse = sawFalse || !ready
		}
		if name == "operator left the hub" { // rebuild what the cascade removed
			e.exec(t, `INSERT INTO hub_memberships (hub_id, user_id, role_id) SELECT $1,$2,id FROM roles WHERE key='hub_agent' AND tenant_id IS NULL`, w.hub, w.op)
			e.exec(t, `INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, can_reply) VALUES ($1,$2,$3,$4,true)`, w.hub, w.op, a, w.contract)
		}
	}
	if !sawTrue || !sawFalse {
		t.Fatalf("the oracle test must exercise both answers (true=%v false=%v)", sawTrue, sawFalse)
	}
}

// Hub como destino: a conversa continua da empresa, só muda para a fila que o contrato cobre; contrato sem cobertura ou sem operador vivo é recusado.
func TestHubDestinationMovesTheConversationInsideTheCompanyAndOnlyWhenTheContractIsLive(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1, hubQ, other := e.queue(t, a, "N1", "round_robin"), e.queue(t, a, "Hub", "manual"), e.queue(t, a, "Outra", "manual")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	w := e.hub(t, a, []uuid.UUID{hubQ})
	hubStep := overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestHub, DestID: w.contract, DestQueueID: &hubQ}
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, hubStep)
	c := e.conversation(t, a, &n1)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.Enqueue || d.Reason != engine.ReasonManualQueue || e.queueOf(t, c) != hubQ {
		t.Fatalf("exposed to the Hub: %+v %v (queue %v)", d, err, e.queueOf(t, c))
	}
	var tenantOf uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT tenant_id FROM conversations WHERE id=$1`, c).Scan(&tenantOf)
	if tenantOf != a {
		t.Fatal("the conversation never leaves its company")
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE conversation_id=$1 AND decision='OVERFLOW' AND destination=$2`, c, "hub:"+w.contract.String()) != 1 {
		t.Fatal("the transition names the contract")
	}
	if e.count(t, `SELECT count(*) FROM memberships WHERE tenant_id=$1 AND user_id=$2`, a, w.op) != 0 {
		t.Fatal("no membership is ever fabricated for a Hub operator")
	}
	// the operator may only read: not ready -> refused, chain exhausted
	e.exec(t, `UPDATE effective_access_grants SET can_reply=false WHERE service_contract_id=$1`, w.contract)
	c2 := e.conversation(t, a, &n1)
	d, _ = e.decide(t, a, c2, "")
	var why string
	_ = e.seed.QueryRow(e.ctx, `SELECT COALESCE(detail->'rejected'->0->>'reason','') FROM routing_engine_decisions WHERE conversation_id=$1 AND decision='REQUIRE_MANUAL_ACTION'`, c2).Scan(&why)
	if d.Reason != engine.ReasonChainExhausted || e.queueOf(t, c2) != n1 || why != "hub_not_ready" {
		t.Fatalf("a Hub with no live operator who may reply: %+v queue=%v why=%q", d, e.queueOf(t, c2), why)
	}
	// a contract that does not cover the queue
	e.exec(t, `UPDATE effective_access_grants SET can_reply=true WHERE service_contract_id=$1`, w.contract)
	e.exec(t, `UPDATE hub_tenant_service_contracts SET service_scope=$2::jsonb WHERE id=$1`, w.contract, fmt.Sprintf(`{"queue_ids":["%s"]}`, other))
	c3 := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c3, ""); d.Reason != engine.ReasonChainExhausted || e.queueOf(t, c3) != n1 {
		t.Fatalf("a queue outside the contract scope: %+v", d)
	}
	// another company's contract can never serve this company's queue (the function asks for the CONTRACT's tenant)
	b := e.tenant(t)
	wb := e.hub(t, b, nil)
	e.exec(t, `DELETE FROM overflow_policies`)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestHub, DestID: wb.contract, DestQueueID: &hubQ})
	c4 := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c4, ""); d.Reason != engine.ReasonChainExhausted || e.queueOf(t, c4) != n1 {
		t.Fatalf("a contract of another company: %+v", d)
	}
}

func TestAgentDestinationEscalatesToANamedAttendantWithinCapacityAndShift(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1 := e.queue(t, a, "N1", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	boss, _ := e.attendant(t, a, nil, 0) // not a member of any queue: the default capacity (5) applies
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestAgent, DestID: boss})
	c := e.conversation(t, a, &n1)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.AssignAgent || d.Reason != engine.ReasonOverflowAgent || d.AssignedUser != boss {
		t.Fatalf("escalated to the named attendant: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM assignment_events WHERE conversation_id=$1 AND reason='overflow_agent'`, c) != 1 {
		t.Fatal("one history row")
	}
	// at capacity (5) -> refused
	for i := 0; i < 4; i++ {
		x := e.conversation(t, a, &n1)
		e.exec(t, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, x, boss)
	}
	c2 := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c2, ""); d.Reason != engine.ReasonChainExhausted || e.owner(t, c2) != nil {
		t.Fatalf("the named attendant is full: %+v", d)
	}
	// off shift: refused unless the step allows it
	e.exec(t, `UPDATE conversations SET status='closed' WHERE assigned_to_user_id=$1`, boss)
	closedShift := e.calendar(t, a, "fechado", "custom", `[{"day":"mon","intervals":[]}]`)
	e.exec(t, `INSERT INTO agent_shifts (tenant_id, user_id, calendar_id) VALUES ($1,$2,$3)`, a, boss, closedShift)
	c3 := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c3, ""); d.Reason != engine.ReasonChainExhausted {
		t.Fatalf("off shift: %+v", d)
	}
	e.exec(t, `UPDATE overflow_steps SET allow_off_shift=true`)
	c4 := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c4, ""); d.AssignedUser != boss {
		t.Fatalf("allow_off_shift: %+v", d)
	}
}

func TestSectorDestinationPicksTheFirstQueueWithAnEligibleAgent(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	n1 := e.queue(t, a, "N1", "round_robin")
	e.attendant(t, a, &n1, 1)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=(SELECT user_id FROM queue_members WHERE queue_id=$1) WHERE id=$2`, n1, e.conversation(t, a, &n1))
	sector := uuid.New()
	e.exec(t, `INSERT INTO sectors (id, tenant_id, name) VALUES ($1,$2,'Financeiro')`, sector, a)
	qA, qB := e.queue(t, a, "A fila vazia", "round_robin"), e.queue(t, a, "B fila com gente", "round_robin")
	e.exec(t, `UPDATE queues SET sector_id=$2 WHERE id IN ($1, $3)`, qA, sector, qB)
	b, _ := e.attendant(t, a, &qB, 3)
	e.policy(t, a, n1, 4, overflow.ExhaustedManual, overflow.Step{Triggers: []overflow.Trigger{overflow.CapacityExhausted}, DestKind: overflow.DestSector, DestID: sector})
	c := e.conversation(t, a, &n1)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.AssignedUser != b || e.queueOf(t, c) != qB {
		t.Fatalf("the first queue with someone eligible (B), not the first by name (A): %+v %v queue=%v", d, err, e.queueOf(t, c))
	}
	if _, _, _, _, visited := e.stateOf(t, c); len(visited) != 3 {
		t.Fatalf("origin, the sector and the chosen queue are visited: %v", visited)
	}
	// nobody anywhere: the conversation waits in the first acceptable queue of the sector
	e.exec(t, `UPDATE queue_members SET available=false WHERE queue_id=$1`, qB)
	c2 := e.conversation(t, a, &n1)
	d, _ = e.decide(t, a, c2, "")
	if d.Kind != engine.Enqueue || e.queueOf(t, c2) != qA {
		t.Fatalf("no agent in the sector: waits in the first queue: %+v queue=%v", d, e.queueOf(t, c2))
	}
	// a closed queue is skipped; deactivating the sector refuses it
	closed := e.calendar(t, a, "fechado", "24x7", "[]")
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '1 hour',now()+interval '2 hours')`, a, closed)
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, qA, closed)
	e.exec(t, `UPDATE queue_members SET available=true WHERE queue_id=$1`, qB)
	e.exec(t, `UPDATE conversations SET status='closed' WHERE queue_id=$1`, qB)
	c3 := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c3, ""); e.queueOf(t, c3) != qB || d.AssignedUser != b {
		t.Fatalf("the closed queue A is skipped: %+v", d)
	}
	e.exec(t, `UPDATE sectors SET active=false WHERE id=$1`, sector)
	c4 := e.conversation(t, a, &n1)
	if d, _ := e.decide(t, a, c4, ""); d.Reason != engine.ReasonChainExhausted || e.queueOf(t, c4) != n1 {
		t.Fatalf("an inactive sector is not a destination: %+v", d)
	}
	_ = time.Second
}

// Codex: even under a contract with no queue restriction, the function only answers for queues of the contract's own company.
func TestHubReadinessNeverAnswersForAQueueOfAnotherCompany(t *testing.T) {
	e := newEnv(t, nil)
	a, b := e.tenant(t), e.tenant(t)
	own, foreign := e.queue(t, a, "Minha", "manual"), e.queue(t, b, "Alheia", "manual")
	w := e.hub(t, a, nil) // unrestricted: covers every queue of A
	if ready, _ := e.readiness(t, a, w, own); !ready {
		t.Fatal("an own queue under an unrestricted contract is ready")
	}
	if ready, _ := e.readiness(t, a, w, foreign); ready {
		t.Fatal("a queue of another company must never be ready, whatever the contract says")
	}
}
