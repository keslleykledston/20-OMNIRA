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
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

func (s *stack) openCase(user uuid.UUID, in domain.CaseOpenInput) (res application.CaseResult, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) (e error) {
		res, e = s.svc.OpenCase(ctx, in)
		return
	})
	return res, err
}

func (s *stack) mustCase(user uuid.UUID, title string, session *uuid.UUID) *domain.Case {
	s.t.Helper()
	res, err := s.openCase(user, domain.CaseOpenInput{Title: title, SessionID: session})
	if err != nil {
		s.t.Fatal(err)
	}
	return res.Case
}

func (s *stack) createTask(user uuid.UUID, in domain.TaskOpenInput) (res application.TaskResult, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) (e error) {
		res, e = s.svc.CreateTask(ctx, in)
		return
	})
	return res, err
}

func (s *stack) mustTask(user uuid.UUID, title string, session, cs *uuid.UUID) *domain.Task {
	s.t.Helper()
	res, err := s.createTask(user, domain.TaskOpenInput{Title: title, SessionID: session, CaseID: cs})
	if err != nil {
		s.t.Fatal(err)
	}
	return res.Task
}

func (s *stack) setCaseStatus(user, id uuid.UUID, in domain.CaseStatusInput, version int) (c *domain.Case, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) (e error) {
		c, e = s.svc.SetCaseStatus(ctx, id, in, version)
		return
	})
	return c, err
}

func (s *stack) act(user, id uuid.UUID, a application.TaskAction, reason string, version int) (t *domain.Task, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) (e error) {
		t, e = s.svc.ActTask(ctx, id, a, reason, version)
		return
	})
	return t, err
}

func (s *stack) getCase(id uuid.UUID) (c *domain.Case) {
	s.t.Helper()
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		var err error
		if c, err = s.svc.GetCase(ctx, id); err != nil {
			s.t.Fatal(err)
		}
	})
	return c
}

// ---- Gate 3 -------------------------------------------------------------------------------------------------------------------------------

// 1. an attendance that only leaves history: no case, no task, nothing pending
func TestGate3AnAttendanceThatGeneratesOnlyHistory(t *testing.T) {
	s := newStack(t)
	conv, contact := s.conversation()
	s.assign(conv, s.agent)
	if err := s.finalize(s.attendance(true), s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	rows := s.sessionsOf(conv)
	if len(rows) != 1 || rows[0].canonical != domain.ResInformationProvided {
		t.Fatalf("history only: %+v", rows)
	}
	if s.count(`SELECT count(*) FROM service_cases WHERE tenant_id=$1`, s.env.TenantA) != 0 || s.count(`SELECT count(*) FROM service_tasks WHERE tenant_id=$1`, s.env.TenantA) != 0 {
		t.Fatal("no case and no task were made for a conversation by itself")
	}
	var cc *application.ContactContext
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) { cc, e = s.svc.ContactContext(ctx, contact, nil); return }); err != nil {
		t.Fatal(err)
	}
	if len(cc.OpenCases) != 0 || len(cc.PendingTasks) != 0 {
		t.Fatalf("nothing pending: %+v", cc)
	}
}

// 2. an attendance that generates a task: the task outlives the attendance and the result says so
func TestGate3AnAttendanceThatGeneratesATask(t *testing.T) {
	s := newStack(t)
	conv, contact := s.conversation()
	s.assign(conv, s.agent)
	sess := s.mustOpen(s.agent, conv)
	due := time.Now().UTC().Add(48 * time.Hour)
	res, err := s.createTask(s.agent, domain.TaskOpenInput{Title: "Ligar com o resultado da visita", SessionID: &sess.ID, DueAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	task := res.Task
	if task.OriginType != domain.OriginHuman || task.SessionID == nil || *task.SessionID != sess.ID || task.CaseID != nil || task.ContactID == nil || *task.ContactID != contact {
		t.Fatalf("a task needs no case; it belongs to the session and the contact: %+v", task)
	}
	if err := s.finalize(s.attendance(true), s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	rows := s.sessionsOf(conv)
	if len(rows) != 1 || rows[0].status != "closed" || rows[0].canonical != domain.ResTaskCreated {
		t.Fatalf("the result says a task came out of it: %+v", rows)
	}
	after := func() *domain.Task {
		var got *domain.Task
		s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
			var e error
			if got, e = s.svc.GetTask(ctx, task.ID); e != nil {
				t.Fatal(e)
			}
		})
		return got
	}
	if got := after(); got.Status != domain.TaskOpen || got.Version != 1 {
		t.Fatalf("ending the attendance leaves the task open and untouched: %+v", got)
	}
	var cc *application.ContactContext
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) { cc, e = s.svc.ContactContext(ctx, contact, nil); return }); err != nil {
		t.Fatal(err)
	}
	if len(cc.PendingTasks) != 1 || cc.PendingTasks[0].ID != task.ID {
		t.Fatalf("the next attendant sees it: %+v", cc.PendingTasks)
	}
}

