package adapters_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// A person sends a conversation to another queue (department). Real PostgreSQL, the application role, the real handler and the real audit.

func (e *assignEnv) queue(tenant uuid.UUID, name string, sectorOn bool) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,$3,'manual')`, id, tenant, name)
	if !sectorOn {
		sector := uuid.New()
		e.exec(`INSERT INTO sectors(id,tenant_id,name,active) VALUES($1,$2,$3,false)`, sector, tenant, "setor-"+name)
		e.exec(`UPDATE queues SET sector_id=$3 WHERE tenant_id=$1 AND id=$2`, tenant, id, sector)
	}
	return id
}

func (e *assignEnv) inQueue(conv, queue uuid.UUID) {
	e.t.Helper()
	e.exec(`UPDATE conversations SET queue_id=$2 WHERE id=$1`, conv, queue)
}

func (e *assignEnv) route(user, tenant, conv, queue uuid.UUID) result {
	return e.call(user, tenant, conv, "route-queue", fmt.Sprintf(`{"queue_id":"%s"}`, queue))
}

func (e *assignEnv) queueOf(conv uuid.UUID) string {
	var q *uuid.UUID
	if err := e.seed.QueryRow(e.t.Context(), `SELECT queue_id FROM conversations WHERE id=$1`, conv).Scan(&q); err != nil {
		e.t.Fatal(err)
	}
	if q == nil {
		return ""
	}
	return q.String()
}

func TestRouteToQueueMovesTheConversationQueuesTheRoutingJobAndAudits(t *testing.T) {
	e := newAssignEnv(t)
	comercial, suporte := e.queue(e.tenantA, "Comercial", true), e.queue(e.tenantA, "Suporte", true)
	conv := e.conversation(e.tenantA)
	e.inQueue(conv, comercial)

	jobs := func() int {
		return e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.routing.assign.v1' AND aggregate_id=$2`, e.tenantA, conv.String())
	}
	before := jobs()
	r := e.route(e.agent1, e.tenantA, conv, suporte)
	want(t, r, 200, "an agent sends an unassigned conversation to another queue")
	var out struct {
		QueueID string `json:"queue_id"`
		Changed bool   `json:"changed"`
	}
	_ = json.Unmarshal([]byte(r.body), &out)
	if !out.Changed || out.QueueID != suporte.String() || e.queueOf(conv) != suporte.String() {
		t.Fatalf("answer %s; queue now %s", r.body, e.queueOf(conv))
	}
	if jobs() != before+1 {
		t.Errorf("routing jobs: %d, want one new (the engine must place it inside the new queue)", jobs()-before)
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2 AND action='conversation.queue_changed' AND actor_id=$3`, e.tenantA, conv, e.agent1) != 1 {
		t.Error("the queue change is not audited under the person who made it")
	}
	if e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, conv) != 0 {
		t.Error("nobody held it: no assignment history should be written")
	}

	// the same queue again, with nobody holding it: a no-op (no second job, no second audit)
	r = e.route(e.agent1, e.tenantA, conv, suporte)
	want(t, r, 200, "the same queue again")
	if e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2 AND action='conversation.queue_changed'`, e.tenantA, conv) != 1 || jobs() != before+1 {
		t.Error("repeating the move queued a second job or a second audit")
	}
}

func TestRouteToQueueReleasesTheHolderWithHistory(t *testing.T) {
	e := newAssignEnv(t)
	a, b := e.queue(e.tenantA, "Vendas", true), e.queue(e.tenantA, "Financeiro", true)
	conv := e.conversation(e.tenantA)
	e.inQueue(conv, a)
	want(t, e.call(e.agent1, e.tenantA, conv, "assign", ""), 200, "agent1 holds it")
	want(t, e.route(e.agent1, e.tenantA, conv, b), 200, "the holder sends it away")
	if e.owner(conv) != "" || e.queueOf(conv) != b.String() {
		t.Fatalf("owner=%q queue=%s: the holder must be released and the queue changed", e.owner(conv), e.queueOf(conv))
	}
	if e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1 AND reason='queue_transfer' AND to_user_id IS NULL AND from_user_id=$2`, conv, e.agent1) != 1 {
		t.Error("the release is not in the append-only history")
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2 AND action IN ('conversation.unassigned','conversation.queue_changed')`, e.tenantA, conv) != 2 {
		t.Error("the release and the queue change are not both audited")
	}
}

func TestRouteToQueueRespectsWhoHoldsTheConversation(t *testing.T) {
	e := newAssignEnv(t)
	a, b := e.queue(e.tenantA, "Q-a", true), e.queue(e.tenantA, "Q-b", true)
	conv := e.conversation(e.tenantA)
	e.inQueue(conv, a)
	want(t, e.call(e.agent2, e.tenantA, conv, "assign", ""), 200, "agent2 holds it")
	want(t, e.route(e.agent1, e.tenantA, conv, b), 403, "a colleague's chat cannot be pulled away by another agent")
	if e.owner(conv) != e.agent2.String() || e.queueOf(conv) != a.String() {
		t.Fatal("a refused move changed the conversation")
	}
	want(t, e.route(e.supervisor, e.tenantA, conv, b), 200, "a supervisor (conversation.manage) may")
	if e.owner(conv) != "" || e.queueOf(conv) != b.String() {
		t.Fatalf("after the supervisor's move: owner=%q queue=%s", e.owner(conv), e.queueOf(conv))
	}
}

