package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/flowstest"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func simCtx() context.Context {
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	return tenancydomain.WithTenantContext(context.Background(), tc)
}

func simulate(t *testing.T, def string, sc Scenario) *SimResult {
	t.Helper()
	res, err := NewSimulator(nil).Simulate(simCtx(), SimInput{Raw: json.RawMessage(def), Scenario: sc})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func msgs(texts ...string) []SimEvent {
	out := make([]SimEvent, len(texts))
	for i, x := range texts {
		out[i] = SimEvent{Type: "message", Text: x}
	}
	return out
}

func TestSimulatorAskFlowEndToEnd(t *testing.T) {
	res := simulate(t, askFlow, Scenario{Contact: SimContact{Name: "Ana"}, Events: msgs("oi", "Carlos")})
	if res.Status != "completed" || res.Consumed != 2 || res.Variables["nome"] != "Carlos" {
		t.Fatalf("result: %+v", res)
	}
	if len(res.Messages) != 2 || res.Messages[0].Text != "Qual o seu nome?" || res.Messages[1].Text != "Prazer, Carlos!" {
		t.Fatalf("messages the bot would send: %+v", res.Messages)
	}
	if !res.Reached("hi") || res.Reached("slow") || res.Steps[0].NodeType != "trigger" {
		t.Fatalf("path: %+v", res.Steps)
	}
	// stops waiting when the scenario runs out of events
	res = simulate(t, askFlow, Scenario{})
	if res.Status != "waiting_input" || res.Waiting != "q" || len(res.Messages) != 1 {
		t.Fatalf("a scenario with only the first message ends waiting at the question: %+v", res)
	}
}

func TestSimulatorTimeoutEventAndEventsAfterTheEnd(t *testing.T) {
	res := simulate(t, askFlow, Scenario{Events: []SimEvent{{Type: "message", Text: "oi"}, {Type: "timeout"}, {Type: "message", Text: "tarde demais"}}})
	if res.Status != "completed" || !res.Reached("slow") || res.Reached("hi") {
		t.Fatalf("a timeout event must take the timeout port: %+v", res)
	}
	if res.Consumed != 2 {
		t.Fatalf("events after the end are not consumed: %d", res.Consumed)
	}
}

func TestSimulatorScenarioKnobs(t *testing.T) {
	// Meta window closed: the send node follows its window_closed port.
	flow := wf(`{"id":"start","type":"trigger"},{"id":"hi","type":"send_message","config":{"text":"x"}},{"id":"ok","type":"end"},{"id":"closed","type":"end"}`,
		edge("1", "start", "next", "hi")+","+edge("2", "hi", "next", "ok")+","+edge("3", "hi", "window_closed", "closed"), "")
	closed := false
	res := simulate(t, flow, Scenario{Provider: "meta_cloud", WindowOpen: &closed})
	if !res.Reached("closed") || res.Reached("ok") || len(res.Messages) != 0 {
		t.Fatalf("closed window: %+v", res)
	}
	if res := simulate(t, flow, Scenario{Provider: "meta_cloud"}); !res.Reached("ok") {
		t.Fatalf("open window: %+v", res)
	}
	// Companies: none / one / several (the bot must ask, never pick)
	ctxFlow := wf(`{"id":"start","type":"trigger"},{"id":"k","type":"resolve_customer_context"},{"id":"pick","type":"customer_choice"},{"id":"none","type":"end"},{"id":"single","type":"end"},{"id":"chosen","type":"end"},{"id":"gaveup","type":"end"}`,
		strings.Join([]string{edge("1", "start", "next", "k"), edge("2", "k", "none", "none"), edge("3", "k", "single", "single"), edge("4", "k", "multiple", "pick"), edge("5", "pick", "selected", "chosen"), edge("6", "pick", "timeout", "gaveup")}, ","), "")
	if res := simulate(t, ctxFlow, Scenario{}); !res.Reached("none") {
		t.Fatalf("no company: %+v", res)
	}
	if res := simulate(t, ctxFlow, Scenario{Companies: []SimCompany{{Name: "ACME"}}}); !res.Reached("single") {
		t.Fatalf("one company: %+v", res)
	}
	res = simulate(t, ctxFlow, Scenario{Companies: []SimCompany{{Name: "ACME"}, {Name: "Beta"}}})
	if res.Status != "waiting_input" || len(res.Messages) != 1 || !strings.Contains(res.Messages[0].Text, "1) ACME") {
		t.Fatalf("several companies must be asked, not chosen: %+v", res)
	}
	res = simulate(t, ctxFlow, Scenario{Companies: []SimCompany{{Name: "ACME"}, {Name: "Beta"}}, Events: msgs("oi", "2")})
	if !res.Reached("chosen") || res.Status != "completed" {
		t.Fatalf("choosing: %+v", res)
	}
	// Business hours follow the scenario clock in the schedule's timezone.
	hours := wf(`{"id":"start","type":"trigger"},{"id":"h","type":"business_hours","config":{"timezone":"America/Sao_Paulo","windows":[{"days":["mon","tue","wed","thu","fri"],"start":"09:00","end":"18:00"}]}},{"id":"open","type":"end"},{"id":"closed","type":"end"}`,
		edge("1", "start", "next", "h")+","+edge("2", "h", "open", "open")+","+edge("3", "h", "closed", "closed"), "")
	monday10 := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC) // 10:00 in São Paulo
	sunday := time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC)
	if !simulate(t, hours, Scenario{Now: &monday10}).Reached("open") || !simulate(t, hours, Scenario{Now: &sunday}).Reached("closed") {
		t.Fatal("business hours must follow the scenario clock")
	}
}