// 3. an attendance that generates a case: the case outlives it
func TestGate3AnAttendanceThatGeneratesACase(t *testing.T) {
	s := newStack(t)
	conv, contact := s.conversation()
	s.assign(conv, s.agent)
	sess := s.mustOpen(s.agent, conv)
	cs := s.mustCase(s.agent, "Link instável desde segunda", &sess.ID)
	if cs.CaseNumber != "OC-1000" || cs.Status != domain.CaseOpen || cs.OriginSessionID == nil || *cs.OriginSessionID != sess.ID {
		t.Fatalf("case: %+v", cs)
	}
	if err := s.finalize(s.attendance(true), s.agent, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	rows := s.sessionsOf(conv)
	if rows[0].canonical != domain.ResCaseCreated {
		t.Fatalf("result: %+v", rows)
	}
	if got := s.getCase(cs.ID); got.Status != domain.CaseOpen || got.Version != 1 {
		t.Fatalf("closing the attendance never closes the case: %+v", got)
	}
	var cc *application.ContactContext
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) { cc, e = s.svc.ContactContext(ctx, contact, nil); return }); err != nil {
		t.Fatal(err)
	}
	if len(cc.OpenCases) != 1 || cc.OpenCases[0].ID != cs.ID {
		t.Fatalf("the open case is in the contact's context: %+v", cc.OpenCases)
	}
}

// 4. several attendances refer to the same case
func TestGate3SeveralAttendancesShareOneCase(t *testing.T) {
	s := newStack(t)
	conv1, contact := s.conversation()
	sess1 := s.mustOpen(s.agent, conv1)
	cs := s.mustCase(s.agent, "Fatura em duplicidade", &sess1.ID)
	// the customer calls again: a second attendance (another conversation of the same contact is not needed; a phone call logged through the API will do)
	second, err := s.open(s.agent, domain.OpenInput{ContactID: &contact, OriginChannel: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	var linked bool
	link := func() error {
		return s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) {
			linked, e = s.svc.LinkSessionToCase(ctx, cs.ID, second.Session.ID)
			return
		})
	}
	if err := link(); err != nil || !linked {
		t.Fatalf("link: %v %v", linked, err)
	}
	if err := link(); err != nil || linked {
		t.Fatalf("linking twice changes nothing: %v %v", linked, err)
	}
	var sessions []domain.Session
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		var e error
		if sessions, e = s.svc.CaseSessions(ctx, cs.ID); e != nil {
			t.Fatal(e)
		}
	})
	if len(sessions) != 2 {
		t.Fatalf("both attendances refer to the case: %d", len(sessions))
	}
	if n := s.count(`SELECT count(*) FROM service_case_sessions WHERE case_id=$1 AND relation='origin'`, cs.ID); n != 1 {
		t.Fatalf("exactly one origin: %d", n)
	}
	// ending either attendance leaves the case alone
	if _, err := s.closeSession(s.agent, second.Session.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, 1); err != nil {
		t.Fatal(err)
	}
	if got := s.getCase(cs.ID); got.Status != domain.CaseOpen {
		t.Fatalf("case after an attendance ended: %+v", got)
	}
	// the case timeline names the second attendance by protocol, and the case also shows in the first session's timeline
	var items []ports.TimelineItem
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		var e error
		if items, _, e = s.svc.CaseTimeline(ctx, cs.ID, "", 50, true); e != nil {
			t.Fatal(e)
		}
	})
	if fmt.Sprint(types(items)) != "[service_case.opened service_case.session_linked]" || items[1].Payload["protocol"] != second.Session.Protocol {
		t.Fatalf("case timeline: %v", types(items))
	}
	first, _ := s.timeline(s.agent, sess1.ID, true, 50)
	if !strings.Contains(fmt.Sprint(types(first)), "service_case.opened") {
		t.Fatalf("the session's timeline shows the case it opened: %v", types(first))
	}
	// a customer's case is never tied to a session of another contact by accident
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error {
		_, e := s.svc.LinkSessionToCase(ctx, cs.ID, second.Session.ID)
		return e
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another company cannot link: %v", err)
	}
}

// 5. finishing a task does not resolve the case
func TestGate3CompletingATaskDoesNotResolveTheCase(t *testing.T) {
	s := newStack(t)
	cs := s.mustCase(s.agent, "Instalação pendente", nil)
	task := s.mustTask(s.agent, "Agendar a visita", nil, &cs.ID)
	done, err := s.act(s.agent, task.ID, application.TaskComplete, "", task.Version)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != domain.TaskDone || done.CompletedAt == nil || done.CompletedBy == nil || *done.CompletedBy != s.agent {
		t.Fatalf("done: %+v", done)
	}
	if got := s.getCase(cs.ID); got.Status != domain.CaseOpen || got.Version != cs.Version || got.ResolvedAt != nil {
		t.Fatalf("the case is exactly as it was: %+v", got)
	}
	// and a case resolved/closed/cancelled leaves its tasks alone too
	t2 := s.mustTask(s.agent, "Cobrar retorno", nil, &cs.ID)
	res, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "Visita feita, link estável."}, cs.Version)
	if err != nil {
		t.Fatal(err)
	}
	var again *domain.Task
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { again, _ = s.svc.GetTask(ctx, t2.ID) })
	if res.Status != domain.CaseResolved || again.Status != domain.TaskOpen || again.Version != t2.Version {
		t.Fatalf("resolving a case never completes its tasks: case=%s task=%+v", res.Status, again)
	}
}

// ---- rules -----------------------------------------------------------------------------------------------------------------------------------

