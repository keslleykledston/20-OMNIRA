package application_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	. "github.com/omnira/omnira/internal/flows/application"
)

// ADR-0044 routing nodes in the simulator: they ask the (scripted) shared services and only map the answer to a port.

// effectsOf lists the recorded effects of one kind (the engine's end-of-flow safety net, return_to_queue, is not under test here).
func effectsOf(res *SimResult, kind string) []SimEffect {
	var out []SimEffect
	for _, e := range res.Effects {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func scheduleFlow(sector uuid.UUID, withUnavailable bool) string {
	edges := []string{edge("1", "start", "next", "chk"), edge("2", "chk", "open", "yes"), edge("3", "chk", "closed", "no")}
	if withUnavailable {
		edges = append(edges, edge("4", "chk", "unavailable", "gone"))
	}
	return wf(fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"chk","type":"check_schedule","config":{"sector":%q}},
	  {"id":"yes","type":"end"},{"id":"no","type":"end"},{"id":"gone","type":"end"}`, sector), strings.Join(edges, ","), "")
}

func TestSimulatorCheckSchedulePortsFollowTheServiceAnswer(t *testing.T) {
	sec := uuid.New()
	for state, want := range map[string]string{"open": "yes", "closed": "no", "unavailable": "gone"} {
		res := simulate(t, scheduleFlow(sec, true), Scenario{Routing: &SimRouting{Places: map[string]SimPlace{sec.String(): {Schedule: state}}}})
		if !res.Reached(want) || res.Status != "completed" {
			t.Fatalf("%s must reach %s: %+v", state, want, res.Steps)
		}
	}
	// default of a simulation: open
	if res := simulate(t, scheduleFlow(sec, true), Scenario{}); !res.Reached("yes") {
		t.Fatalf("default is open: %+v", res.Steps)
	}
	// a gone sector with no "unavailable" edge falls back to "closed" (never promise attendance that may not exist)
	res := simulate(t, scheduleFlow(sec, false), Scenario{Routing: &SimRouting{Places: map[string]SimPlace{sec.String(): {Schedule: "unavailable"}}}})
	if !res.Reached("no") {
		t.Fatalf("unavailable without its own edge must read as closed: %+v", res.Steps)
	}
}

func TestSimulatorCheckAvailabilityAndOverflow(t *testing.T) {
	q := uuid.New()
	flow := wf(fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"av","type":"check_availability","config":{"queue":%q}},
	  {"id":"ov","type":"apply_overflow"},{"id":"ok","type":"end"},{"id":"wait","type":"end"},{"id":"manual","type":"end"},{"id":"gone","type":"end"}`, q),
		strings.Join([]string{edge("1", "start", "next", "av"), edge("2", "av", "available", "ok"), edge("3", "av", "unavailable", "ov"),
			edge("4", "ov", "assigned", "ok"), edge("5", "ov", "queued", "wait"), edge("6", "ov", "manual", "manual"), edge("7", "av", "missing", "gone")}, ","), "")
	res := simulate(t, flow, Scenario{Routing: &SimRouting{Places: map[string]SimPlace{q.String(): {Availability: "unavailable"}}, Overflow: "manual"}})
	if !res.Reached("manual") || len(effectsOf(res, "apply_overflow")) != 1 {
		t.Fatalf("unavailable -> overflow -> manual: %+v %+v", res.Steps, res.Effects)
	}
	// "waiting_open" without its own edge reads as the generic "queued"
	res = simulate(t, flow, Scenario{Routing: &SimRouting{Places: map[string]SimPlace{q.String(): {Availability: "unavailable"}}, Overflow: "waiting_open"}})
	if !res.Reached("wait") {
		t.Fatalf("waiting_open must fall back to queued: %+v", res.Steps)
	}
	if res := simulate(t, flow, Scenario{Routing: &SimRouting{Places: map[string]SimPlace{q.String(): {Availability: "missing"}}}}); !res.Reached("gone") {
		t.Fatalf("a missing place takes the missing port: %+v", res.Steps)
	}
	// "closed" with no closed edge reads as unavailable and goes to the overflow
	if res := simulate(t, flow, Scenario{Routing: &SimRouting{Places: map[string]SimPlace{q.String(): {Availability: "closed"}}}}); !res.Reached("wait") {
		t.Fatalf("closed without its own edge reads as unavailable: %+v", res.Steps)
	}
}

