package overflow

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func step(pos int, kind string, id uuid.UUID, wait int, triggers ...Trigger) Step {
	return Step{Position: pos, Triggers: triggers, WaitSeconds: wait, DestKind: kind, DestID: id}
}

func elapsed(d time.Duration) func(bool) time.Duration { return func(bool) time.Duration { return d } }

func TestValidateRefusesWhatTheEngineCouldNotRunSafely(t *testing.T) {
	origin, n2, roster := uuid.New(), uuid.New(), uuid.New()
	ok := Policy{MaxSteps: 4, OnExhausted: ExhaustedManual, Steps: []Step{step(1, DestQueue, n2, 300, NoEligibleAgent), step(2, DestOnCall, roster, 600, MaxWait, NoEligibleAgent)}}
	if err := ok.Validate(origin); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(p *Policy){
		"no steps":               func(p *Policy) { p.Steps = nil },
		"too many steps":         func(p *Policy) { p.Steps = make([]Step, 9) },
		"bad max steps":          func(p *Policy) { p.MaxSteps = 0 },
		"bad exhausted":          func(p *Policy) { p.OnExhausted = "explode" },
		"gap in positions":       func(p *Policy) { p.Steps[1].Position = 3 },
		"no trigger":             func(p *Policy) { p.Steps[0].Triggers = nil },
		"unknown trigger":        func(p *Policy) { p.Steps[0].Triggers = []Trigger{"whenever"} },
		"duplicate trigger":      func(p *Policy) { p.Steps[0].Triggers = []Trigger{NoEligibleAgent, NoEligibleAgent} },
		"wait too long":          func(p *Policy) { p.Steps[0].WaitSeconds = 8 * 24 * 3600 },
		"negative wait":          func(p *Policy) { p.Steps[0].WaitSeconds = -1 },
		"max_wait without wait":  func(p *Policy) { p.Steps[1].WaitSeconds = 0 },
		"unknown destination":    func(p *Policy) { p.Steps[0].DestKind = "mars" },
		"nil destination":        func(p *Policy) { p.Steps[0].DestID = uuid.Nil },
		"back to the origin":     func(p *Policy) { p.Steps[0].DestID = origin },
		"same destination twice": func(p *Policy) { p.Steps[1].DestKind, p.Steps[1].DestID = DestQueue, n2 },
		"flow with a queue":      func(p *Policy) { p.Steps[0].DestKind = DestFlow; q := uuid.New(); p.Steps[0].DestQueueID = &q },
		"same flow twice": func(p *Policy) {
			f := uuid.New()
			p.Steps[0].DestKind, p.Steps[0].DestID = DestFlow, f
			p.Steps[1].DestKind, p.Steps[1].DestID = DestFlow, f
		},
	}
	for name, mut := range bad {
		p := Policy{MaxSteps: ok.MaxSteps, OnExhausted: ok.OnExhausted, Steps: append([]Step(nil), ok.Steps...)}
		for i := range p.Steps {
			p.Steps[i].Triggers = append([]Trigger(nil), ok.Steps[i].Triggers...)
		}
		mut(&p)
		if p.Validate(origin) == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestNextWalksTheChainInOrderAndHonoursWaits(t *testing.T) {
	origin, n2, roster := uuid.New(), uuid.New(), uuid.New()
	p := Policy{MaxSteps: 4, OnExhausted: ExhaustedManual, Steps: []Step{
		step(1, DestQueue, n2, 300, NoEligibleAgent, CapacityExhausted),
		step(2, DestOnCall, roster, 900, NoEligibleAgent, MaxWait),
	}}
	visited := map[string]bool{DestKey(DestQueue, origin): true}
	// 2 minutes in the origin: step 1 wants 5, so the answer is "come back in 3 minutes"
	sel := p.Next(0, NoEligibleAgent, visited, 0, elapsed(2*time.Minute))
	if sel.Step != nil || sel.Remaining != 3*time.Minute || sel.WaitFor.Position != 1 {
		t.Fatalf("waiting for step 1: %+v", sel)
	}
	// 5 minutes: enter step 1
	sel = p.Next(0, NoEligibleAgent, visited, 0, elapsed(5*time.Minute))
	if sel.Step == nil || sel.Step.Position != 1 {
		t.Fatalf("enter step 1: %+v", sel)
	}
	visited[sel.Step.Key()] = true
	// now in step 1 for 3 minutes: step 2 wants 15
	sel = p.Next(1, NoEligibleAgent, visited, 1, elapsed(3*time.Minute))
	if sel.Step != nil || sel.Remaining != 12*time.Minute {
		t.Fatalf("waiting for step 2: %+v", sel)
	}
	sel = p.Next(1, NoEligibleAgent, visited, 1, elapsed(15*time.Minute))
	if sel.Step == nil || sel.Step.Position != 2 {
		t.Fatalf("enter step 2: %+v", sel)
	}
	visited[sel.Step.Key()] = true
	// past the last step: the trigger holds and the policy has steps for it, but none can be entered => exhausted
	sel = p.Next(2, NoEligibleAgent, visited, 2, elapsed(time.Hour))
	if sel.Step != nil || sel.Exhausted {
		// nothing after position 2 matches at all: that is NOT exhaustion by loop, it is the end of the chain
		t.Fatalf("after the last step there is no candidate: %+v", sel)
	}
	// a trigger no step lists is simply not handled by the chain
	sel = p.Next(0, AfterHours, visited, 0, elapsed(time.Hour))
	if sel.Step != nil || sel.Exhausted || sel.Remaining != 0 {
		t.Fatalf("a trigger without steps: %+v", sel)
	}
	// a step that lists the trigger but cannot be entered (visited) is skipped and, with nothing left, the chain is exhausted
	v2 := map[string]bool{DestKey(DestQueue, origin): true, DestKey(DestQueue, n2): true, DestKey(DestOnCall, roster): true}
	sel = p.Next(0, NoEligibleAgent, v2, 1, elapsed(time.Hour))
	if sel.Step != nil || !sel.Exhausted || len(sel.Skipped) != 2 {
		t.Fatalf("every candidate already visited: %+v", sel)
	}
}

func TestTheHopLimitStopsTheChainEvenWhenDestinationsAreFresh(t *testing.T) {
	origin, a, b := uuid.New(), uuid.New(), uuid.New()
	p := Policy{MaxSteps: 1, OnExhausted: ExhaustedContingency, Steps: []Step{step(1, DestQueue, a, 0, NoEligibleAgent), step(2, DestQueue, b, 0, NoEligibleAgent)}}
	visited := map[string]bool{DestKey(DestQueue, origin): true}
	if sel := p.Next(0, NoEligibleAgent, visited, 0, elapsed(0)); sel.Step == nil || sel.Step.Position != 1 {
		t.Fatalf("first hop: %+v", sel)
	}
	sel := p.Next(1, NoEligibleAgent, map[string]bool{DestKey(DestQueue, origin): true, DestKey(DestQueue, a): true}, 1, elapsed(0))
	if sel.Step != nil || !sel.Exhausted || sel.Skipped[0].Reason != SkipHopLimit {
		t.Fatalf("the hop limit is reached: %+v", sel)
	}
}

func TestAnEarlierStepDoesNotLetALaterOneJumpTheQueue(t *testing.T) {
	origin, a, b := uuid.New(), uuid.New(), uuid.New()
	p := Policy{MaxSteps: 4, OnExhausted: ExhaustedManual, Steps: []Step{step(1, DestQueue, a, 600, MaxWait), step(2, DestQueue, b, 60, MaxWait)}}
	sel := p.Next(0, MaxWait, map[string]bool{DestKey(DestQueue, origin): true}, 0, elapsed(2*time.Minute))
	if sel.Step != nil || sel.WaitFor.Position != 1 {
		t.Fatalf("step 2 must not be entered before step 1: %+v", sel)
	}
}

func TestNewDestinationKindsAreValidatedAndKeepTheLoopGuard(t *testing.T) {
	origin, n2, user, sector, contract := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	hubQ := uuid.New()
	hubStep := Step{Position: 3, Triggers: []Trigger{NoEligibleAgent}, DestKind: DestHub, DestID: contract, DestQueueID: &hubQ}
	ok := Policy{MaxSteps: 5, OnExhausted: ExhaustedManual, Steps: []Step{
		step(1, DestAgent, user, 0, NoEligibleAgent), step(2, DestSector, sector, 0, NoEligibleAgent), hubStep}}
	if err := ok.Validate(origin); err != nil {
		t.Fatal(err)
	}
	if hubStep.Key() != "hub:"+contract.String() {
		t.Fatalf("hub key: %s", hubStep.Key())
	}
	bad := map[string]func(p *Policy){
		"hub without a queue":         func(p *Policy) { p.Steps[2].DestQueueID = nil },
		"hub serving from the origin": func(p *Policy) { o := origin; p.Steps[2].DestQueueID = &o },
		"queue id on a non-hub":       func(p *Policy) { q := n2; p.Steps[0].DestQueueID = &q },
		"hub queue reused by a queue step": func(p *Policy) {
			p.Steps = append(p.Steps[:2:2], step(3, DestQueue, hubQ, 0, NoEligibleAgent), Step{Position: 4, Triggers: []Trigger{NoEligibleAgent}, DestKind: DestHub, DestID: contract, DestQueueID: &hubQ})
		},
		"same agent twice": func(p *Policy) { p.Steps[1] = step(2, DestAgent, user, 0, NoEligibleAgent) },
		"unknown kind":     func(p *Policy) { p.Steps[0].DestKind = "mars" },
	}
	for name, mut := range bad {
		p := Policy{MaxSteps: ok.MaxSteps, OnExhausted: ok.OnExhausted, Steps: append([]Step(nil), ok.Steps...)}
		mut(&p)
		if p.Validate(origin) == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestAFlowIsAValidDestinationAndCountsAsAVisitedStep(t *testing.T) {
	origin, flow, n2 := uuid.New(), uuid.New(), uuid.New()
	p := Policy{MaxSteps: 4, OnExhausted: ExhaustedManual, Steps: []Step{step(1, DestFlow, flow, 0, AfterHours), step(2, DestQueue, n2, 0, AfterHours)}}
	if err := p.Validate(origin); err != nil {
		t.Fatalf("a flow destination is valid: %v", err)
	}
	visited := map[string]bool{DestKey(DestQueue, origin): true}
	sel := p.Next(0, AfterHours, visited, 0, elapsed(0))
	if sel.Step == nil || sel.Step.DestKind != DestFlow {
		t.Fatalf("the flow is step 1: %+v", sel)
	}
	visited[sel.Step.Key()] = true // what the engine records once it started the flow
	sel = p.Next(1, AfterHours, visited, 1, elapsed(0))
	if sel.Step == nil || sel.Step.DestKind != DestQueue {
		t.Fatalf("after the flow the chain goes on to step 2, never back to the flow: %+v", sel)
	}
}