func TestCaseStateMachine(t *testing.T) {
	ok := map[[2]domain.CaseStatus]bool{
		{domain.CaseOpen, domain.CaseInvestigating}: true, {domain.CaseOpen, domain.CaseWaiting}: true, {domain.CaseOpen, domain.CaseResolved}: true, {domain.CaseOpen, domain.CaseCancelled}: true,
		{domain.CaseInvestigating, domain.CaseWaiting}: true, {domain.CaseInvestigating, domain.CaseResolved}: true, {domain.CaseInvestigating, domain.CaseCancelled}: true,
		{domain.CaseWaiting, domain.CaseInvestigating}: true, {domain.CaseWaiting, domain.CaseResolved}: true, {domain.CaseWaiting, domain.CaseCancelled}: true,
		{domain.CaseResolved, domain.CaseClosed}: true, {domain.CaseResolved, domain.CaseInvestigating}: true,
	}
	all := []domain.CaseStatus{domain.CaseOpen, domain.CaseInvestigating, domain.CaseWaiting, domain.CaseResolved, domain.CaseClosed, domain.CaseCancelled}
	for _, a := range all {
		for _, b := range all {
			if got := domain.CanCaseTransition(a, b); got != ok[[2]domain.CaseStatus{a, b}] {
				t.Errorf("%s -> %s = %v", a, b, got)
			}
		}
	}
	taskOK := map[[2]domain.TaskStatus]bool{
		{domain.TaskOpen, domain.TaskInProgress}: true, {domain.TaskOpen, domain.TaskDone}: true, {domain.TaskOpen, domain.TaskCancelled}: true,
		{domain.TaskInProgress, domain.TaskOpen}: true, {domain.TaskInProgress, domain.TaskDone}: true, {domain.TaskInProgress, domain.TaskCancelled}: true, {domain.TaskDone, domain.TaskOpen}: true,
	}
	tall := []domain.TaskStatus{domain.TaskOpen, domain.TaskInProgress, domain.TaskDone, domain.TaskCancelled}
	for _, a := range tall {
		for _, b := range tall {
			if got := domain.CanTaskTransition(a, b); got != taskOK[[2]domain.TaskStatus{a, b}] {
				t.Errorf("task %s -> %s = %v", a, b, got)
			}
		}
	}
}

func TestACaseNeedsItsResolutionItsReasonsAndEndsFinal(t *testing.T) {
	s := newStack(t)
	cs := s.mustCase(s.agent, "Caso", nil)
	v := cs.Version
	if _, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved}, v); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("resolving without the resolution: %v", err)
	}
	if _, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseCancelled}, v); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("cancelling without a reason: %v", err)
	}
	if _, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseClosed}, v); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("closing a case that was never resolved: %v", err)
	}
	if _, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "ok"}, 0); !errors.Is(err, domain.ErrPreconditionNeeded) {
		t.Fatalf("no version: %v", err)
	}
	if _, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "ok"}, v+3); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	resolved, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "Trocado o roteador."}, v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseInvestigating}, resolved.Version); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("reopening needs a reason: %v", err)
	}
	reopened, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseInvestigating, Reason: "o problema voltou"}, resolved.Version)
	if err != nil || reopened.Status != domain.CaseInvestigating || reopened.ReopenCount != 1 || reopened.Resolution != nil || reopened.ResolvedAt != nil {
		t.Fatalf("reopened: %+v %v", reopened, err)
	}
	r2, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "De novo."}, reopened.Version)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseClosed}, r2.Version)
	if err != nil || closed.ClosedAt == nil || closed.Resolution == nil {
		t.Fatalf("closed: %+v %v", closed, err)
	}
	// closed is final: for the application and for the database
	if _, err := s.setCaseStatus(s.agent, cs.ID, domain.CaseStatusInput{To: domain.CaseInvestigating, Reason: "volta"}, closed.Version); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("closed is final: %v", err)
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_cases SET title='x' WHERE id=$1`, cs.ID)
	}); err == nil {
		t.Fatal("the database refuses to touch a closed case")
	}
	// cancelling
	c2 := s.mustCase(s.agent, "Outro", nil)
	cancelled, err := s.setCaseStatus(s.agent, c2.ID, domain.CaseStatusInput{To: domain.CaseCancelled, Reason: "aberto em duplicidade"}, c2.Version)
	if err != nil || cancelled.CancelReason == nil || cancelled.CancelledAt == nil {
		t.Fatalf("cancelled: %+v %v", cancelled, err)
	}
	var note string
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT note FROM context_events WHERE case_id=$1 AND event_type='service_case.status_changed'`, c2.ID).Scan(&note); err != nil || note != "aberto em duplicidade" {
		t.Fatalf("the reason is in the history: %q %v", note, err)
	}
}

