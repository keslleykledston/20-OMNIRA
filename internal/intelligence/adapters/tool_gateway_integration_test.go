package adapters

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
)

func toolFlags() application.Flags {
	f := application.DefaultFlags()
	f.AIToolGatewayEnabled = true
	return f
}

func (e *env) toolHandler(flags application.Flags) *TopicHandler {
	topics := NewPostgresTopicRepository(e.app)
	h := e.handler()
	topicSvc := application.NewTopicService(topics)
	summaries := summarySvc(e, &fakeGen{out: "Assunto\nPedido atrasado"}, flags)
	tickets := e.ticketSvc(flags)
	return h.WithTools(application.NewToolGateway(NewPostgresToolCallRepository(e.app), topics, h.ToolAuthorizer(), application.NewToolExecutors(topicSvc, summaries, tickets), flags))
}

type toolBody struct {
	ID        uuid.UUID       `json:"id"`
	Tool      string          `json:"tool"`
	Status    string          `json:"status"`
	Source    string          `json:"source"`
	Result    json.RawMessage `json:"result"`
	Error     string          `json:"error"`
	Untrusted bool            `json:"result_is_untrusted_data"`
}

func invoke(e *env, h *TopicHandler, tenant, user uuid.UUID, topic uuid.UUID, tool, args, key, by string) (int, toolBody) {
	body := `{"tool":"` + tool + `","idempotency_key":"` + key + `"`
	if args != "" {
		body += `,"args":` + args
	}
	if by != "" {
		body += `,"proposed_by":"` + by + `"`
	}
	body += `}`
	rec := e.call(tenant, user, http.MethodPost, body, p("topic_id", topic.String()), h.InvokeAITool)
	var out toolBody
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestGatewayReadsRunForTheUserAndTheCatalogIsClosed(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	viewer := e.readOnlyMember(a.id)
	adminB := e.member(b.id, "tenant_admin")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	e.ticket(a.id, a.conversation, "open")
	topic, _ := e.topicWith(a, attendant, "Pedido", "meu pedido 837 não chegou")
	h := e.toolHandler(toolFlags())
	tp := p("topic_id", topic.ID.String())

	rec := e.call(a.id, stranger, http.MethodGet, "", tp, h.ListAITools)
	var cat struct{ Items []struct{ Name, Risk string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &cat)
	names := map[string]string{}
	for _, it := range cat.Items {
		names[it.Name] = it.Risk
	}
	if rec.Code != 200 || len(names) != 5 || names["topic.get_summary"] != "read" || names["topic.apply_ticket_policy"] != "low_write" {
		t.Fatalf("catalog = %d %v", rec.Code, names)
	}
	// reads run even for a non-attendant (they hold topic.read), and the result is flagged as untrusted data
	for _, tool := range []string{"topic.list_tickets", "topic.ticket_policy_advice", "topic.get_summary"} {
		code, out := invoke(e, h, a.id, stranger, topic.ID, tool, "", "read-"+tool+"-1", "")
		if code != 200 || out.Status != "executed" || !out.Untrusted || out.Source != "ai" {
			t.Errorf("%s = %d %+v", tool, code, out)
		}
	}
	// nothing outside the closed registry exists, however it is spelled
	for _, tool := range []string{"message.send", "ticket.close", "sql.query", "script.run", "http.request", "topic.delete", "TOPIC.GET_SUMMARY", "topic.get_summary "} {
		if code, _ := invoke(e, h, a.id, attendant, topic.ID, tool, "", "bad-"+strings.ReplaceAll(tool, " ", "_")+"-1", "agent"); code != http.StatusUnprocessableEntity {
			t.Errorf("%q = %d, want 422", tool, code)
		}
	}
	if e.count(`SELECT count(*) FROM ai_tool_calls WHERE tenant_id=$1 AND tool IN ('message.send','ticket.close','sql.query')`, a.id) != 0 {
		t.Fatal("an unknown tool must leave no row")
	}
	// a viewer has no topic.read; another tenant sees nothing
	if code, _ := invoke(e, h, a.id, viewer, topic.ID, "topic.get_summary", "", "viewer-read-1", ""); code != http.StatusForbidden {
		t.Errorf("viewer = %d", code)
	}
	if code, _ := invoke(e, h, b.id, adminB, topic.ID, "topic.get_summary", "", "other-tenant-1", ""); code != http.StatusNotFound {
		t.Errorf("cross-tenant = %d, want 404", code)
	}
	// arguments are strict: a smuggled tenant, url or sql is refused before anything is stored
	for _, args := range []string{`{"tenant_id":"` + b.id.String() + `"}`, `{"url":"http://evil"}`, `{"sql":"select 1"}`, `[]`} {
		before := e.count(`SELECT count(*) FROM ai_tool_calls WHERE tenant_id=$1`, a.id)
		if code, _ := invoke(e, h, a.id, attendant, topic.ID, "topic.get_summary", args, "args-"+uuid.NewString()[:8], ""); code != http.StatusUnprocessableEntity {
			t.Errorf("args %s = %d", args, code)
		}
		if e.count(`SELECT count(*) FROM ai_tool_calls WHERE tenant_id=$1`, a.id) != before {
			t.Errorf("args %s left a row", args)
		}
	}
	// flag off: the whole surface is gone
	off := e.toolHandler(application.DefaultFlags())
	if code, _ := invoke(e, off, a.id, attendant, topic.ID, "topic.get_summary", "", "flag-off-1", ""); code != http.StatusNotFound {
		t.Errorf("flag off = %d", code)
	}
	if rec := e.call(a.id, attendant, http.MethodGet, "", tp, off.ListAITools); rec.Code != http.StatusNotFound {
		t.Errorf("flag off catalog = %d", rec.Code)
	}
}

func TestAWriteAskedByTheAIWaitsForAPersonWhoMustBeAllowedToDoItThemselves(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	placeholder := e.ticket(a.id, a.conversation, "open")
	topic, _ := e.topicWith(a, attendant, "Pedido", "meu pedido 837 não chegou")
	h := e.toolHandler(toolFlags())
	tp := p("topic_id", topic.ID.String())

	code, call := invoke(e, h, a.id, attendant, topic.ID, "topic.apply_ticket_policy", `{"action":"adopt_active"}`, "adopt-pending-1", "ai")
	if code != http.StatusAccepted || call.Status != "pending_approval" {
		t.Fatalf("AI write = %d %+v, must wait", code, call)
	}
	if e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("the AI changed data without approval")
	}
	cp := p("topic_id", topic.ID.String())
	cp["call_id"] = call.ID.String()
	// somebody who could not do it themselves cannot approve it
	if rec := e.call(a.id, stranger, http.MethodPost, ``, cp, h.ApproveAIToolCall); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant approve = %d", rec.Code)
	}
	if rec := e.call(a.id, stranger, http.MethodPost, ``, cp, h.RejectAIToolCall); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant reject = %d", rec.Code)
	}
	var st string
	_ = e.seed.QueryRow(e.ctx, `SELECT status FROM ai_tool_calls WHERE id=$1`, call.ID).Scan(&st)
	if st != "pending_approval" {
		t.Fatalf("a refused approval must leave the call pending, got %s", st)
	}
	// the attendant approves: it runs with THEIR authority, recorded as coming from the AI
	rec := e.call(a.id, attendant, http.MethodPost, ``, cp, h.ApproveAIToolCall)
	var done toolBody
	_ = json.Unmarshal(rec.Body.Bytes(), &done)
	if rec.Code != 200 || done.Status != "executed" || !strings.Contains(string(done.Result), placeholder.String()) {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body.String())
	}
	var origin string
	_ = e.seed.QueryRow(e.ctx, `SELECT created_by FROM topic_ticket_links WHERE tenant_id=$1 AND ticket_id=$2`, a.id, placeholder).Scan(&origin)
	if origin != "ai" {
		t.Fatalf("the link must record that the idea came from the AI, got %q", origin)
	}
	var decidedBy *uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT decided_by FROM ai_tool_calls WHERE id=$1`, call.ID).Scan(&decidedBy)
	if decidedBy == nil || *decidedBy != attendant {
		t.Fatalf("decided_by = %v", decidedBy)
	}
	// it cannot be approved or rejected twice
	if rec := e.call(a.id, attendant, http.MethodPost, ``, cp, h.ApproveAIToolCall); rec.Code != http.StatusConflict {
		t.Errorf("approve again = %d, want 409", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, ``, cp, h.RejectAIToolCall); rec.Code != http.StatusConflict {
		t.Errorf("reject after execute = %d, want 409", rec.Code)
	}

	// rejection: nothing runs
	_, call2 := invoke(e, h, a.id, attendant, topic.ID, "topic.generate_summary", "", "gen-pending-1", "ai")
	cp2 := p("topic_id", topic.ID.String())
	cp2["call_id"] = call2.ID.String()
	if rec := e.call(a.id, attendant, http.MethodPost, ``, cp2, h.RejectAIToolCall); rec.Code != http.StatusNoContent {
		t.Fatalf("reject = %d", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, ``, cp2, h.ApproveAIToolCall); rec.Code != http.StatusConflict {
		t.Errorf("approving a rejected call = %d", rec.Code)
	}
	if e.count(`SELECT count(*) FROM topic_summaries WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("a rejected call must not have run")
	}
	// a call of another topic cannot be approved through this one
	other, _ := e.topicWith(a, attendant, "Outro", "x")
	_, call3 := invoke(e, h, a.id, attendant, other.ID, "topic.generate_summary", "", "gen-other-1", "ai")
	wrong := p("topic_id", topic.ID.String())
	wrong["call_id"] = call3.ID.String()
	if rec := e.call(a.id, attendant, http.MethodPost, ``, wrong, h.ApproveAIToolCall); rec.Code != http.StatusNotFound {
		t.Errorf("approve through the wrong topic = %d, want 404", rec.Code)
	}
	// the audit trail lists everything, and it never offers a delete
	rec = e.call(a.id, stranger, http.MethodGet, "", tp, h.ListAIToolCalls)
	var list struct{ Items []toolBody }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Items) != 2 {
		t.Fatalf("calls = %d %s", rec.Code, rec.Body.String())
	}
}

