package adapters_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// ---- flow steps are idempotent, concurrently, and by content ---------------------------------------------------------------------------------

func (s *stack) flowRun(conv uuid.UUID) uuid.UUID {
	s.t.Helper()
	flowID, versionID, runID := uuid.New(), uuid.New(), uuid.New()
	s.exec(`INSERT INTO flows(id, tenant_id, slug, name) VALUES($1,$2,$3,'Fluxo')`, flowID, s.env.TenantA, "svc-"+flowID.String()[:8])
	s.exec(`INSERT INTO flow_versions(id, tenant_id, flow_id, version, definition, definition_hash) VALUES($1,$2,$3,1,'{}','h')`, versionID, s.env.TenantA, flowID)
	s.exec(`INSERT INTO flow_runs(id, tenant_id, flow_id, flow_version_id, conversation_id, status, trigger_event_id) VALUES($1,$2,$3,$4,$5,'running',$6)`, runID, s.env.TenantA, flowID, versionID, conv, uuid.NewString())
	return runID
}

func TestAFlowStepRunTwiceInParallelMakesOneTaskAndAChangedStepIsRefused(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	run := s.flowRun(conv)
	seq := 7
	mk := func(title string) (res application.TaskResult, err error) {
		err = s.trySystem(s.env.TenantA, func(ctx context.Context) (e error) {
			res, e = s.svc.CreateTask(ctx, domain.TaskOpenInput{Title: title, OriginType: domain.OriginFlow, FlowRunID: &run, FlowSeq: &seq})
			return
		})
		return
	}
	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, ids := 0, map[uuid.UUID]bool{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := mk("Retornar ao cliente")
			if err != nil {
				t.Errorf("parallel step: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if r.Created {
				created++
			}
			ids[r.Task.ID] = true
		}()
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("one task for the step: created=%d ids=%d", created, len(ids))
	}
	if _, err := mk("Outra coisa"); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("the same step with other content is refused: %v", err)
	}
	other := 8
	r2, err := s.createTaskSystem(domain.TaskOpenInput{Title: "Retornar ao cliente", OriginType: domain.OriginFlow, FlowRunID: &run, FlowSeq: &other})
	if err != nil || !r2.Created {
		t.Fatalf("another step of the same run is another task: %+v %v", r2, err)
	}
	// a person cannot claim to be a flow, with or without a run, and cannot hand a flow task a key of their own
	if _, err := s.createTask(s.agent, domain.TaskOpenInput{Title: "x", OriginType: domain.OriginFlow, FlowRunID: &run, FlowSeq: &seq}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a person claiming to be a flow: %v", err)
	}
	if _, err := s.createTaskSystem(domain.TaskOpenInput{Title: "x", OriginType: domain.OriginFlow, FlowRunID: &run, FlowSeq: &seq, IdempotencyKey: "own-key-1234"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a flow task takes its idempotency from the step: %v", err)
	}
	// the database backstops it with the unique index and refuses a person writing a flow task by plain SQL
	if err := s.trySystem(s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO service_tasks(tenant_id, title, origin_type, flow_run_id, flow_seq) VALUES($1,'dup','flow',$2,$3)`, s.env.TenantA, run, seq)
	}); err == nil {
		t.Error("the unique (run, step) index must refuse a second task for the step")
	}
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO service_tasks(tenant_id, title, origin_type, flow_run_id, flow_seq) VALUES($1,'forged','flow',$2,99)`, s.env.TenantA, run)
	}); err == nil {
		t.Error("a person cannot write a flow task by SQL")
	}
}

func (s *stack) createTaskSystem(in domain.TaskOpenInput) (res application.TaskResult, err error) {
	err = s.trySystem(s.env.TenantA, func(ctx context.Context) (e error) {
		res, e = s.svc.CreateTask(ctx, in)
		return
	})
	return
}

// ---- the facts of finishing a case or a task cannot be edited on their own ---------------------------------------------------------------------

