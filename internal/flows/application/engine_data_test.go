package application_test

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

func lastNode(w *world) string { return w.runs.execs[len(w.runs.execs)-1].NodeID }

func TestEveryCatalogNodeHasAnExecutor(t *testing.T) {
	have := map[domain.NodeType]bool{}
	for _, e := range AllExecutors() {
		if have[e.Type()] {
			t.Errorf("duplicate executor for %s", e.Type())
		}
		have[e.Type()] = true
	}
	var missing, extra []string
	for _, s := range domain.Specs() {
		if !have[s.Type] {
			missing = append(missing, string(s.Type))
		}
		delete(have, s.Type)
	}
	for t := range have {
		extra = append(extra, string(t))
	}
	sort.Strings(missing)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("catalog and executors must match: missing executors %v, executors without a spec %v", missing, extra)
	}
}

var contextFlow = wf(`{"id":"start","type":"trigger"},{"id":"c","type":"resolve_contact"},{"id":"k","type":"resolve_customer_context"},
  {"id":"pick","type":"customer_choice","config":{"text":"Qual empresa?"}},
  {"id":"known","type":"end"},{"id":"unknown","type":"end"},{"id":"none","type":"end"},{"id":"single","type":"end"},{"id":"chosen","type":"end"},{"id":"gaveup","type":"end"}`,
	strings.Join([]string{
		edge("1", "start", "next", "c"), edge("2", "c", "known", "k"), edge("3", "c", "unknown", "unknown"),
		edge("4", "k", "none", "none"), edge("5", "k", "single", "single"), edge("6", "k", "multiple", "pick"),
		edge("7", "pick", "selected", "chosen"), edge("8", "pick", "timeout", "gaveup"),
	}, ","), "")

func TestResolveContactDoesNotTreatUnknownAsACustomer(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "ctx", def: contextFlow})
	w.conv.ContactKind = "unclassified"
	w.inbound("oi", true)
	if lastNode(w) != "unknown" || len(w.fx.activeSet) != 0 {
		t.Fatalf("an unclassified contact stays unknown: %s", lastNode(w))
	}
}

func TestCustomerContextNoneSingleAndMultiple(t *testing.T) {
	a, b := ports.CustomerCandidate{AccountID: uuid.New(), Name: "ACME"}, ports.CustomerCandidate{AccountID: uuid.New(), Name: "Beta"}
	// no company: nothing is created or guessed
	w := newWorld(t, flowSpec{slug: "ctx", def: contextFlow})
	w.inbound("oi", true)
	if lastNode(w) != "none" || len(w.fx.activeSet) != 0 {
		t.Fatalf("none: %s", lastNode(w))
	}
	// exactly one company: selected automatically and recorded on the run
	w = newWorld(t, flowSpec{slug: "ctx", def: contextFlow})
	w.fx.candidates = []ports.CustomerCandidate{a}
	w.inbound("oi", true)
	r := w.onlyRun()
	if lastNode(w) != "single" || len(w.fx.activeSet) != 1 || w.fx.activeSet[0] != a.AccountID || r.ActiveCustomerAccountID == nil || *r.ActiveCustomerAccountID != a.AccountID {
		t.Fatalf("single: %s %v", lastNode(w), w.fx.activeSet)
	}
	// several companies: the bot must NOT pick one; it asks, and waits
	w = newWorld(t, flowSpec{slug: "ctx", def: contextFlow})
	w.fx.candidates = []ports.CustomerCandidate{a, b}
	w.inbound("oi", true)
	r = w.onlyRun()
	if r.Status != domain.RunWaitingInput || r.CurrentNodeID != "pick" || len(w.fx.activeSet) != 0 || r.ActiveCustomerAccountID != nil {
		t.Fatalf("multiple must wait for the contact, selecting nothing: %s %v", r.Status, w.fx.activeSet)
	}
	if got := w.fx.sent[len(w.fx.sent)-1]; got != "Qual empresa?\n1) ACME\n2) Beta" {
		t.Fatalf("menu: %q", got)
	}
	// an unrecognised reply re-asks; "2" selects Beta
	w.inbound("a primeira", false)
	if r = w.onlyRun(); r.Status != domain.RunWaitingInput || len(w.fx.activeSet) != 0 {
		t.Fatalf("invalid reply must re-ask: %s", r.Status)
	}
	w.inbound("2", false)
	r = w.onlyRun()
	if lastNode(w) != "chosen" || len(w.fx.activeSet) != 1 || w.fx.activeSet[0] != b.AccountID || *r.ActiveCustomerAccountID != b.AccountID {
		t.Fatalf("selection: %s %v", lastNode(w), w.fx.activeSet)
	}
	// candidates must not linger in the run variables once chosen
	if _, still := r.Variables["_candidates"]; still && r.Variables["_candidates"] != nil {
		t.Fatalf("candidates must be cleared after the choice: %v", r.Variables["_candidates"])
	}
	// three invalid replies fall to the timeout port (default 3 attempts)
	w = newWorld(t, flowSpec{slug: "ctx", def: contextFlow})
	w.fx.candidates = []ports.CustomerCandidate{a, b}
	w.inbound("oi", true)
	w.inbound("x", false)
	w.inbound("y", false)
	w.inbound("z", false)
	if lastNode(w) != "gaveup" || len(w.fx.activeSet) != 0 {
		t.Fatalf("exhausted attempts: %s", lastNode(w))
	}
}