func TestATaskNeedsAReasonToBeCancelledAndCancelledIsFinal(t *testing.T) {
	s := newStack(t)
	task := s.mustTask(s.agent, "Tarefa", nil, nil)
	if _, err := s.act(s.agent, task.ID, application.TaskCancel, "", task.Version); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := s.act(s.agent, task.ID, application.TaskCancel, "ab", task.Version); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a reason too short: %v", err)
	}
	cancelled, err := s.act(s.agent, task.ID, application.TaskCancel, "o cliente desistiu", task.Version)
	if err != nil || cancelled.Status != domain.TaskCancelled || cancelled.CancelReason == nil {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	if _, err := s.act(s.agent, task.ID, application.TaskReopen, "engano", cancelled.Version); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("cancelled is final: %v", err)
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_tasks SET status='open', cancelled_at=NULL, cancel_reason=NULL WHERE id=$1`, task.ID)
	}); err == nil {
		t.Fatal("the database refuses to bring a cancelled task back")
	}
	// done can be reopened with a reason
	t2 := s.mustTask(s.agent, "Outra", nil, nil)
	done, _ := s.act(s.agent, t2.ID, application.TaskComplete, "", t2.Version)
	again, err := s.act(s.agent, t2.ID, application.TaskReopen, "faltou testar", done.Version)
	if err != nil || again.Status != domain.TaskOpen || again.CompletedAt != nil || again.ReopenCount != 1 {
		t.Fatalf("reopen: %+v %v", again, err)
	}
}

func TestIdempotentCreationOfCasesAndTasks(t *testing.T) {
	s := newStack(t)
	key := "case-" + uuid.NewString()
	first, err := s.openCase(s.agent, domain.CaseOpenInput{Title: "Caso A", IdempotencyKey: key})
	if err != nil || !first.Created {
		t.Fatal(first, err)
	}
	again, err := s.openCase(s.agent, domain.CaseOpenInput{Title: "Caso A", IdempotencyKey: key})
	if err != nil || again.Created || again.Case.ID != first.Case.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if _, err := s.openCase(s.agent, domain.CaseOpenInput{Title: "Caso B", IdempotencyKey: key}); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("same key, other request: %v", err)
	}
	tkey := "task-" + uuid.NewString()
	t1, err := s.createTask(s.agent, domain.TaskOpenInput{Title: "T", IdempotencyKey: tkey})
	t2, err2 := s.createTask(s.agent, domain.TaskOpenInput{Title: "T", IdempotencyKey: tkey})
	if err != nil || err2 != nil || !t1.Created || t2.Created || t1.Task.ID != t2.Task.ID {
		t.Fatalf("task replay: %+v %+v %v %v", t1, t2, err, err2)
	}
	// in parallel: one case, ten callers
	pkey := "pcase-" + uuid.NewString()
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, ids := 0, map[uuid.UUID]bool{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.openCase(s.agent, domain.CaseOpenInput{Title: "Paralelo", IdempotencyKey: pkey})
			if err != nil {
				t.Errorf("parallel: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if r.Created {
				created++
			}
			ids[r.Case.ID] = true
		}()
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("one case: created=%d ids=%d", created, len(ids))
	}
}

func TestCaseNumbersAreDensePerCompanyAndIndependentOfSessions(t *testing.T) {
	s := newStack(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.openCase(s.agent, domain.CaseOpenInput{Title: fmt.Sprintf("Caso %d", i)}); err != nil {
				t.Errorf("open: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if n := s.count(`SELECT count(DISTINCT case_sequence) FROM service_cases WHERE tenant_id=$1 AND case_sequence BETWEEN 1000 AND 1011`, s.env.TenantA); n != 12 {
		t.Fatalf("dense, unique numbers OC-1000..OC-1011: %d", n)
	}
	// the sessions' counter is separate: the first session is still number 1001
	conv, _ := s.conversation()
	if sess := s.mustOpen(s.agent, conv); sess.Protocol != s.proto(s.env.TenantA, 1001) {
		t.Fatalf("session protocol: %s", sess.Protocol)
	}
}

func TestAFlowStepReplayedReturnsTheTaskItAlreadyMade(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	var flowID, versionID, runID uuid.UUID
	flowID, versionID, runID = uuid.New(), uuid.New(), uuid.New()
	s.exec(`INSERT INTO flows(id, tenant_id, slug, name) VALUES($1,$2,'svc-test','Fluxo de teste')`, flowID, s.env.TenantA)
	s.exec(`INSERT INTO flow_versions(id, tenant_id, flow_id, version, definition, definition_hash) VALUES($1,$2,$3,1,'{}','h')`, versionID, s.env.TenantA, flowID)
	s.exec(`INSERT INTO flow_runs(id, tenant_id, flow_id, flow_version_id, conversation_id, status, trigger_event_id) VALUES($1,$2,$3,$4,$5,'running',$6)`, runID, s.env.TenantA, flowID, versionID, conv, uuid.NewString())
	seq := 3
	mk := func() (res application.TaskResult, err error) {
		s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
			res, err = s.svc.CreateTask(ctx, domain.TaskOpenInput{Title: "Criada pelo fluxo", OriginType: domain.OriginFlow, FlowRunID: &runID, FlowSeq: &seq, ContactID: nil})
		})
		return
	}
	a, err := mk()
	b, err2 := mk()
	if err != nil || err2 != nil || !a.Created || b.Created || a.Task.ID != b.Task.ID || a.Task.OriginType != domain.OriginFlow || a.Task.CreatedBy != nil {
		t.Fatalf("flow replay: %+v %+v %v %v", a, b, err, err2)
	}
	// a person cannot claim to be a flow, nor a flow omit its step
	if _, err := s.createTask(s.agent, domain.TaskOpenInput{Title: "x", OriginType: domain.OriginFlow}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("flow origin without a run: %v", err)
	}
	if _, err := s.createTask(s.agent, domain.TaskOpenInput{Title: "x", FlowRunID: &runID, FlowSeq: &seq}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a run without the flow origin: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM service_tasks WHERE flow_run_id=$1`, runID); n != 1 {
		t.Fatalf("one task for the step: %d", n)
	}
}

// ---- keys, in the application and in the database ------------------------------------------------------------------------------------------

