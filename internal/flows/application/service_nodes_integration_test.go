package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/flowstest"
	servicectxadapters "github.com/omnira/omnira/internal/servicectx/adapters"
)

// ADR-0049 phase 6 on a real PostgreSQL: the nodes go through the real engine, the real effects and the real module, in the step's own transaction.

type serviceFx struct {
	*flowstest.RecordingEffects
	real *adapters.PostgresEffects
}

func (s serviceFx) ServiceStart(ctx context.Context, c, r uuid.UUID, q int) (string, bool, error) {
	return s.real.ServiceStart(ctx, c, r, q)
}
func (s serviceFx) ServiceClassify(ctx context.Context, c, r uuid.UUID, q int, code string) error {
	return s.real.ServiceClassify(ctx, c, r, q, code)
}
func (s serviceFx) ServiceTask(ctx context.Context, c, r uuid.UUID, q int, title, desc, pr string) error {
	return s.real.ServiceTask(ctx, c, r, q, title, desc, pr)
}
func (s serviceFx) ServiceOpenCase(ctx context.Context, c, r uuid.UUID, q int, title, desc, pr string) (string, error) {
	return s.real.ServiceOpenCase(ctx, c, r, q, title, desc, pr)
}
func (s serviceFx) ServiceClose(ctx context.Context, c, r uuid.UUID, q int, code, note string) (bool, error) {
	return s.real.ServiceClose(ctx, c, r, q, code, note)
}

func (x *rt) withServiceModule() *adapters.PostgresEffects {
	svc, _, _ := servicectxadapters.Build(x.env.App, servicectxadapters.Options{})
	real := adapters.NewPostgresEffects(x.env.App, nil).WithService(servicectxadapters.NewFlowEffects(x.env.App, svc))
	x.eng = NewEngine(x.repo, x.repo, serviceFx{RecordingEffects: x.fx, real: real}, AllExecutors()).
		WithClock(func() time.Time { x.mu.Lock(); defer x.mu.Unlock(); return x.now }).WithLogger(func(string, ...any) {})
	return real
}

func (x *rt) q1(sql string, args ...any) (n int) {
	x.t.Helper()
	if err := x.env.Seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		x.t.Fatal(err)
	}
	return n
}

const serviceNodesFlow = `{"schema_version":1,"nodes":[
 {"id":"start","type":"trigger"},
 {"id":"s","type":"start_service_session","config":{"variable":"protocolo"}},
 {"id":"c","type":"classify_service_session","config":{"category":"suporte"}},
 {"id":"t","type":"create_service_task","config":{"title":"Retornar {{protocolo}}","priority":"high"}},
 {"id":"o","type":"open_service_case","config":{"title":"Falha {{protocolo}}","variable":"ocorrencia"}},
 {"id":"x","type":"close_service_session","config":{"result":"information_provided","note":"respondido pelo fluxo"}},
 {"id":"fin","type":"end"}],
 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"s"},{"id":"2","source":"s","sourcePort":"next","target":"c"},{"id":"3","source":"c","sourcePort":"next","target":"t"},
  {"id":"4","source":"t","sourcePort":"next","target":"o"},{"id":"5","source":"o","sourcePort":"next","target":"x"},{"id":"6","source":"x","sourcePort":"next","target":"fin"}]}`