func TestSimulatorRecordsSideEffectsWithoutPerformingThem(t *testing.T) {
	queue := uuid.New()
	flow := wf(fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"find","type":"find_open_tickets"},{"id":"mk","type":"create_ticket","config":{"subject":"VPN {{contact.name}}","priority":"high"}},
	  {"id":"q","type":"assign_queue","config":{"queue":%q}},{"id":"h","type":"human_handoff","config":{"summary":"resumo"}},{"id":"related","type":"end"}`, queue),
		strings.Join([]string{edge("1", "start", "next", "find"), edge("2", "find", "none", "mk"), edge("3", "find", "found", "related"), edge("4", "mk", "next", "q"), edge("5", "q", "next", "h")}, ","), "")
	res := simulate(t, flow, Scenario{Contact: SimContact{Name: "Ana"}})
	if res.Status != "waiting_human" {
		t.Fatalf("status: %+v", res)
	}
	kinds := []string{}
	for _, e := range res.Effects {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "ticket,assign_queue,handoff" || res.Effects[0].Detail["subject"] != "VPN Ana" || res.Effects[0].Step != 3 {
		t.Fatalf("effects must list what would happen and which node does it: %+v", res.Effects)
	}
	if res := simulate(t, flow, Scenario{OpenTickets: 2}); !res.Reached("related") {
		t.Fatalf("open tickets scenario: %+v", res)
	}
}

func TestSimulatorBlocksInvalidDefinitionsAndBoundsInput(t *testing.T) {
	res := simulate(t, `{"schema_version":1,"nodes":[],"edges":[]}`, Scenario{})
	if res.Status != "blocked" || len(res.Issues) == 0 || len(res.Steps) != 0 {
		t.Fatalf("a definition with blocking errors must not run: %+v", res)
	}
	if res := simulate(t, `{{{`, Scenario{}); res.Status != "blocked" {
		t.Fatalf("unparseable: %+v", res)
	}
	sim := NewSimulator(nil)
	for name, sc := range map[string]Scenario{
		"too many events":   {Events: make([]SimEvent, MaxSimEvents+1)},
		"timeout first":     {Events: []SimEvent{{Type: "timeout"}}},
		"unknown event":     {Events: []SimEvent{{Type: "message", Text: "a"}, {Type: "explode"}}},
		"bad contact kind":  {Contact: SimContact{Kind: "vip"}},
		"bad provider":      {Provider: "telegram"},
		"too many comp.":    {Companies: make([]SimCompany, MaxSimCompanies+1)},
		"oversized message": {Events: msgs(strings.Repeat("a", domain.MaxMessageRunes+1))},
	} {
		if len(sc.Events) > 0 && sc.Events[0].Type == "" {
			for i := range sc.Events {
				sc.Events[i] = SimEvent{Type: "message", Text: "x"}
			}
		}
		if _, err := sim.Simulate(simCtx(), SimInput{Raw: json.RawMessage(askFlow), Scenario: sc}); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if _, err := sim.Simulate(context.Background(), SimInput{Raw: json.RawMessage(askFlow)}); err == nil {
		t.Fatal("a simulation needs a tenant context")
	}
}

func TestSimulatorRedactsSecretsInVariables(t *testing.T) {
	res := simulate(t, askFlow, Scenario{Events: msgs("oi", "Bearer abcdefghijklmnop1234")})
	if got := fmt.Sprint(res.Variables["nome"]); strings.Contains(got, "abcdefghijklmnop1234") {
		t.Fatalf("secret leaked in the simulation result: %v", res.Variables)
	}
	for _, s := range res.Steps {
		if strings.Contains(string(s.Output), "abcdefghijklmnop1234") {
			t.Fatal("secret leaked in a step output")
		}
	}
}

// The simulation uses the real engine with every side-effect node, yet not one row is written anywhere.
func TestSimulationWritesNothingToTheDatabase(t *testing.T) {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	cp := NewControlPlane(repo, repo, nil)
	queue := uuid.New()
	if _, err := env.Seed.Exec(context.Background(), `INSERT INTO queues(id, tenant_id, name) VALUES($1,$2,'q')`, queue, env.TenantA); err != nil {
		t.Fatal(err)
	}
	convID, _ := env.SeedConversation(t, env.TenantA, "bot")
	def := fmt.Sprintf(`{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"hi","type":"send_message","config":{"text":"Olá"}},
	  {"id":"mk","type":"create_ticket","config":{"subject":"X","priority":"low"}},{"id":"q","type":"assign_queue","config":{"queue":%q}},{"id":"h","type":"human_handoff"}],
	  "edges":[{"id":"1","source":"start","sourcePort":"next","target":"hi"},{"id":"2","source":"hi","sourcePort":"next","target":"mk"},{"id":"3","source":"mk","sourcePort":"next","target":"q"},{"id":"4","source":"q","sourcePort":"next","target":"h"}]}`, queue)
	tables := []string{"flow_runs", "flow_node_executions", "messages", "outbox_events", "tickets", "conversations", "queues", "contacts", "flow_versions", "audit_events"}
	snapshot := func() (out []int) {
		for _, tb := range tables {
			var n int
			if err := env.Seed.QueryRow(context.Background(), `SELECT count(*) FROM `+tb).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		var mode string
		_ = env.Seed.QueryRow(context.Background(), `SELECT automation_mode FROM conversations WHERE id=$1`, convID).Scan(&mode)
		if mode != "bot" {
			t.Fatalf("a real conversation was touched: %s", mode)
		}
		return out
	}
	var flowID uuid.UUID
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		f, err := cp.Create(ctx, CreateInput{Slug: "sim", Name: "Sim"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cp.SaveDraft(ctx, f.ID, f.DraftRevision, f.Name, "", json.RawMessage(def)); err != nil {
			t.Fatal(err)
		}
		flowID = f.ID
	})
	before := snapshot()
	var res *SimResult
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		var err error
		res, err = cp.SimulateFlow(ctx, flowID, nil, Scenario{})
		if err != nil {
			t.Fatal(err)
		}
	})
	if res.Status != "waiting_human" || len(res.Effects) != 3 || len(res.Messages) != 1 {
		t.Fatalf("the simulation must have exercised every effect: %+v", res)
	}
	after := snapshot()
	for i := range tables {
		if before[i] != after[i] {
			t.Errorf("table %s changed during a simulation: %d -> %d", tables[i], before[i], after[i])
		}
	}
	// A definition the editor holds but has not saved can be simulated too, and blocking errors (a queue of another tenant) stop it.
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		foreign := uuid.New()
		_, _ = env.Seed.Exec(context.Background(), `INSERT INTO queues(id, tenant_id, name) VALUES($1,$2,'other')`, foreign, env.TenantB)
		bad := strings.Replace(def, queue.String(), foreign.String(), 1)
		res, err := cp.SimulateFlow(ctx, flowID, json.RawMessage(bad), Scenario{})
		if err != nil || res.Status != "blocked" || !hasCode(res.Issues, "resource_not_found") {
			t.Fatalf("a foreign queue must block the simulation: %v %+v", err, res)
		}
	})
}