func TestRouteToQueueRefusesWhatIsNotRoutable(t *testing.T) {
	e := newAssignEnv(t)
	a, b := e.queue(e.tenantA, "R-a", true), e.queue(e.tenantA, "R-b", true)
	off := e.queue(e.tenantA, "R-off", false)
	other := e.queue(e.tenantB, "R-other", true)

	closed := e.conversation(e.tenantA)
	e.inQueue(closed, a)
	e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, closed)
	want(t, e.route(e.agent1, e.tenantA, closed, b), 409, "a closed conversation")

	held := e.conversation(e.tenantA)
	e.inQueue(held, a)
	inst := uuid.New()
	e.exec(`INSERT INTO external_app_installations(id,tenant_id,name,scopes) VALUES($1,$2,'crm',ARRAY['conversation.claim'])`, inst, e.tenantA)
	e.exec(`UPDATE conversations SET control_kind='crm_application', control_holder_id=$2, control_version=control_version+1, control_since=now(), control_origin='crm' WHERE id=$1`, held, inst)
	want(t, e.route(e.supervisor, e.tenantA, held, b), 409, "a conversation an external application holds")
	if e.queueOf(held) != a.String() {
		t.Error("the application's conversation was moved")
	}

	free := e.conversation(e.tenantA)
	e.inQueue(free, a)
	want(t, e.route(e.agent1, e.tenantA, free, off), 422, "a queue whose sector is off")
	want(t, e.route(e.agent1, e.tenantA, free, other), 422, "another company's queue")
	want(t, e.route(e.agent1, e.tenantA, free, uuid.New()), 422, "a queue that does not exist")
	want(t, e.call(e.agent1, e.tenantA, free, "route-queue", `{}`), 400, "no queue named")
	want(t, e.call(e.agent1, e.tenantA, free, "route-queue", `{"queue_id":"`+b.String()+`","tenant_id":"`+e.tenantB.String()+`"}`), 400, "a body field the API does not know (tenant from the body is never trusted)")
	if e.queueOf(free) != a.String() {
		t.Error("a refused move changed the queue")
	}
}

func TestRouteToQueueNeedsAMembershipThatMayAndNeverCrossesCompanies(t *testing.T) {
	e := newAssignEnv(t)
	a, b := e.queue(e.tenantA, "S-a", true), e.queue(e.tenantA, "S-b", true)
	conv := e.conversation(e.tenantA)
	e.inQueue(conv, a)
	want(t, e.route(e.viewer, e.tenantA, conv, b), 403, "a member with no conversation permission")
	if r := e.route(e.revoked, e.tenantA, conv, b); r.code != 403 && r.code != 404 { // the tenant middleware answers a revoked member as "tenant not found"
		t.Fatalf("a revoked membership: %d %s", r.code, r.body)
	}
	// another company's administrator reaches nothing of this company, in either direction
	if r := e.route(e.outsiderB, e.tenantA, conv, b); r.code != 403 && r.code != 404 {
		t.Fatalf("an administrator of another company: %d %s", r.code, r.body)
	}
	convB := e.conversation(e.tenantB)
	if r := e.route(e.agent1, e.tenantB, convB, b); r.code != 403 && r.code != 404 {
		t.Fatalf("an agent of A acting on company B: %d %s", r.code, r.body)
	}
	if e.queueOf(conv) != a.String() {
		t.Error("a refused request changed the queue")
	}
}

// A spam contact left the queue on purpose, and an internal contact (the instance's own people) is never routed: the server refuses both, whatever the screen shows.
func TestRouteToQueueRefusesSpamAndInternalContacts(t *testing.T) {
	e := newAssignEnv(t)
	from, to := e.queue(e.tenantA, "Origem", true), e.queue(e.tenantA, "Destino", true)
	for _, kind := range []string{"spam", "internal"} {
		conv := e.conversation(e.tenantA)
		e.inQueue(conv, from)
		if kind == "internal" {
			e.exec(`UPDATE contacts SET kind='internal', internal_role='team' WHERE id=(SELECT contact_id FROM conversations WHERE id=$1)`, conv)
		} else {
			e.exec(`UPDATE contacts SET kind='spam' WHERE id=(SELECT contact_id FROM conversations WHERE id=$1)`, conv)
		}
		// spam: the routing guard answers 409; an internal contact's conversation is not even visible to the member (404). Either way, it does not move.
		if r := e.route(e.agent1, e.tenantA, conv, to); r.code != 409 && r.code != 404 {
			t.Errorf("a %s contact: code=%d body=%q, want 409 or 404", kind, r.code, r.body)
		}
		if e.queueOf(conv) != from.String() {
			t.Errorf("%s: the queue changed", kind)
		}
	}
}