func TestAlreadyConfirmedCustomerIsKeptNotReasked(t *testing.T) {
	a, b := ports.CustomerCandidate{AccountID: uuid.New(), Name: "ACME"}, ports.CustomerCandidate{AccountID: uuid.New(), Name: "Beta"}
	w := newWorld(t, flowSpec{slug: "ctx", def: contextFlow})
	w.fx.candidates = []ports.CustomerCandidate{a, b}
	w.conv.ActiveCustomerAccountID = &b.AccountID // confirmed earlier in this conversation
	w.inbound("oi", true)
	if lastNode(w) != "single" || len(w.fx.sent) != 0 || len(w.fx.activeSet) != 0 {
		t.Fatalf("confirmed company must be reused without asking: %s sent=%v", lastNode(w), w.fx.sent)
	}
	// ... but a confirmed company the contact is no longer linked to is not trusted
	w = newWorld(t, flowSpec{slug: "ctx", def: contextFlow})
	w.fx.candidates = []ports.CustomerCandidate{a}
	stale := uuid.New()
	w.conv.ActiveCustomerAccountID = &stale
	w.inbound("oi", true)
	if len(w.fx.activeSet) != 1 || w.fx.activeSet[0] != a.AccountID {
		t.Fatalf("a stale confirmation must be re-resolved: %v", w.fx.activeSet)
	}
}

func TestTicketsHandoffAndErrorRouting(t *testing.T) {
	queue := uuid.New()
	flow := wf(fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"find","type":"find_open_tickets"},
	  {"id":"ask","type":"ask","config":{"text":"Descreva","variable":"problema"}},
	  {"id":"mk","type":"create_ticket","config":{"subject":"VPN - {{problema}} ({{contact.name}})","priority":"high"}},
	  {"id":"q","type":"assign_queue","config":{"queue":%q}},
	  {"id":"h","type":"human_handoff","config":{"queue":%q,"summary":"{{problema}} / ticket {{tickets.first_id}}"}},
	  {"id":"related","type":"end"},{"id":"giveup","type":"end"},{"id":"mkerr","type":"end"}`, queue, queue),
		strings.Join([]string{edge("1", "start", "next", "find"), edge("2", "find", "none", "ask"), edge("3", "find", "found", "related"),
			edge("4", "ask", "next", "mk"), edge("5", "ask", "timeout", "giveup"), edge("6", "mk", "next", "q"), edge("7", "q", "next", "h"), edge("8", "mk", "error", "mkerr")}, ","), "")
	w := newWorld(t, flowSpec{slug: "vpn", def: flow})
	w.inbound("oi", true)
	w.inbound("a VPN caiu", false)
	r := w.onlyRun()
	if len(w.fx.ensured) != 1 || w.fx.ensured[0] != "VPN - a VPN caiu (Ana)|high" {
		t.Fatalf("ticket: %v", w.fx.ensured)
	}
	if r.Status != domain.RunWaitingHuman || w.conv.AutomationMode != domain.AutomationWaitingHuman || w.fx.handoffs != 1 || len(w.fx.explicitQ) != 1 || w.fx.explicitQ[0] != queue {
		t.Fatalf("handoff: status=%s mode=%s handoffs=%d", r.Status, w.conv.AutomationMode, w.fx.handoffs)
	}
	h, _ := r.Variables["_handoff"].(map[string]any)
	if !strings.HasPrefix(fmt.Sprint(h["summary"]), "a VPN caiu / ticket ") {
		t.Fatalf("handoff context for the operator: %v", h)
	}
	if len(w.logs) != 0 || len(w.fx.assigned) != 1 {
		t.Fatalf("a handoff is not a failure and must not release the conversation to the default queue: logs=%v assigned=%d", w.logs, len(w.fx.assigned))
	}
	// open tickets found => the author's 'found' branch
	w = newWorld(t, flowSpec{slug: "vpn", def: flow})
	w.fx.tickets = ports.TicketSummary{Count: 2, FirstID: "t-1", FirstSubject: "Link down"}
	w.inbound("oi", true)
	if lastNode(w) != "related" {
		t.Fatalf("found: %s", lastNode(w))
	}
	// an effect failure follows the optional error port when wired...
	w = newWorld(t, flowSpec{slug: "vpn", def: flow})
	w.fx.ensureErr = errors.New("db down")
	w.inbound("oi", true)
	w.inbound("x", false)
	if lastNode(w) != "mkerr" || w.onlyRun().Status != domain.RunCompleted {
		t.Fatalf("error port: %s", lastNode(w))
	}
	// ... and fails the run cleanly (conversation back to humans) when not wired
	noErr := strings.Replace(flow, `,`+edge("8", "mk", "error", "mkerr"), "", 1)
	w = newWorld(t, flowSpec{slug: "vpn", def: noErr})
	w.fx.assignErr = errors.New("queue gone")
	w.inbound("oi", true)
	w.inbound("x", false)
	if r := w.onlyRun(); r.Status != domain.RunFailed || !strings.Contains(r.Error, "queue gone") {
		t.Fatalf("unwired error must fail the run: %+v", r)
	}
}