func TestResolutionAndCompletionFactsCannotBeEditedOutsideAStatusChange(t *testing.T) {
	s := newStack(t)
	cs := s.mustCase(s.admin, "Caso", nil)
	resolved, err := s.setCaseStatus(s.admin, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "Trocado o roteador."}, cs.Version)
	if err != nil {
		t.Fatal(err)
	}
	task := s.mustTask(s.agent, "Tarefa", nil, nil)
	done, err := s.act(s.agent, task.ID, application.TaskComplete, "", task.Version)
	if err != nil {
		t.Fatal(err)
	}
	cancelledTask := s.mustTask(s.agent, "Outra", nil, nil)
	if _, err := s.act(s.agent, cancelledTask.ID, application.TaskCancel, "não é mais necessária", cancelledTask.Version); err != nil {
		t.Fatal(err)
	}
	// people who hold service.update or service.assign (but not service.close) try to rewrite those facts with plain SQL
	for name, stmt := range map[string]string{
		"resolution text":  fmt.Sprintf(`UPDATE service_cases SET resolution='reescrita' WHERE id='%s'`, resolved.ID),
		"resolved_at":      fmt.Sprintf(`UPDATE service_cases SET resolved_at = resolved_at - interval '1 day' WHERE id='%s'`, resolved.ID),
		"completed_at":     fmt.Sprintf(`UPDATE service_tasks SET completed_at = completed_at - interval '1 day' WHERE id='%s'`, done.ID),
		"completed_by":     fmt.Sprintf(`UPDATE service_tasks SET completed_by_user_id='%s' WHERE id='%s'`, s.admin, done.ID),
		"cancel_reason":    fmt.Sprintf(`UPDATE service_tasks SET cancel_reason='outra razão' WHERE cancel_reason IS NOT NULL AND tenant_id='%s'`, s.env.TenantA),
		"edit a done task": fmt.Sprintf(`UPDATE service_tasks SET title='mudado depois de pronta' WHERE id='%s'`, done.ID),
	} {
		for _, who := range []uuid.UUID{s.agent, s.limited, s.admin} {
			if err := s.try(who, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, stmt) }); err == nil {
				t.Errorf("%s: refused for everybody, including the person who holds every key", name)
				break
			}
		}
	}
	// completing in somebody else's name is refused, even by somebody who may complete
	t2 := s.mustTask(s.agent, "Terceira", nil, nil)
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_tasks SET status='done', completed_at=clock_timestamp(), completed_by_user_id=$2 WHERE id=$1`, t2.ID, s.admin)
	}); err == nil {
		t.Error("a task is completed in the name of whoever completes it")
	}
	// a future closing is refused
	t3 := s.mustTask(s.agent, "Quarta", nil, nil)
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_tasks SET status='done', completed_at=now() + interval '1 day', completed_by_user_id=$2 WHERE id=$1`, t3.ID, s.agent)
	}); err == nil {
		t.Error("a task cannot be completed in the future")
	}
}