func TestAssignmentNeedsItsOwnKey(t *testing.T) {
	s := newStack(t)
	// the attendant (no service.assign) may create for themselves or unassigned, never for somebody else
	if _, err := s.createTask(s.agent, domain.TaskOpenInput{Title: "para mim", AssignedUserID: &s.agent}); err != nil {
		t.Fatalf("self assignment at creation: %v", err)
	}
	if _, err := s.createTask(s.agent, domain.TaskOpenInput{Title: "para outro", AssignedUserID: &s.admin}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("assigning to somebody else without service.assign: %v", err)
	}
	if res, err := s.createTask(s.admin, domain.TaskOpenInput{Title: "para o agente", AssignedUserID: &s.agent}); err != nil || res.Task.AssignedUserID == nil {
		t.Fatalf("the administrator assigns: %v", err)
	}
	// a person who is not a member cannot be assigned
	stranger := uuid.New()
	if _, err := s.createTask(s.admin, domain.TaskOpenInput{Title: "para fora", AssignedUserID: &stranger}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("assigning to a non-member: %v", err)
	}
	task := s.mustTask(s.agent, "depois", nil, nil)
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		_, e := s.svc.AssignTask(ctx, task.ID, &s.agent, nil, task.Version)
		return e
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reassigning later needs service.assign: %v", err)
	}
	// the database says the same for raw SQL
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_tasks SET assigned_user_id=$2 WHERE id=$1`, task.ID, s.admin)
	}); err == nil || !strings.Contains(err.Error(), "service.assign") {
		t.Fatalf("the row guard asks service.assign: %v", err)
	}
	cs := s.mustCase(s.agent, "Caso", nil)
	if _, err := s.openCase(s.agent, domain.CaseOpenInput{Title: "com time", AssignedAgentID: &s.agent}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a case opened with an assignee needs service.assign: %v", err)
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		_, e := s.svc.AssignCase(ctx, cs.ID, nil, &s.agent, cs.Version)
		return e
	}); err != nil {
		t.Fatalf("the administrator assigns a case: %v", err)
	}
}

func TestClosingAndCompletingNeedServiceClose(t *testing.T) {
	s := newStack(t)
	task := s.mustTask(s.limited, "tarefa", nil, nil) // limited holds read/create/update, NOT close
	if _, err := s.act(s.limited, task.ID, application.TaskComplete, "", task.Version); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("complete without service.close: %v", err)
	}
	if started, err := s.act(s.limited, task.ID, application.TaskStart, "", task.Version); err != nil || started.Status != domain.TaskInProgress {
		t.Fatalf("start needs only service.update: %v", err)
	}
	cs := s.mustCase(s.limited, "caso", nil)
	if _, err := s.setCaseStatus(s.limited, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "ok"}, cs.Version); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("resolve without service.close: %v", err)
	}
	if inv, err := s.setCaseStatus(s.limited, cs.ID, domain.CaseStatusInput{To: domain.CaseInvestigating}, cs.Version); err != nil || inv.Status != domain.CaseInvestigating {
		t.Fatalf("investigating needs only service.update: %v", err)
	}
	// raw SQL as the same person is refused by the database
	for _, stmt := range []string{
		`UPDATE service_tasks SET status='done', completed_at=now() WHERE id=$1`,
	} {
		if err := s.try(s.limited, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, stmt, task.ID) }); err == nil || !strings.Contains(err.Error(), "service.close") {
			t.Fatalf("the row guard asks service.close: %v", err)
		}
	}
	if err := s.try(s.limited, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_cases SET status='cancelled', cancelled_at=now(), cancel_reason='abc' WHERE id=$1`, cs.ID)
	}); err == nil || !strings.Contains(err.Error(), "service.close") {
		t.Fatalf("the case guard asks service.close: %v", err)
	}
	// an observer (no keys) reads nothing through the service and writes nothing through SQL
	if _, err := s.createTask(s.observer, domain.TaskOpenInput{Title: "x"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("observer: %v", err)
	}
	if err := s.try(s.observer, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO service_tasks(tenant_id, title, origin_type) VALUES($1,'x','human')`, s.env.TenantA)
	}); err == nil {
		t.Fatal("RLS refuses a task insert without service.create")
	}
}

func TestOptimisticConcurrencyOnTasksAndCases(t *testing.T) {
	s := newStack(t)
	task := s.mustTask(s.agent, "disputada", nil, nil)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, u := range []uuid.UUID{s.agent, s.admin} {
		wg.Add(1)
		go func(u uuid.UUID) {
			defer wg.Done()
			_, err := s.act(u, task.ID, application.TaskComplete, "", task.Version)
			results <- err
		}(u)
	}
	wg.Wait()
	close(results)
	won, lost := 0, 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrInvalidTransition):
			lost++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("exactly one completer wins: won=%d lost=%d", won, lost)
	}
	cs := s.mustCase(s.agent, "disputado", nil)
	title := "novo título"
	var wg2 sync.WaitGroup
	res2 := make(chan error, 2)
	for _, u := range []uuid.UUID{s.agent, s.admin} {
		wg2.Add(1)
		go func(u uuid.UUID) {
			defer wg2.Done()
			res2 <- s.try(u, s.env.TenantA, func(ctx context.Context) error {
				_, e := s.svc.PatchCase(ctx, cs.ID, domain.CasePatchInput{Title: &title}, cs.Version)
				return e
			})
		}(u)
	}
	wg2.Wait()
	close(res2)
	won, lost = 0, 0
	for err := range res2 {
		if err == nil {
			won++
		} else if errors.Is(err, domain.ErrConflict) {
			lost++
		} else {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("exactly one editor wins: won=%d lost=%d", won, lost)
	}
}

func TestDatabaseGuardsOfCasesAndTasks(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	cs := s.mustCase(s.agent, "caso", &sess.ID)
	task := s.mustTask(s.agent, "tarefa", &sess.ID, &cs.ID)
	ex := func(sql string, args ...any) error {
		_, err := s.env.Seed.Exec(context.Background(), sql, args...)
		return err
	}
	for name, stmt := range map[string]string{
		"case number is identity":    fmt.Sprintf(`UPDATE service_cases SET case_number='OC-9' WHERE id='%s'`, cs.ID),
		"case company is identity":   fmt.Sprintf(`UPDATE service_cases SET tenant_id='%s' WHERE id='%s'`, s.env.TenantB, cs.ID),
		"case origin is identity":    fmt.Sprintf(`UPDATE service_cases SET origin_session_id=NULL WHERE id='%s'`, cs.ID),
		"task origin is identity":    fmt.Sprintf(`UPDATE service_tasks SET origin_type='api' WHERE id='%s'`, task.ID),
		"task session is identity":   fmt.Sprintf(`UPDATE service_tasks SET session_id=NULL WHERE id='%s'`, task.ID),
		"task case is identity":      fmt.Sprintf(`UPDATE service_tasks SET case_id=NULL WHERE id='%s'`, task.ID),
		"a flow origin needs a run":  fmt.Sprintf(`UPDATE service_tasks SET origin_type='flow' WHERE id='%s'`, task.ID),
		"a done task needs a time":   fmt.Sprintf(`UPDATE service_tasks SET status='done' WHERE id='%s'`, task.ID),
		"a cancelled case needs why": fmt.Sprintf(`UPDATE service_cases SET status='cancelled', cancelled_at=now() WHERE id='%s'`, cs.ID),
		"a resolved case needs text": fmt.Sprintf(`UPDATE service_cases SET status='resolved', resolved_at=now() WHERE id='%s'`, cs.ID),
	} {
		if err := ex(stmt); err == nil {
			t.Errorf("%s: the database must refuse", name)
		}
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE service_case_sessions SET relation='reference' WHERE case_id=$1`, cs.ID)
	}); err == nil {
		t.Error("a case-session link is history: the application role cannot change it")
	}
	if err := ex(`INSERT INTO service_cases(tenant_id, case_number, case_sequence, title) VALUES($1,'bad number',5,'x')`, s.env.TenantA); err == nil {
		t.Error("a case number has a shape")
	}
	for _, tbl := range []string{"service_cases", "service_tasks", "service_case_sessions"} {
		err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, `DELETE FROM `+tbl, nil...) })
		if err == nil {
			t.Errorf("the application role must not delete from %s", tbl)
		}
	}
	// cross-company references fail on the composite keys, not by luck
	otherSess := uuid.New()
	if err := ex(`INSERT INTO service_tasks(tenant_id, title, origin_type, session_id) VALUES($1,'x','human',$2)`, s.env.TenantB, sess.ID); err == nil {
		t.Error("a task of company B cannot point at a session of company A")
	}
	_ = otherSess
	if err := ex(`INSERT INTO service_case_sessions(tenant_id, case_id, session_id, relation) VALUES($1,$2,$3,'reference')`, s.env.TenantB, cs.ID, sess.ID); err == nil {
		t.Error("a link under company B cannot join A's case and session")
	}
}