func TestOnlyOneApproverEverExecutesAndAPersonActingIsJustThePerson(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	topic, _ := e.topicWith(a, attendant, "Pedido", "um", "dois", "três")
	h := e.toolHandler(toolFlags())

	_, call := invoke(e, h, a.id, attendant, topic.ID, "topic.generate_summary", "", "race-approve-1", "ai")
	cp := p("topic_id", topic.ID.String())
	cp["call_id"] = call.ID.String()
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := e.call(a.id, attendant, http.MethodPost, ``, cp, h.ApproveAIToolCall); rec.Code == 200 {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 || e.count(`SELECT count(*) FROM topic_summaries WHERE tenant_id=$1`, a.id) != 1 {
		t.Fatalf("%d approvals executed, summaries=%d; want exactly 1 of each", ok, e.count(`SELECT count(*) FROM topic_summaries WHERE tenant_id=$1`, a.id))
	}

	// the same write requested by a person who may do it runs at once (it IS the person acting)...
	code, out := invoke(e, h, a.id, attendant, topic.ID, "topic.apply_ticket_policy", `{"action":"create"}`, "agent-direct-1", "agent")
	if code != 200 || out.Source != "agent" || (out.Status != "executed" && out.Status != "failed") {
		t.Fatalf("agent direct = %d %+v", code, out)
	}
	// ...and a person who may NOT is denied and the denial is recorded
	code, den := invoke(e, h, a.id, stranger, topic.ID, "topic.generate_summary", "", "denied-write-1", "agent")
	if code != http.StatusForbidden || den.Status != "denied" {
		t.Fatalf("denied = %d %+v", code, den)
	}
	if e.count(`SELECT count(*) FROM ai_tool_calls WHERE tenant_id=$1 AND status='denied' AND requested_by=$2`, a.id, stranger) != 1 {
		t.Fatal("a denial must be audited")
	}
	// claiming to be the agent changes nothing about permissions: the stranger still cannot write as "ai" either
	code, pend := invoke(e, h, a.id, stranger, topic.ID, "topic.generate_summary", "", "denied-ai-write-1", "ai")
	if code != http.StatusForbidden || pend.Status != "denied" {
		t.Fatalf("stranger as ai = %d %+v", code, pend)
	}
}

func TestIdempotencyKeyMakesARetryReturnTheFirstOutcomeAndActOnlyOnce(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	topic, _ := e.topicWith(a, attendant, "Pedido", "um", "dois", "três")
	h := e.toolHandler(toolFlags())
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[uuid.UUID]bool{}
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, out := invoke(e, h, a.id, attendant, topic.ID, "topic.generate_summary", "", "same-key-123", "agent")
			if code == 200 {
				mu.Lock()
				ids[out.ID] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(ids) != 1 || e.count(`SELECT count(*) FROM ai_tool_calls WHERE tenant_id=$1 AND idempotency_key='same-key-123'`, a.id) != 1 || e.count(`SELECT count(*) FROM topic_summaries WHERE tenant_id=$1`, a.id) != 1 {
		t.Fatalf("concurrent retries: ids=%v rows/summaries wrong", ids)
	}
	// a replay returns the stored outcome
	code, again := invoke(e, h, a.id, attendant, topic.ID, "topic.generate_summary", "", "same-key-123", "agent")
	if code != 200 || again.Status != "executed" {
		t.Fatalf("replay = %d %+v", code, again)
	}
	// the same key for a different request is a conflict, never a silent reuse
	if code, _ := invoke(e, h, a.id, attendant, topic.ID, "topic.get_summary", "", "same-key-123", "agent"); code != http.StatusConflict {
		t.Errorf("same key, different tool = %d, want 409", code)
	}
	if code, _ := invoke(e, h, a.id, attendant, topic.ID, "topic.get_summary", "", "short", "agent"); code != http.StatusUnprocessableEntity {
		t.Errorf("short key = %d", code)
	}
	// a tool that fails reports a fixed reason, never raw error text
	code, failed := invoke(e, h, a.id, attendant, topic.ID, "topic.apply_ticket_policy", `{"action":"share_active"}`, "fails-safely-1", "agent")
	if code != 200 || failed.Status != "failed" || failed.Error == "" || len(failed.Error) > 40 {
		t.Fatalf("failure = %d %+v", code, failed)
	}
}
