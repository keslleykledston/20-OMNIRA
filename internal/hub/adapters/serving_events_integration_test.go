package adapters_test

// Migration 168: a Hub agent serving an instance reads the events of an attendance (who took it, who handed it over, which application held it) the way it reads the
// messages: for the instance it acts for, with the conversation domain, through the contract's queue scope, read only. Real PostgreSQL, the application's own role.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

func (w *world) attendanceEvents() {
	w.t.Helper()
	person := w.user("event-person")
	for _, key := range []string{"A", "A2", "B"} {
		tenant := w.tenant[key[:1]]
		w.exec(`INSERT INTO assignment_events(tenant_id, conversation_id, to_user_id, changed_by, reason) VALUES($1,$2,$3,$3,'manual_claim')`, tenant, w.conv[key], person)
		w.exec(`INSERT INTO conversation_control_events(tenant_id, conversation_id, control_version, to_kind, to_holder_id, actor_kind, reason)
		        VALUES($1,$2,1,'crm_application',$3,'crm_application','claim')`, tenant, w.conv[key], uuid.New())
	}
}

// eventsSeen counts what the user reads of the two tables for ONE instance, optionally inside the delegated context of another/the same one.
func (w *world) eventsSeen(user uuid.UUID, actInstance, count string) (assignments, controls int) {
	w.t.Helper()
	w.inSession(user, func(ctx context.Context, q platformdb.Querier) {
		if actInstance != "" && !w.enter(ctx, q, user, actInstance) {
			w.t.Fatalf("could not enter the delegated context for %s", actInstance)
		}
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM assignment_events WHERE tenant_id = $1`, w.tenant[count]).Scan(&assignments))
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM conversation_control_events WHERE tenant_id = $1`, w.tenant[count]).Scan(&controls))
	})
	return
}

func TestDelegatedAgentReadsTheEventsOfTheAttendancesItServes(t *testing.T) {
	w := newWorld(t)
	w.attendanceEvents()
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read", "media.read")

	// the conversation domain opens them: both conversations of instance A (queues 1 and 2)
	w.grantKeys(g, "conversation.read")
	if a, c := w.eventsSeen(agent, "A", "A"); a != 2 || c != 2 {
		t.Errorf("with conversation.read the agent reads %d assignment and %d control events of A, want 2 and 2", a, c)
	}
	// never another instance
	if a, c := w.eventsSeen(agent, "A", "B"); a != 0 || c != 0 {
		t.Errorf("instance B leaked %d and %d events", a, c)
	}
	// not acting for a hub: a person who is not a member reads nothing
	if a, c := w.eventsSeen(agent, "", "A"); a != 0 || c != 0 {
		t.Errorf("outside the delegated context the agent read %d and %d events", a, c)
	}

	// another domain alone does not open them
	w.grantKeys(g, "media.read")
	if a, c := w.eventsSeen(agent, "A", "A"); a != 0 || c != 0 {
		t.Errorf("with media.read only the agent read %d and %d events", a, c)
	}

	// the contract serves only queue 1: the events of the other queue's conversation stay closed
	w.grantKeys(g, "conversation.read")
	w.queueScope("A", w.queue1["A"])
	if a, c := w.eventsSeen(agent, "A", "A"); a != 1 || c != 1 {
		t.Errorf("through a contract limited to one queue the agent reads %d and %d events, want 1 and 1", a, c)
	}
}

func TestNothingCanBeWrittenToTheEventsThroughTheDelegatedContext(t *testing.T) {
	w := newWorld(t)
	w.attendanceEvents()
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read", "conversation.reply")
	w.grantKeys(g, "conversation.read", "conversation.reply")
	person := w.user("writer")
	statements := []struct {
		name, sql string
		args      []any
	}{
		{"insert assignment event", `INSERT INTO assignment_events(tenant_id, conversation_id, to_user_id, changed_by, reason) VALUES($1,$2,$3,$3,'manual_claim')`, []any{w.tenant["A"], w.conv["A"], person}},
		{"update assignment event", `UPDATE assignment_events SET reason = 'x' WHERE tenant_id = $1`, []any{w.tenant["A"]}},
		{"delete assignment event", `DELETE FROM assignment_events WHERE tenant_id = $1`, []any{w.tenant["A"]}},
		{"update control event", `UPDATE conversation_control_events SET reason = 'x' WHERE tenant_id = $1`, []any{w.tenant["A"]}},
		{"delete control event", `DELETE FROM conversation_control_events WHERE tenant_id = $1`, []any{w.tenant["A"]}},
	}
	for _, s := range statements {
		var affected int64
		err := platformdb.WithTenantSession(w.ctx, w.app, agent, false, func(c context.Context) error {
			q := platformdb.QuerierFromContext(c, w.app)
			var ok *bool
			if err := q.QueryRow(c, `SELECT lock_served_tenant($1,$2,$3)`, w.tenant["A"], agent, w.hub).Scan(&ok); err != nil || ok == nil || !*ok {
				t.Fatal("enter")
			}
			tag, err := q.Exec(c, s.sql, s.args...)
			affected = tag.RowsAffected()
			return err
		})
		if err == nil && affected != 0 {
			t.Errorf("%s changed %d row(s) through the delegated context", s.name, affected)
		}
	}
	if n := w.count(`SELECT count(*) FROM assignment_events WHERE tenant_id = $1 AND reason = 'x'`, w.tenant["A"]); n != 0 {
		t.Errorf("%d assignment events were rewritten", n)
	}
	if n := w.count(`SELECT count(*) FROM assignment_events WHERE tenant_id = $1`, w.tenant["A"]); n != 2 {
		t.Errorf("assignment events of A = %d, want the 2 that were seeded", n)
	}
}

// Through the real handler and middleware: the timeline of a conversation the agent serves carries its events, another instance's conversation is a 404.
func TestDelegatedAgentGetsTheTimelineOfTheAttendanceItServes(t *testing.T) {
	w := newWorld(t)
	api := w.readsAPI(t)
	w.attendanceEvents()
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")
	hub := "hub:" + w.hub.String()
	base := "/api/v1/tenants/" + w.tenant["A"].String()

	code, body, _ := api.get(agent, base+"/inbox/conversations/"+w.conv["A"].String()+"/timeline", hub)
	if code != 200 || !strings.Contains(body, `"claimed"`) || !strings.Contains(body, `"external_claimed"`) {
		t.Fatalf("timeline of a served conversation: %d %s", code, body)
	}
	// a conversation of another instance, asked under this instance's address, is not found
	if code, _, _ := api.get(agent, base+"/inbox/conversations/"+w.conv["B"].String()+"/timeline", hub); code != 404 {
		t.Errorf("another instance's conversation: %d, want 404", code)
	}
	// without the key the door itself refuses
	w.grantKeys(g, "media.read")
	if code, _, _ := api.get(agent, base+"/inbox/conversations/"+w.conv["A"].String()+"/timeline", hub); code == 200 {
		t.Errorf("without conversation.read the timeline answered %d", code)
	}
}