func TestCasesAndTasksNeverCrossCompanies(t *testing.T) {
	s := newStack(t)
	cs := s.mustCase(s.agent, "caso de A", nil)
	task := s.mustTask(s.agent, "tarefa de A", nil, nil)
	check := func(label string, fn func(ctx context.Context) error) {
		t.Helper()
		if err := s.try(s.env.UserB, s.env.TenantB, fn); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s from B: %v", label, err)
		}
	}
	check("get case", func(ctx context.Context) error { _, e := s.svc.GetCase(ctx, cs.ID); return e })
	check("get task", func(ctx context.Context) error { _, e := s.svc.GetTask(ctx, task.ID); return e })
	check("case timeline", func(ctx context.Context) error { _, _, e := s.svc.CaseTimeline(ctx, cs.ID, "", 5, false); return e })
	check("complete task", func(ctx context.Context) error {
		_, e := s.svc.ActTask(ctx, task.ID, application.TaskComplete, "", task.Version)
		return e
	})
	check("resolve case", func(ctx context.Context) error {
		_, e := s.svc.SetCaseStatus(ctx, cs.ID, domain.CaseStatusInput{To: domain.CaseResolved, Resolution: "x"}, cs.Version)
		return e
	})
	s.env.AsSystem(t, s.env.TenantB, func(ctx context.Context) {
		if _, err := s.svc.GetCase(ctx, cs.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a system session of B reads A's case: %v", err)
		}
		if _, err := s.svc.GetTask(ctx, task.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a system session of B reads A's task: %v", err)
		}
		if _, err := s.svc.ActTask(ctx, task.ID, application.TaskComplete, "", task.Version); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a system session of B completes A's task: %v", err)
		}
	})
	var cases []domain.Case
	var tasks []domain.Task
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) (e error) {
		cases, _, e = s.svc.ListCases(ctx, ports.CaseFilter{})
		if e != nil {
			return
		}
		tasks, _, e = s.svc.ListTasks(ctx, ports.TaskFilter{})
		return
	}); err != nil || len(cases) != 0 || len(tasks) != 0 {
		t.Fatalf("B lists nothing of A: %v %v %v", cases, tasks, err)
	}
	// A's case id used as a reference inside B
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error {
		_, e := s.svc.CreateTask(ctx, domain.TaskOpenInput{Title: "x", CaseID: &cs.ID})
		return e
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a task in B pointing at A's case: %v", err)
	}
}