func TestServiceNodesWorkTheRealModuleInsideTheStep(t *testing.T) {
	x := newRT(t)
	x.withServiceModule()
	if _, err := x.env.Seed.Exec(context.Background(), `INSERT INTO service_categories(tenant_id, code, label) VALUES($1,'suporte','Suporte')`, x.tenant); err != nil {
		t.Fatal(err)
	}
	x.publish("service-nodes", serviceNodesFlow)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := x.runStatus(); st != "completed" {
		t.Fatalf("the run completes: %s", st)
	}
	var protocol, status, resolution, closedSource string
	var category *uuid.UUID
	var sessionID uuid.UUID
	if err := x.env.Seed.QueryRow(context.Background(), `SELECT id, protocol, status, resolution_code, closed_source, category_id FROM service_sessions WHERE conversation_id=$1`, x.conv).
		Scan(&sessionID, &protocol, &status, &resolution, &closedSource, &category); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(protocol, "AT-") || status != "closed" || resolution != "information_provided" || closedSource != "flow" || category == nil {
		t.Fatalf("session: %s %s %s %s %v", protocol, status, resolution, closedSource, category)
	}
	// the CONVERSATION is untouched by close_service_session
	var convStatus string
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT status FROM conversations WHERE id=$1`, x.conv).Scan(&convStatus)
	if convStatus != "open" {
		t.Fatalf("closing the service session does not close the conversation: %s", convStatus)
	}
	// the task: origin flow, tied to the run and the step, title with the protocol variable, no assignee
	var title, origin string
	var assignee *uuid.UUID
	var runID *uuid.UUID
	if err := x.env.Seed.QueryRow(context.Background(), `SELECT title, origin_type, assigned_user_id, flow_run_id FROM service_tasks WHERE session_id=$1`, sessionID).Scan(&title, &origin, &assignee, &runID); err != nil {
		t.Fatal(err)
	}
	if title != "Retornar "+protocol || origin != "flow" || assignee != nil || runID == nil {
		t.Fatalf("task: %q %s %v %v", title, origin, assignee, runID)
	}
	// the case: opened from the session, number allocated by the company's counter
	var caseNumber, caseTitle string
	var originSession *uuid.UUID
	if err := x.env.Seed.QueryRow(context.Background(), `SELECT case_number, title, origin_session_id FROM service_cases WHERE tenant_id=$1`, x.tenant).Scan(&caseNumber, &caseTitle, &originSession); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(caseNumber, "OC-") || caseTitle != "Falha "+protocol || originSession == nil || *originSession != sessionID {
		t.Fatalf("case: %s %q %v", caseNumber, caseTitle, originSession)
	}
	// every event the flow wrote is signed "flow", never as a person
	if n := x.q1(`SELECT count(*) FROM context_events WHERE tenant_id=$1 AND session_id=$2 AND actor_type='flow'`, x.tenant, sessionID); n < 5 {
		t.Fatalf("the flow signs what it does: %d events", n)
	}
	if n := x.q1(`SELECT count(*) FROM context_events WHERE tenant_id=$1 AND session_id=$2 AND actor_type<>'flow'`, x.tenant, sessionID); n != 0 {
		t.Fatalf("nothing in this session was signed by anybody but the flow: %d", n)
	}
}

// The same (run, step) never makes a second session, task or case: the effects are idempotent per step.
func TestServiceEffectsAreIdempotentPerStep(t *testing.T) {
	x := newRT(t)
	real := x.withServiceModule()
	// a real run (the task keeps the id of the run that made it: a foreign key)
	x.publish("noop", `{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"fin","type":"end"}],"edges":[{"id":"1","source":"start","sourcePort":"next","target":"fin"}]}`)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	var run uuid.UUID
	if err := x.env.Seed.QueryRow(context.Background(), `SELECT id FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&run); err != nil {
		t.Fatal(err)
	}
	do := func(fn func(ctx context.Context) error) {
		t.Helper()
		if err := platformdbSystem(context.Background(), x.env, x.tenant, fn); err != nil {
			t.Fatal(err)
		}
	}
	var first, second string
	var created1, created2 bool
	do(func(ctx context.Context) (e error) {
		first, created1, e = real.ServiceStart(ctx, x.conv, run, 1)
		return
	})
	do(func(ctx context.Context) (e error) {
		second, created2, e = real.ServiceStart(ctx, x.conv, run, 1)
		return
	})
	if first != second || !created1 || created2 {
		t.Fatalf("the same step returns the same session: %s/%v vs %s/%v", first, created1, second, created2)
	}
	for i := 0; i < 3; i++ {
		do(func(ctx context.Context) error { return real.ServiceTask(ctx, x.conv, run, 2, "Ligar", "", "normal") })
	}
	var c1, c2 string
	do(func(ctx context.Context) (e error) {
		c1, e = real.ServiceOpenCase(ctx, x.conv, run, 3, "Falha", "", "normal")
		return
	})
	do(func(ctx context.Context) (e error) {
		c2, e = real.ServiceOpenCase(ctx, x.conv, run, 3, "Falha", "", "normal")
		return
	})
	if c1 != c2 {
		t.Fatalf("the same step opens one case: %s vs %s", c1, c2)
	}
	if n := x.q1(`SELECT count(*) FROM service_sessions WHERE conversation_id=$1`, x.conv); n != 1 {
		t.Fatalf("one session: %d", n)
	}
	if n := x.q1(`SELECT count(*) FROM service_tasks WHERE tenant_id=$1`, x.tenant); n != 1 {
		t.Fatalf("one task after three identical steps: %d", n)
	}
	if n := x.q1(`SELECT count(*) FROM service_cases WHERE tenant_id=$1`, x.tenant); n != 1 {
		t.Fatalf("one case: %d", n)
	}
	// ... while ANOTHER step of the same run is another task
	do(func(ctx context.Context) error { return real.ServiceTask(ctx, x.conv, run, 4, "Outra", "", "normal") })
	if n := x.q1(`SELECT count(*) FROM service_tasks WHERE tenant_id=$1`, x.tenant); n != 2 {
		t.Fatalf("a different step is a different task: %d", n)
	}
	// closing twice by the SAME step: the second finds the step done and says so, without an error
	var closed1, closed2 bool
	do(func(ctx context.Context) (e error) {
		closed1, e = real.ServiceClose(ctx, x.conv, run, 5, "information_provided", "")
		return
	})
	do(func(ctx context.Context) (e error) {
		closed2, e = real.ServiceClose(ctx, x.conv, run, 5, "information_provided", "")
		return
	})
	if !closed1 || closed2 {
		t.Fatalf("close: %v then %v", closed1, closed2)
	}
}

