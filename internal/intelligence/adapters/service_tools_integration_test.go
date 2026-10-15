package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	servicectxadapters "github.com/omnira/omnira/internal/servicectx/adapters"
	servicectxdomain "github.com/omnira/omnira/internal/servicectx/domain"
)

// ADR-0049 phase 6: the AI reads the service session and only PROPOSES; a person approves with their own permissions. Nothing that closes a session, touches a case or
// asks an ERP is a tool.

func (e *env) serviceToolHandler(flags application.Flags) *TopicHandler {
	topics := NewPostgresTopicRepository(e.app)
	h := e.handler()
	topicSvc := application.NewTopicService(topics)
	summaries := summarySvc(e, &fakeGen{out: "Assunto\nPedido atrasado"}, flags)
	execs := application.NewToolExecutors(topicSvc, summaries, e.ticketSvc(flags))
	svc, _, _ := servicectxadapters.Build(e.app, servicectxadapters.Options{})
	for name, run := range application.NewServiceExecutors(topics, servicectxadapters.NewAIBridge(svc)) {
		execs[name] = run
	}
	return h.WithTools(application.NewToolGateway(NewPostgresToolCallRepository(e.app), topics, h.ToolAuthorizer(), execs, flags))
}

func (e *env) openServiceSession(a tenantFixture, user uuid.UUID) (id uuid.UUID, protocol string) {
	svc, _, _ := servicectxadapters.Build(e.app, servicectxadapters.Options{})
	e.session(a.id, user, func(ctx context.Context) {
		res, err := svc.Open(ctx, servicectxdomain.OpenInput{ConversationID: &a.conversation})
		if err != nil {
			e.t.Fatal(err)
		}
		id, protocol = res.Session.ID, res.Session.Protocol
	})
	return
}