func TestOverdueAndPendingFilters(t *testing.T) {
	s := newStack(t)
	past, future := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(48*time.Hour)
	late := s.mustTaskDue(t, "atrasada", &past)
	soon := s.mustTaskDue(t, "no prazo", &future)
	nodue := s.mustTask(s.agent, "sem prazo", nil, nil)
	done := s.mustTaskDue(t, "feita", &past)
	if _, err := s.act(s.agent, done.ID, application.TaskComplete, "", done.Version); err != nil {
		t.Fatal(err)
	}
	list := func(f ports.TaskFilter) (ids []uuid.UUID) {
		s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
			items, _, err := s.svc.ListTasks(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range items {
				ids = append(ids, it.ID)
			}
		})
		return
	}
	if got := list(ports.TaskFilter{OverdueOnly: true}); len(got) != 1 || got[0] != late.ID {
		t.Fatalf("overdue = only the pending one past its time: %v", got)
	}
	pending := map[uuid.UUID]bool{}
	for _, id := range list(ports.TaskFilter{PendingOnly: true}) {
		pending[id] = true
	}
	if !pending[late.ID] || !pending[soon.ID] || !pending[nodue.ID] || pending[done.ID] {
		t.Fatalf("pending: %v", pending)
	}
	if !late.Overdue(time.Now().UTC()) || soon.Overdue(time.Now().UTC()) || nodue.Overdue(time.Now().UTC()) {
		t.Fatal("Overdue()")
	}
}

func (s *stack) mustTaskDue(t *testing.T, title string, due *time.Time) *domain.Task {
	t.Helper()
	res, err := s.createTask(s.agent, domain.TaskOpenInput{Title: title, DueAt: due})
	if err != nil {
		t.Fatal(err)
	}
	return res.Task
}

// A customer's case or task never rides along under another customer's selection.
func TestOpenCasesAndPendingTasksFollowTheCustomerSelection(t *testing.T) {
	s := newStack(t)
	_, contact := s.conversation()
	c1, c2 := uuid.New(), uuid.New()
	for i, c := range []uuid.UUID{c1, c2} {
		s.exec(`INSERT INTO customer_accounts(id, tenant_id, name) VALUES($1,$2,$3)`, c, s.env.TenantA, fmt.Sprintf("Empresa %d", i+1))
		s.exec(`INSERT INTO contact_account_links(tenant_id, contact_id, account_id, source) VALUES($1,$2,$3,'manual')`, s.env.TenantA, contact, c)
	}
	sessOf := func(c *uuid.UUID) *domain.Session {
		res, err := s.open(s.agent, domain.OpenInput{ContactID: &contact, CustomerID: c, OriginChannel: "phone"})
		if err != nil {
			t.Fatal(err)
		}
		return res.Session
	}
	s1, s2, s0 := sessOf(&c1), sessOf(&c2), sessOf(nil)
	case1 := s.mustCase(s.agent, "caso da um", &s1.ID)
	case2 := s.mustCase(s.agent, "caso da dois", &s2.ID)
	case0 := s.mustCase(s.agent, "caso sem cliente", &s0.ID)
	task1 := s.mustTask(s.agent, "tarefa da um", &s1.ID, nil)
	task2 := s.mustTask(s.agent, "tarefa da dois", &s2.ID, nil)
	task0 := s.mustTask(s.agent, "tarefa sem cliente", &s0.ID, nil)
	idsC := func(cc *application.ContactContext) map[uuid.UUID]bool {
		m := map[uuid.UUID]bool{}
		for _, c := range cc.OpenCases {
			m[c.ID] = true
		}
		return m
	}
	idsT := func(cc *application.ContactContext) map[uuid.UUID]bool {
		m := map[uuid.UUID]bool{}
		for _, x := range cc.PendingTasks {
			m[x.ID] = true
		}
		return m
	}
	get := func(c *uuid.UUID) (cc *application.ContactContext) {
		if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) { cc, e = s.svc.ContactContext(ctx, contact, c); return }); err != nil {
			t.Fatal(err)
		}
		return
	}
	cc := get(nil)
	if !cc.NeedsSelection || len(idsC(cc)) != 1 || !idsC(cc)[case0.ID] || len(idsT(cc)) != 1 || !idsT(cc)[task0.ID] {
		t.Fatalf("before the choice only what has no customer: cases=%v tasks=%v", idsC(cc), idsT(cc))
	}
	cc = get(&c1)
	if !idsC(cc)[case1.ID] || !idsC(cc)[case0.ID] || idsC(cc)[case2.ID] || !idsT(cc)[task1.ID] || !idsT(cc)[task0.ID] || idsT(cc)[task2.ID] {
		t.Fatalf("customer 1: cases=%v tasks=%v", idsC(cc), idsT(cc))
	}
}

// ---- HTTP -------------------------------------------------------------------------------------------------------------------------------------

