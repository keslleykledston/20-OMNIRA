package adapters

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/ports"
)

func TestEvaluationReportsAgreementOverridesAndNeverLeaksContentOrTenants(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	admin := e.member(a.id, "tenant_admin")
	viewer := e.readOnlyMember(a.id)
	adminB := e.member(b.id, "tenant_admin")
	h := e.handler().WithEvaluation(NewPostgresEvaluationRepository(e.app))

	t1, ids1 := e.topicWith(a, admin, "Pedido SEGREDO-TITULO", "texto secreto um")
	t2, _ := e.topicWith(a, admin, "Outro", "texto secreto dois")
	// router: 3 applied decisions, one overridden by a person; plus 1 dry-run proposal
	mA, mB, mC, mD := e.message(a.id, a.conversation, "a"), e.message(a.id, a.conversation, "b"), e.message(a.id, a.conversation, "c"), e.message(a.id, a.conversation, "d")
	dec := func(msg uuid.UUID, status string, topic *uuid.UUID, source string, applied bool, conf float64, over bool) {
		e.exec(`INSERT INTO routing_decisions(tenant_id,message_id,status,selected_topic_thread_id,decision_source,applied,confidence,overridden_at) VALUES($1,$2,$3,$4,$5,$6,$7, CASE WHEN $8 THEN now() END)`,
			a.id, msg, status, topic, source, applied, conf, over)
	}
	dec(mA, "assigned", &t1.ID, "rule", true, 0.9, false)
	dec(mB, "assigned", &t1.ID, "entity", true, 0.95, true)
	dec(mC, "ambiguous", nil, "rule", true, 0.6, false)
	dec(mD, "assigned", &t1.ID, "rule", false, 0.9, false)
	// AI shadow: 4 proposals that point at a topic. Two messages ended in the proposed topic (one high, one low confidence),
	// one ended elsewhere, one was never placed.
	p1, p2, p3, p4 := e.message(a.id, a.conversation, "p1"), e.message(a.id, a.conversation, "p2"), e.message(a.id, a.conversation, "p3"), e.message(a.id, a.conversation, "p4")
	for _, m := range []uuid.UUID{p1, p2} {
		e.exec(`INSERT INTO message_topic_links(tenant_id,message_id,topic_thread_id,relation,decision_source) VALUES($1,$2,$3,'primary','agent')`, a.id, m, t1.ID)
	}
	e.exec(`INSERT INTO message_topic_links(tenant_id,message_id,topic_thread_id,relation,decision_source) VALUES($1,$2,$3,'primary','agent')`, a.id, p3, t2.ID)
	aiDec := func(msg uuid.UUID, conf float64) {
		e.exec(`INSERT INTO routing_decisions(tenant_id,message_id,status,selected_topic_thread_id,decision_source,applied,confidence,input_tokens,output_tokens) VALUES($1,$2,'assigned',$3,'ai',false,$4,100,10)`, a.id, msg, t1.ID, conf)
	}
	aiDec(p1, 0.9)
	aiDec(p2, 0.3)
	aiDec(p3, 0.85)
	aiDec(p4, 0.6)
	_ = ids1
	// other things people did
	e.exec(`INSERT INTO topic_summaries(tenant_id,topic_thread_id,version,summary_text,status,model_provider) VALUES($1,$2,1,'x','superseded','fake'),($1,$2,2,'y','agent_confirmed','fake'),($1,$2,3,'z','corrected',NULL)`, a.id, t1.ID)
	e.exec(`INSERT INTO ai_tool_calls(tenant_id,topic_thread_id,tool,risk,source,status,idempotency_key,executed_at) VALUES($1,$2,'topic.get_summary','read','ai','executed','eval-key-0001',now()),($1,$2,'topic.generate_summary','low_write','ai','pending_approval','eval-key-0002',NULL)`, a.id, t1.ID)
	// tenant B's data must never show up in A's numbers
	bm := e.message(b.id, b.conversation, "bm")
	e.exec(`INSERT INTO routing_decisions(tenant_id,message_id,status,decision_source,applied,confidence) VALUES($1,$2,'unassigned','ai',false,0.9)`, b.id, bm)

	rec := e.call(a.id, admin, http.MethodGet, "", nil, h.GetEvaluation)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("evaluation = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, secret := range []string{"SEGREDO", "texto secreto"} {
		if strings.Contains(body, secret) {
			t.Fatalf("the report leaked content: %s", secret)
		}
	}
	var ev ports.Evaluation
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Router.Decisions != 4 || ev.Router.Applied != 3 || ev.Router.Overridden != 1 || ev.Router.OverrideRate < 0.333 || ev.Router.OverrideRate > 0.334 {
		t.Errorf("router = %+v", ev.Router)
	}
	if ev.AIShadow.Proposals != 4 || ev.AIShadow.WithFinalPlacement != 3 || ev.AIShadow.Agreed != 2 || ev.AIShadow.TokensIn != 400 {
		t.Errorf("ai shadow = %+v", ev.AIShadow)
	}
	if r := ev.AIShadow.AgreementRate; r < 0.666 || r > 0.667 {
		t.Errorf("agreement rate = %v, want 2/3", r)
	}
	byRange := map[string]ports.ConfBucket{}
	for _, b := range ev.AIShadow.ByConfidence {
		byRange[b.Range] = b
	}
	if b := byRange["0.80-1.00"]; b.WithFinalPlacement != 2 || b.Agreed != 1 {
		t.Errorf("high bucket = %+v", b)
	}
	if b := byRange["0.00-0.49"]; b.WithFinalPlacement != 1 || b.Agreed != 1 {
		t.Errorf("low bucket = %+v", b)
	}
	if ev.Summaries.Versions != 3 || ev.Summaries.Confirmed != 1 || ev.Summaries.Corrected != 1 || ev.Summaries.CorrectionRate != 0.5 || ev.Summaries.AIInferred != 2 {
		t.Errorf("summaries = %+v", ev.Summaries)
	}
	if ev.ToolCalls["executed"] != 1 || ev.ToolCalls["pending_approval"] != 1 {
		t.Errorf("tool calls = %v", ev.ToolCalls)
	}
	// authorization, tenancy, validation
	if rec := e.call(a.id, viewer, http.MethodGet, "", nil, h.GetEvaluation); rec.Code != http.StatusForbidden {
		t.Errorf("viewer = %d", rec.Code)
	}
	recB := e.call(b.id, adminB, http.MethodGet, "", nil, h.GetEvaluation)
	var evB ports.Evaluation
	_ = json.Unmarshal(recB.Body.Bytes(), &evB)
	if recB.Code != 200 || evB.AIShadow.Proposals != 1 || evB.Router.Decisions != 0 || len(evB.ToolCalls) != 0 {
		t.Errorf("tenant B sees %+v", evB)
	}
	for _, q := range []string{"0", "400", "abc", "-3"} {
		req := e.callQuery(a.id, admin, "days="+q, h.GetEvaluation)
		if req.Code != http.StatusBadRequest {
			t.Errorf("days=%s = %d", q, req.Code)
		}
	}
	if rec := e.call(a.id, admin, http.MethodGet, "", nil, e.handler().GetEvaluation); rec.Code != http.StatusNotFound {
		t.Errorf("unwired = %d", rec.Code)
	}
	// an empty tenant gets zeros, not errors
	c := e.tenant()
	adminC := e.member(c.id, "tenant_admin")
	if rec := e.call(c.id, adminC, http.MethodGet, "", nil, h.GetEvaluation); rec.Code != 200 {
		t.Errorf("empty tenant = %d", rec.Code)
	}
}