func TestTheAIReadsTheServiceSessionAndOnlyProposesWhatAPersonApproves(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	e.exec(`INSERT INTO service_categories(tenant_id, code, label) VALUES($1,'suporte','Suporte')`, a.id)
	topic, _ := e.topicWith(a, attendant, "Pedido", "meu pedido 837 não chegou")
	h := e.serviceToolHandler(toolFlags())
	tp := p("topic_id", topic.ID.String())

	// the catalog offers the four service tools, and nothing that closes, links or asks an ERP
	rec := e.call(a.id, attendant, http.MethodGet, "", tp, h.ListAITools)
	var cat struct{ Items []struct{ Name, Risk string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &cat)
	names := map[string]string{}
	for _, it := range cat.Items {
		names[it.Name] = it.Risk
	}
	if names["service.current_session"] != "read" || names["service.open_tasks"] != "read" || names["service.propose_task"] != "low_write" || names["service.propose_classification"] != "low_write" {
		t.Fatalf("catalog: %v", names)
	}
	for n := range names {
		if strings.HasPrefix(n, "service.") && n != "service.current_session" && n != "service.open_tasks" && n != "service.propose_task" && n != "service.propose_classification" {
			t.Fatalf("an unexpected service tool is offered to the AI: %s", n)
		}
	}
	// reads: no session yet -> null; then the protocol
	code, out := invoke(e, h, a.id, attendant, topic.ID, "service.current_session", "", "svc-read-0001", "")
	if code != 200 || out.Status != "executed" || !strings.Contains(string(out.Result), `"session":null`) {
		t.Fatalf("no session yet: %d %+v %s", code, out, out.Result)
	}
	sessionID, protocol := e.openServiceSession(a, attendant)
	code, out = invoke(e, h, a.id, attendant, topic.ID, "service.current_session", "", "svc-read-0002", "")
	if code != 200 || !strings.Contains(string(out.Result), protocol) || !out.Untrusted {
		t.Fatalf("the session is read, as untrusted data: %d %s", code, out.Result)
	}

	// the AI proposes a task: it waits, nothing is created
	code, call := invoke(e, h, a.id, attendant, topic.ID, "service.propose_task", `{"title":"Ligar com a posição do pedido","priority":"high"}`, "svc-task-0001", "ai")
	if code != http.StatusAccepted || call.Status != "pending_approval" {
		t.Fatalf("AI proposal: %d %+v", code, call)
	}
	if n := e.count(`SELECT count(*) FROM service_tasks WHERE tenant_id=$1`, a.id); n != 0 {
		t.Fatalf("the AI created a task without a person: %d", n)
	}
	cp := p("topic_id", topic.ID.String())
	cp["call_id"] = call.ID.String()
	rec = e.call(a.id, attendant, http.MethodPost, ``, cp, h.ApproveAIToolCall)
	if rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	var origin, title string
	var by, session *uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT origin_type, title, created_by_user_id, session_id FROM service_tasks WHERE tenant_id=$1`, a.id).Scan(&origin, &title, &by, &session); err != nil {
		t.Fatal(err)
	}
	if origin != "ai" || title != "Ligar com a posição do pedido" || by == nil || *by != attendant || session == nil || *session != sessionID {
		t.Fatalf("the task says the idea was the AI's, the approver made it, and it hangs from the session: %s %q %v %v", origin, title, by, session)
	}
	if n := e.count(`SELECT count(*) FROM context_events WHERE tenant_id=$1 AND session_id=$2 AND actor_type='user' AND actor_id=$3`, a.id, sessionID, attendant); n < 1 {
		t.Fatalf("the history is signed by the person who approved: %d", n)
	}
	// open tasks reads it back
	code, out = invoke(e, h, a.id, attendant, topic.ID, "service.open_tasks", "", "svc-read-0003", "")
	if code != 200 || !strings.Contains(string(out.Result), "Ligar com a posição do pedido") {
		t.Fatalf("open tasks: %d %s %s %s", code, out.Status, out.Error, out.Result)
	}

	// a classification: pending, then approved; an unknown category fails and changes nothing
	code, call = invoke(e, h, a.id, attendant, topic.ID, "service.propose_classification", `{"category":"suporte"}`, "svc-class-0001", "ai")
	if code != http.StatusAccepted {
		t.Fatalf("classification proposal: %d %+v", code, call)
	}
	if e.count(`SELECT count(*) FROM service_sessions WHERE id=$1 AND category_id IS NOT NULL`, sessionID) != 0 {
		t.Fatal("not classified until approved")
	}
	cp["call_id"] = call.ID.String()
	if rec = e.call(a.id, attendant, http.MethodPost, ``, cp, h.ApproveAIToolCall); rec.Code != 200 {
		t.Fatalf("approve classification: %d %s", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT count(*) FROM service_sessions WHERE id=$1 AND category_id IS NOT NULL`, sessionID) != 1 {
		t.Fatal("classified after approval")
	}
	code, call = invoke(e, h, a.id, attendant, topic.ID, "service.propose_classification", `{"category":"nao-existe"}`, "svc-class-0002", "ai")
	cp["call_id"] = call.ID.String()
	rec = e.call(a.id, attendant, http.MethodPost, ``, cp, h.ApproveAIToolCall)
	var failed toolBody
	_ = json.Unmarshal(rec.Body.Bytes(), &failed)
	if failed.Status != "failed" {
		t.Fatalf("an unknown category fails when approved: %d %s", rec.Code, rec.Body.String())
	}

	// a rejected proposal leaves nothing
	code, call = invoke(e, h, a.id, attendant, topic.ID, "service.propose_task", `{"title":"Não fazer"}`, "svc-task-0002", "ai")
	cp["call_id"] = call.ID.String()
	if rec = e.call(a.id, attendant, http.MethodPost, ``, cp, h.RejectAIToolCall); rec.Code >= 300 {
		t.Fatalf("reject: %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM service_tasks WHERE tenant_id=$1`, a.id); n != 1 {
		t.Fatalf("only the approved task exists: %d", n)
	}
}

func TestTheAICannotReachWhatIsNotATool(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	topic, _ := e.topicWith(a, attendant, "Pedido", "meu pedido 837 não chegou")
	h := e.serviceToolHandler(toolFlags())
	e.openServiceSession(a, attendant)
	for _, tool := range []string{"service.request_external", "service.request_service_order", "service.close_session", "service.close", "service.open_case", "service.link_external", "service.reopen", "service.assign_task"} {
		if code, _ := invoke(e, h, a.id, attendant, topic.ID, tool, `{}`, "no-"+strings.ReplaceAll(tool, ".", "-"), "ai"); code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422 (it is not a tool)", tool, code)
		}
	}
	if n := e.count(`SELECT count(*) FROM ai_tool_calls WHERE tenant_id=$1 AND tool LIKE 'service.%'`, a.id); n != 0 {
		t.Fatalf("a tool that does not exist leaves no row: %d", n)
	}
	// strict arguments: a smuggled field, a credential, a multi-line title, an unknown priority
	for _, args := range []string{`{"title":"x","tenant_id":"` + uuid.NewString() + `"}`, `{"title":"x","assigned_user_id":"` + uuid.NewString() + `"}`, `{"title":"linha 1\nlinha 2"}`,
		`{"title":""}`, `{"title":"x","priority":"critical"}`, `{"title":"use Bearer abcdefghijklmnop123456 no cabeçalho"}`} {
		if code, _ := invoke(e, h, a.id, attendant, topic.ID, "service.propose_task", args, "bad-"+uuid.NewString()[:10], "ai"); code != http.StatusUnprocessableEntity {
			t.Errorf("args %s = %d, want 422", args, code)
		}
	}
	for _, args := range []string{`{"category":"Suporte Técnico"}`, `{"category":"x","extra":1}`, `{}`} {
		if code, _ := invoke(e, h, a.id, attendant, topic.ID, "service.propose_classification", args, "bad-"+uuid.NewString()[:10], "ai"); code != http.StatusUnprocessableEntity {
			t.Errorf("args %s = %d, want 422", args, code)
		}
	}
	// without the module's keys the gateway itself denies: a member who may read topics but holds no service key
	viewer := e.readOnlyMember(a.id)
	if code, _ := invoke(e, h, a.id, viewer, topic.ID, "service.propose_task", `{"title":"x"}`, "denied-0001-x", "ai"); code != http.StatusForbidden {
		t.Errorf("a member without service.create: %d", code)
	}
	// the module off: no tool is offered at all
	bare := e.toolHandler(toolFlags())
	rec := e.call(a.id, attendant, http.MethodGet, "", p("topic_id", topic.ID.String()), bare.ListAITools)
	if strings.Contains(rec.Body.String(), "service.") {
		t.Fatalf("with the module off no service tool is in the catalog: %s", rec.Body.String())
	}
}