func TestHTTPCasesAndTasks(t *testing.T) {
	s := newStack(t)
	mux := s.mux()
	conv, _ := s.conversation()
	rs := s.call(mux, s.agent, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv))
	sessID := rs.body["id"].(string)

	if r := s.call(mux, uuid.Nil, "POST", "/service-cases", `{"title":"x"}`); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.code)
	}
	if r := s.call(mux, s.observer, "POST", "/service-cases", `{"title":"x"}`); r.code != http.StatusForbidden {
		t.Fatalf("no service.create: %d", r.code)
	}
	if r := s.call(mux, s.agent, "POST", "/service-cases", `{"title":"x","tenant_id":"`+uuid.NewString()+`"}`); r.code != http.StatusBadRequest {
		t.Fatalf("smuggled tenant: %d", r.code)
	}
	if r := s.call(mux, s.agent, "POST", "/service-cases", `{"title":""}`); r.code != http.StatusBadRequest {
		t.Fatalf("a title is required: %d", r.code)
	}
	r := s.call(mux, s.agent, "POST", "/service-cases", fmt.Sprintf(`{"title":"Link instável","description":"desde segunda","session_id":"%s","priority":"high"}`, sessID), "Idempotency-Key", "http-case-"+uuid.NewString())
	if r.code != http.StatusCreated || r.body["case_number"] != "OC-1000" || r.header.Get("ETag") != `"1"` || r.body["priority"] != "high" {
		t.Fatalf("open case: %d %v", r.code, r.body)
	}
	caseID := r.body["id"].(string)
	if r := s.call(mux, s.agent, "PATCH", "/service-cases/"+caseID, `{"title":"novo"}`); r.code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", r.code)
	}
	if r := s.call(mux, s.agent, "PATCH", "/service-cases/"+caseID, `{"title":"novo"}`, "If-Match", `"7"`); r.code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", r.code)
	}
	if r := s.call(mux, s.agent, "PATCH", "/service-cases/"+caseID, `{"title":"novo"}`, "If-Match", `"1"`); r.code != http.StatusOK || r.body["title"] != "novo" || r.header.Get("ETag") != `"2"` {
		t.Fatalf("patch: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", "/service-cases/"+caseID+"/status", `{"status":"resolved"}`, "If-Match", `"2"`); r.code != http.StatusBadRequest {
		t.Fatalf("resolution required: %d", r.code)
	}
	if r := s.call(mux, s.limited, "POST", "/service-cases/"+caseID+"/status", `{"status":"resolved","resolution":"ok"}`, "If-Match", `"2"`); r.code != http.StatusForbidden {
		t.Fatalf("no service.close: %d", r.code)
	}
	if r := s.call(mux, s.agent, "POST", "/service-cases/"+caseID+"/assign", fmt.Sprintf(`{"assigned_agent_id":"%s"}`, s.agent), "If-Match", `"2"`); r.code != http.StatusForbidden {
		t.Fatalf("no service.assign: %d", r.code)
	}
	if r := s.call(mux, s.admin, "POST", "/service-cases/"+caseID+"/assign", fmt.Sprintf(`{"assigned_agent_id":"%s"}`, s.agent), "If-Match", `"2"`); r.code != http.StatusOK || r.body["assigned_agent_id"] != s.agent.String() {
		t.Fatalf("assign: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "GET", "/service-cases?working=true&session_id="+sessID, ""); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("list by session: %d %v", r.code, r.body)
	}

	// tasks
	if r := s.call(mux, s.agent, "POST", "/service-tasks", `{"title":"x","origin_type":"flow"}`); r.code != http.StatusBadRequest {
		t.Fatalf("a caller cannot choose the origin: %d", r.code)
	}
	r = s.call(mux, s.agent, "POST", "/service-tasks", fmt.Sprintf(`{"title":"Ligar amanhã","case_id":"%s","session_id":"%s","due_at":"%s"}`, caseID, sessID, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339)))
	if r.code != http.StatusCreated || r.body["origin_type"] != "human" || r.body["overdue"] != false {
		t.Fatalf("create task: %d %v", r.code, r.body)
	}
	taskID := r.body["id"].(string)
	if r := s.call(mux, s.agent, "POST", "/service-tasks/"+taskID+"/cancel", `{}`, "If-Match", `"1"`); r.code != http.StatusBadRequest {
		t.Fatalf("a reason to cancel: %d", r.code)
	}
	if r := s.call(mux, s.agent, "POST", "/service-tasks/"+taskID+"/start", ``, "If-Match", `"1"`); r.code != http.StatusOK || r.body["status"] != "in_progress" {
		t.Fatalf("start: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", "/service-tasks/"+taskID+"/complete", ``, "If-Match", `"2"`); r.code != http.StatusOK || r.body["status"] != "done" {
		t.Fatalf("complete: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "GET", "/service-cases/"+caseID, ""); r.body["status"] != "open" {
		t.Fatalf("the case is untouched by its task: %v", r.body)
	}
	if r := s.call(mux, s.agent, "GET", "/service-tasks?case_id="+caseID+"&status=done", ""); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("list tasks: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "GET", "/service-cases/"+caseID+"/timeline?order=asc", ""); r.code != http.StatusOK || len(r.body["items"].([]any)) < 3 {
		t.Fatalf("case timeline: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", "/service-cases/"+caseID+"/sessions", `{}`); r.code != http.StatusBadRequest {
		t.Fatalf("session_id required: %d", r.code)
	}
	if r := s.call(mux, s.agent, "POST", "/service-cases/"+caseID+"/sessions", fmt.Sprintf(`{"session_id":"%s"}`, sessID)); r.code != http.StatusOK {
		t.Fatalf("the origin session is already linked: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.env.UserB, "GET", "/service-cases/"+caseID, ""); r.code != http.StatusNotFound && r.code != http.StatusForbidden {
		t.Fatalf("another company: %d", r.code)
	}
}
