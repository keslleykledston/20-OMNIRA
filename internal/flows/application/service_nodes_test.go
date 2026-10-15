package application_test

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/omnira/omnira/internal/flows/application"
)

// ADR-0049 phase 6 in the simulator: it records what each service node WOULD do and never reaches the module.

func serviceFlowDef() string {
	return wf(`{"id":"start","type":"trigger"},
	  {"id":"s","type":"start_service_session","config":{"variable":"protocolo"}},
	  {"id":"c","type":"classify_service_session","config":{"category":"suporte"}},
	  {"id":"t","type":"create_service_task","config":{"title":"Retornar {{protocolo}}","priority":"high"}},
	  {"id":"o","type":"open_service_case","config":{"title":"Falha {{protocolo}}","variable":"ocorrencia"}},
	  {"id":"m","type":"send_message","config":{"text":"Protocolo {{protocolo}}, ocorrência {{ocorrencia}}"}},
	  {"id":"x","type":"close_service_session","config":{"result":"information_provided"}},
	  {"id":"fin","type":"end"}`,
		strings.Join([]string{edge("1", "start", "next", "s"), edge("2", "s", "next", "c"), edge("3", "c", "next", "t"), edge("4", "t", "next", "o"),
			edge("5", "o", "next", "m"), edge("6", "m", "next", "x"), edge("7", "x", "next", "fin")}, ","), "")
}

func TestSimulatorRecordsTheServiceNodesAndFeedsTheirVariables(t *testing.T) {
	res := simulate(t, serviceFlowDef(), Scenario{})
	if res.Status != "completed" {
		t.Fatalf("status %s: %+v", res.Status, res.Steps)
	}
	for _, kind := range []string{"start_service_session", "classify_service_session", "create_service_task", "open_service_case", "close_service_session"} {
		if got := effectsOf(res, kind); len(got) != 1 {
			t.Errorf("%s: want one recorded effect, got %d", kind, len(got))
		}
	}
	if len(res.Messages) == 0 || !strings.Contains(res.Messages[0].Text, "Protocolo AT-SIM, ocorrência OC-SIM") {
		t.Fatalf("the variables of the nodes reach the next message: %+v", res.Messages)
	}
}

// A node whose module is not available (a real environment without it) takes its error port when wired, and fails the run otherwise.
func TestServiceNodesWithoutTheModuleTakeTheirErrorPort(t *testing.T) {
	def := func(wired bool) string {
		edges := []string{edge("1", "start", "next", "s"), edge("2", "s", "next", "ok")}
		if wired {
			edges = append(edges, edge("3", "s", "error", "bad"))
		}
		return wf(`{"id":"start","type":"trigger"},{"id":"s","type":"start_service_session"},{"id":"ok","type":"end"},{"id":"bad","type":"end"}`, strings.Join(edges, ","), "")
	}
	w := newWorld(t, flowSpec{slug: "svc", def: def(true)}) // plain fakeEffects: no ServiceFlow
	if _, _, err := w.inbound("oi", true); err != nil {
		t.Fatal(err)
	}
	run := w.onlyRun()
	if run == nil || string(run.Status) != "completed" {
		t.Fatalf("the error port is wired: the run completes by it: %+v", run)
	}
	w2 := newWorld(t, flowSpec{slug: "svc", def: def(false)})
	_, _, _ = w2.inbound("oi", true)
	if r := w2.onlyRun(); r == nil || string(r.Status) == "completed" {
		t.Fatalf("without an error port the run must not pretend to succeed: %+v", r)
	}
	_ = fmt.Sprint()
}