func TestReopeningAndCancellingNeedAReasonInTheDatabaseToo(t *testing.T) {
	s := newStack(t)
	cs := s.mustCase(s.admin, "Caso", nil)
	resolved, _ := s.setCaseStatus(s.admin, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "ok"}, cs.Version)
	task := s.mustTask(s.agent, "Tarefa", nil, nil)
	done, _ := s.act(s.agent, task.ID, application.TaskComplete, "", task.Version)
	for name, stmt := range map[string]string{
		"reopen a case without a reason":    fmt.Sprintf(`UPDATE service_cases SET status='investigating', resolution=NULL, resolved_at=NULL, reopen_count=reopen_count+1 WHERE id='%s'`, resolved.ID),
		"reopen a case with a blank reason": fmt.Sprintf(`UPDATE service_cases SET status='investigating', resolution=NULL, resolved_at=NULL, reopen_count=reopen_count+1, last_reopen_reason='   ' WHERE id='%s'`, resolved.ID),
		"reopen a task without a reason":    fmt.Sprintf(`UPDATE service_tasks SET status='open', completed_at=NULL, completed_by_user_id=NULL, reopen_count=reopen_count+1 WHERE id='%s'`, done.ID),
		"reopen a task with a blank reason": fmt.Sprintf(`UPDATE service_tasks SET status='open', completed_at=NULL, completed_by_user_id=NULL, reopen_count=reopen_count+1, last_reopen_reason='   ' WHERE id='%s'`, done.ID),
		"move the reason without reopening": fmt.Sprintf(`UPDATE service_cases SET last_reopen_reason='por nada' WHERE id='%s'`, resolved.ID),
	} {
		if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, stmt) }); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	// a blank cancellation reason is refused by the column itself
	fresh := s.mustTask(s.agent, "Cancelar", nil, nil)
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_tasks SET status='cancelled', cancelled_at=clock_timestamp(), cancel_reason='     ' WHERE id=$1`, fresh.ID)
	}); err == nil {
		t.Error("a blank cancellation reason must be refused")
	}
	// and the reasons given through the application are kept where the database can see them
	again, err := s.act(s.agent, done.ID, application.TaskReopen, "faltou testar", done.Version)
	if err != nil || again.LastReopenReason == nil || *again.LastReopenReason != "faltou testar" {
		t.Fatalf("reopen with a reason: %+v %v", again, err)
	}
}

// ---- who may be put in charge ------------------------------------------------------------------------------------------------------------------

func TestOnlyAnActiveMemberOfTheCompanyCanBePutInCharge(t *testing.T) {
	s := newStack(t)
	task := s.mustTask(s.agent, "Tarefa", nil, nil)
	cs := s.mustCase(s.agent, "Caso", nil)
	// a user of ANOTHER company
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_tasks SET assigned_user_id=$2 WHERE id=$1`, task.ID, s.env.UserB)
	}); err == nil || !strings.Contains(err.Error(), "active member") {
		t.Fatalf("assigning a task to somebody from another company: %v", err)
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_cases SET assigned_agent_id=$2 WHERE id=$1`, cs.ID, s.env.UserB)
	}); err == nil || !strings.Contains(err.Error(), "active member") {
		t.Fatalf("assigning a case to somebody from another company: %v", err)
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO service_tasks(tenant_id, title, origin_type, assigned_user_id) VALUES($1,'x','human',$2)`, s.env.TenantA, s.env.UserB)
	}); err == nil {
		t.Fatal("creating a task already in the charge of an outsider")
	}
	// a revoked member is no longer a valid person in charge
	s.exec(`UPDATE memberships SET status='revoked' WHERE tenant_id=$1 AND user_id=$2`, s.env.TenantA, s.limited)
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_tasks SET assigned_user_id=$2 WHERE id=$1`, task.ID, s.limited)
	}); err == nil {
		t.Fatal("a revoked member cannot be put in charge")
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_tasks SET assigned_user_id=$2 WHERE id=$1`, task.ID, s.agent)
	}); err != nil {
		t.Fatalf("an active member can: %v", err)
	}
	// changing something else on a task whose person was later revoked is not blocked by that
	s.exec(`UPDATE memberships SET status='revoked' WHERE tenant_id=$1 AND user_id=$2`, s.env.TenantA, s.agent)
	title := "novo título"
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		_, e := s.svc.PatchTask(ctx, task.ID, domain.TaskPatchInput{Title: &title}, 2)
		return e
	}); err != nil {
		t.Fatalf("a task of a person who left can still be edited: %v", err)
	}
}

// ---- events -----------------------------------------------------------------------------------------------------------------------------------