// A step that classified or closed a session is DONE: replayed after another session was opened, it must not touch the new one.
func TestAReplayedClassifyOrCloseNeverTouchesALaterSession(t *testing.T) {
	x := newRT(t)
	real := x.withServiceModule()
	x.publish("noop", `{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"fin","type":"end"}],"edges":[{"id":"1","source":"start","sourcePort":"next","target":"fin"}]}`)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	var run uuid.UUID
	if err := x.env.Seed.QueryRow(context.Background(), `SELECT id FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := x.env.Seed.Exec(context.Background(), `INSERT INTO service_categories(tenant_id, code, label) VALUES($1,'suporte','Suporte'),($1,'outra','Outra')`, x.tenant); err != nil {
		t.Fatal(err)
	}
	do := func(fn func(ctx context.Context) error) {
		t.Helper()
		if err := platformdbSystem(context.Background(), x.env, x.tenant, fn); err != nil {
			t.Fatal(err)
		}
	}
	var first, second string
	do(func(ctx context.Context) (e error) { first, _, e = real.ServiceStart(ctx, x.conv, run, 1); return })
	do(func(ctx context.Context) error { return real.ServiceClassify(ctx, x.conv, run, 2, "suporte") })
	do(func(ctx context.Context) (e error) {
		_, e = real.ServiceClose(ctx, x.conv, run, 3, "information_provided", "")
		return
	})
	// the attendance goes on: a NEW session opens (another step)
	do(func(ctx context.Context) (e error) { second, _, e = real.ServiceStart(ctx, x.conv, run, 4); return })
	if first == second {
		t.Fatalf("a new session was expected: %s", first)
	}
	// ... and the earlier steps are replayed (the engine retried them): nothing happens to the new session
	do(func(ctx context.Context) error { return real.ServiceClassify(ctx, x.conv, run, 2, "suporte") })
	var closed bool
	do(func(ctx context.Context) (e error) {
		closed, e = real.ServiceClose(ctx, x.conv, run, 3, "information_provided", "")
		return
	})
	if closed {
		t.Fatal("a replayed close reported that it closed something")
	}
	if n := x.q1(`SELECT count(*) FROM service_sessions WHERE conversation_id=$1 AND protocol=$2 AND status IN ('open','in_progress','waiting') AND category_id IS NULL`, x.conv, second); n != 1 {
		t.Fatalf("the later session was left alone (still active, not classified): %d", n)
	}
	// the same step with another content is refused, not silently reused
	err := platformdbSystem(context.Background(), x.env, x.tenant, func(ctx context.Context) error {
		return real.ServiceClassify(ctx, x.conv, run, 2, "outra")
	})
	if err == nil {
		t.Fatal("the same step with another category must be refused")
	}
}

// The facade acts for a run only on the conversation that run attends (a caller inside the system cannot sign work as "flow" on another conversation).
func TestTheFlowFacadeRefusesARunThatDoesNotAttendThatConversation(t *testing.T) {
	x := newRT(t)
	real := x.withServiceModule()
	x.publish("noop", `{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"fin","type":"end"}],"edges":[{"id":"1","source":"start","sourcePort":"next","target":"fin"}]}`)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	var run uuid.UUID
	if err := x.env.Seed.QueryRow(context.Background(), `SELECT id FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&run); err != nil {
		t.Fatal(err)
	}
	other, _ := x.env.SeedConversation(t, x.tenant, "bot") // another conversation of the SAME company
	deny := func(name string, fn func(ctx context.Context) error) {
		t.Helper()
		if err := platformdbSystem(context.Background(), x.env, x.tenant, fn); err == nil {
			t.Errorf("%s: a run that does not attend the conversation must be refused", name)
		}
	}
	deny("start", func(ctx context.Context) error { _, _, e := real.ServiceStart(ctx, other, run, 1); return e })
	deny("task", func(ctx context.Context) error { return real.ServiceTask(ctx, other, run, 2, "x", "", "normal") })
	deny("case", func(ctx context.Context) error {
		_, e := real.ServiceOpenCase(ctx, other, run, 3, "x", "", "normal")
		return e
	})
	deny("classify", func(ctx context.Context) error { return real.ServiceClassify(ctx, other, run, 4, "suporte") })
	deny("close", func(ctx context.Context) error {
		_, e := real.ServiceClose(ctx, other, run, 5, "information_provided", "")
		return e
	})
	deny("a run that does not exist", func(ctx context.Context) error { _, _, e := real.ServiceStart(ctx, x.conv, uuid.New(), 1); return e })
	for _, table := range []string{"service_sessions", "service_tasks", "service_cases"} {
		if n := x.q1(`SELECT count(*) FROM `+table+` WHERE tenant_id=$1`, x.tenant); n != 0 {
			t.Errorf("%s: nothing was written: %d", table, n)
		}
	}
	// a run of ANOTHER company is not found either (the company comes from the session)
	otherConv, _ := x.env.SeedConversation(t, x.env.TenantB, "bot")
	err := platformdbSystem(context.Background(), x.env, x.env.TenantB, func(ctx context.Context) error {
		_, _, e := real.ServiceStart(ctx, otherConv, run, 1)
		return e
	})
	if err == nil {
		t.Error("a run of another company must be refused")
	}
}