func TestSimulatorSelectSectorThenAssign(t *testing.T) {
	fin, com := uuid.New(), uuid.New()
	flow := wf(`{"id":"start","type":"trigger"},{"id":"pick","type":"select_sector","config":{"text":"Qual setor?"}},
	  {"id":"go","type":"assign_sector","config":{"from_variable":"sector"}},{"id":"done","type":"end"},{"id":"out","type":"end"},{"id":"nosec","type":"end"}`,
		strings.Join([]string{edge("1", "start", "next", "pick"), edge("2", "pick", "selected", "go"), edge("3", "pick", "timeout", "out"),
			edge("4", "pick", "none", "nosec"), edge("5", "go", "next", "done")}, ","), "")
	sc := Scenario{Routing: &SimRouting{Sectors: []SimSector{{ID: fin.String(), Name: "Financeiro"}, {ID: com.String(), Name: "Comercial"}}}, Events: msgs("oi", "2")}
	res := simulate(t, flow, sc)
	if res.Status != "completed" || !res.Reached("done") {
		t.Fatalf("choosing option 2 must route to the second sector: %+v", res)
	}
	if len(res.Messages) != 1 || !strings.Contains(res.Messages[0].Text, "1) Financeiro") || !strings.Contains(res.Messages[0].Text, "2) Comercial") {
		t.Fatalf("the menu is the sectors read at run time: %+v", res.Messages)
	}
	if len(effectsOf(res, "assign_sector")) != 1 || effectsOf(res, "assign_sector")[0].Detail["sector"] != com.String() {
		t.Fatalf("the chosen sector is the one assigned: %+v", res.Effects)
	}
	// no active sector: the run takes the none port without sending an empty menu
	res = simulate(t, flow, Scenario{Routing: &SimRouting{}, Events: msgs("oi")})
	if !res.Reached("nosec") || len(res.Messages) != 0 {
		t.Fatalf("no sectors -> none port, no message: %+v", res)
	}
}

func TestSimulatorAssignSectorUnavailableTakesItsPort(t *testing.T) {
	sec := uuid.New()
	flow := wf(fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"go","type":"assign_sector","config":{"sector":%q}},{"id":"done","type":"end"},{"id":"gone","type":"end"}`, sec),
		strings.Join([]string{edge("1", "start", "next", "go"), edge("2", "go", "next", "done"), edge("3", "go", "unavailable", "gone")}, ","), "")
	res := simulate(t, flow, Scenario{Routing: &SimRouting{Places: map[string]SimPlace{sec.String(): {Schedule: "unavailable"}}}})
	if !res.Reached("gone") || len(effectsOf(res, "assign_sector")) != 0 {
		t.Fatalf("an unavailable sector must not assign: %+v %+v", res.Steps, res.Effects)
	}
}

func TestRoutingNodesValidation(t *testing.T) {
	q, s := uuid.New(), uuid.New()
	cases := map[string]string{
		"both":    fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"c","type":"check_schedule","config":{"queue":%q,"sector":%q}},{"id":"a","type":"end"},{"id":"b","type":"end"}`, q, s),
		"none":    `{"id":"start","type":"trigger"},{"id":"c","type":"check_schedule","config":{}},{"id":"a","type":"end"},{"id":"b","type":"end"}`,
		"assign":  `{"id":"start","type":"trigger"},{"id":"c","type":"assign_sector","config":{}},{"id":"a","type":"end"}`,
		"assign2": fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"c","type":"assign_sector","config":{"sector":%q,"from_variable":"sector"}},{"id":"a","type":"end"}`, s),
	}
	for name, nodes := range cases {
		edges := edge("1", "start", "next", "c") + "," + edge("2", "c", "open", "a") + "," + edge("3", "c", "closed", "b")
		if strings.HasPrefix(name, "assign") {
			edges = edge("1", "start", "next", "c") + "," + edge("2", "c", "next", "a")
		}
		res := simulate(t, wf(nodes, edges, ""), Scenario{})
		if res.Status != "blocked" {
			t.Fatalf("%s must be blocked by validation: %+v", name, res.Issues)
		}
	}
}