func TestADedupKeyIsOnlyTheSameFactIfEverythingMatches(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	conv2, _ := s.conversation()
	other := s.mustOpen(s.agent, conv2)
	repo := adapters.NewPostgresRepository(s.env.App)
	base := func() domain.ContextEvent {
		return domain.ContextEvent{ID: uuid.New(), TenantID: s.env.TenantA, EventType: "service_session.classified", EntityType: "service_session", EntityID: sess.ID,
			SessionID: &sess.ID, ActorType: "user", ActorID: &s.agent, OccurredAt: time.Now().UTC(), RecordedAt: time.Now().UTC(), SchemaVersion: 1,
			Payload: map[string]any{"k": 1}, Note: "n", DedupKey: "full-fact-key-1"}
	}
	rec := func(e domain.ContextEvent) error {
		return s.try(s.agent, s.env.TenantA, func(ctx context.Context) error { return repo.Record(ctx, e) })
	}
	if err := rec(base()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*domain.ContextEvent){
		"another session": func(e *domain.ContextEvent) { e.SessionID = &other.ID },
		"another contact": func(e *domain.ContextEvent) { id := uuid.New(); e.ContactID = &id },
		"another note":    func(e *domain.ContextEvent) { e.Note = "outra" },
		"another entity":  func(e *domain.ContextEvent) { e.EntityType = "service_case" },
		"another actor": func(e *domain.ContextEvent) {
			e.ActorID = &s.agent
			e.ActorType = "user"
			e.Payload = map[string]any{"k": 2}
		},
	} {
		e := base()
		mutate(&e)
		if err := rec(e); !errors.Is(err, domain.ErrIdempotencyMismatch) && err == nil {
			t.Errorf("%s under the same key must not pass silently", name)
		}
	}
	if err := rec(base()); err != nil {
		t.Fatalf("the identical fact again is fine: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM context_events WHERE dedup_key='full-fact-key-1' AND tenant_id=$1`, s.env.TenantA); n != 1 {
		t.Fatalf("one fact: %d", n)
	}
	// the shape: a person's fact names the person
	if err := s.trySystem(s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO context_events(id, tenant_id, event_type, entity_type, entity_id, session_id, actor_type, actor_id, occurred_at) VALUES(gen_random_uuid(),$1,'service_session.closed','service_session',$2,$2,'user',NULL, now())`, s.env.TenantA, sess.ID)
	}); err == nil {
		t.Error("an event of actor_type=user without a person must be refused, even from a system session")
	}
}

// ---- HTTP details -----------------------------------------------------------------------------------------------------------------------------

func TestHTTPStrictQueryParametersAndTheStatusRouteKey(t *testing.T) {
	s := newStack(t)
	mux := s.mux()
	for _, q := range []string{"/service-cases?working=maybe", "/service-tasks?pending=1", "/service-tasks?overdue=yes", "/service-cases?limit=0", "/service-tasks?limit=-3", "/service-cases?limit=abc", "/external-records?limit=9999"} {
		if r := s.call(mux, s.agent, "GET", q, ""); r.code != http.StatusBadRequest {
			t.Errorf("%s must be a 400, got %d %v", q, r.code, r.body)
		}
	}
	if r := s.call(mux, s.agent, "GET", "/service-cases?working=true&limit=5", ""); r.code != http.StatusOK {
		t.Errorf("valid parameters: %d", r.code)
	}
	// the case-status route is open to whoever holds service.update (the weakest key of the route); the exact key is asked of each move
	r := s.call(mux, s.limited, "POST", "/service-cases", `{"title":"caso do limitado"}`)
	if r.code != http.StatusCreated {
		t.Fatal(r.code, r.body)
	}
	id := r.body["id"].(string)
	if r := s.call(mux, s.limited, "POST", "/service-cases/"+id+"/status", `{"status":"investigating"}`, "If-Match", `"1"`); r.code != http.StatusOK {
		t.Fatalf("a working-state move needs only service.update: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.limited, "POST", "/service-cases/"+id+"/status", `{"status":"resolved","resolution":"ok"}`, "If-Match", `"2"`); r.code != http.StatusForbidden {
		t.Fatalf("resolving needs service.close: %d %v", r.code, r.body)
	}
}