// A refusal of the module rolls back only that call: the node takes its error port and the step's transaction is still usable.
func TestAServiceRefusalTakesTheErrorPortAndLeavesATransactionThatWorks(t *testing.T) {
	x := newRT(t)
	x.withServiceModule()
	x.publish("service-refusal", `{"schema_version":1,"nodes":[
 {"id":"start","type":"trigger"},
 {"id":"c","type":"classify_service_session","config":{"category":"nao-existe"}},
 {"id":"s","type":"start_service_session","config":{"variable":"protocolo"}},
 {"id":"fin","type":"end"},{"id":"bad","type":"end"}],
 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"c"},{"id":"2","source":"c","sourcePort":"next","target":"fin"},{"id":"3","source":"c","sourcePort":"error","target":"s"},{"id":"4","source":"s","sourcePort":"next","target":"fin"}]}`)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := x.runStatus(); st != "completed" {
		t.Fatalf("the run completes through the error port: %s", st)
	}
	if n := x.q1(`SELECT count(*) FROM service_sessions WHERE conversation_id=$1`, x.conv); n != 1 {
		t.Fatalf("the node AFTER the failed one still wrote (the transaction survived the refusal): %d", n)
	}
}

// A closed conversation cannot get a session from a flow: the module's rule holds for the system too.
func TestAFlowCannotOpenASessionOnAClosedConversation(t *testing.T) {
	x := newRT(t)
	real := x.withServiceModule()
	if _, err := x.env.Seed.Exec(context.Background(), `UPDATE conversations SET status='closed' WHERE id=$1`, x.conv); err != nil {
		t.Fatal(err)
	}
	err := platformdbSystem(context.Background(), x.env, x.tenant, func(ctx context.Context) error {
		_, _, e := real.ServiceStart(ctx, x.conv, uuid.New(), 1)
		return e
	})
	if err == nil {
		t.Fatal("a session on a closed conversation must be refused")
	}
	if n := x.q1(`SELECT count(*) FROM service_sessions WHERE conversation_id=$1`, x.conv); n != 0 {
		t.Fatalf("nothing was created: %d", n)
	}
}

// Only the engine's system session is a flow: a person's session cannot use the facade to sign as one.
func TestAPersonCannotUseTheFlowFacade(t *testing.T) {
	x := newRT(t)
	svc, _, _ := servicectxadapters.Build(x.env.App, servicectxadapters.Options{})
	var err error
	x.env.AsUser(t, x.tenant, x.env.UserA, func(ctx context.Context) {
		_, err = svc.FlowStart(ctx, x.conv, uuid.New(), 1)
	})
	if err == nil {
		t.Fatal("a person's session is refused by the flow facade")
	}
	if n := x.q1(`SELECT count(*) FROM service_sessions WHERE conversation_id=$1`, x.conv); n != 0 {
		t.Fatalf("nothing was created: %d", n)
	}
}
