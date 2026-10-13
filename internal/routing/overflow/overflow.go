// Package overflow is the pure domain of the overflow chain (ADR-0044 phase 5): the policy, its validation and the choice of the next
// step. No I/O: the engine feeds it facts (the trigger that holds, where the conversation has been, how long it has waited).
//
//	ETAPA 1 queue N1 (origin)  --5 min, no agent-->  ETAPA 2 queue N2  --10 min-->  ETAPA 3 on-call  --exhausted-->  contingency
//
// The chain cannot loop: the origin and every destination already tried are in `visited`; a destination already visited is never entered
// again; `hops` can never exceed the policy's max_steps.
package overflow

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

type Trigger string

const (
	AfterHours         Trigger = "after_hours"
	NoEligibleAgent    Trigger = "no_eligible_agent"
	CapacityExhausted  Trigger = "capacity_exhausted"
	MaxWait            Trigger = "max_wait"
	SLARisk            Trigger = "sla_risk"
	SLABreach          Trigger = "sla_breach"
	OperationalFailure Trigger = "operational_failure"
)

var AllTriggers = []Trigger{AfterHours, NoEligibleAgent, CapacityExhausted, MaxWait, SLARisk, SLABreach, OperationalFailure}

const (
	DestQueue  = "queue"
	DestOnCall = "oncall"
	DestAgent  = "agent"  // a named attendant of the company
	DestSector = "sector" // the first useful queue of a sector
	DestHub    = "hub"    // a Hub that serves the company: DestID is the CONTRACT, DestQueueID the company queue it serves from
	DestFlow   = "flow"   // a published automation flow (INBOUND or AFTER_HOURS) of the company, started on the conversation; only when no flow is running on it

	ExhaustedKeepWaiting = "keep_waiting"
	ExhaustedContingency = "contingency"
	ExhaustedManual      = "manual"

	MaxStepsLimit = 8
	MaxWait7Days  = 7 * 24 * time.Hour
)

type Step struct {
	Position     int       `json:"position"`
	Name         string    `json:"name,omitempty"`
	Triggers     []Trigger `json:"triggers"`
	WaitSeconds  int       `json:"wait_seconds"`
	WaitBusiness bool      `json:"wait_business"`
	DestKind     string    `json:"destination_kind"`
	DestID       uuid.UUID `json:"destination_id"`
	// DestQueueID is required for a Hub destination (the company queue the Hub serves from, which its contract must cover) and forbidden otherwise.
	DestQueueID   *uuid.UUID `json:"destination_queue_id,omitempty"`
	AllowClosed   bool       `json:"allow_closed"`
	AllowOffShift bool       `json:"allow_off_shift"`
}

// Key is the canonical identity of the step's destination, the string the loop guard compares.
func (s Step) Key() string { return DestKey(s.DestKind, s.DestID) }

func DestKey(kind string, id uuid.UUID) string { return kind + ":" + id.String() }

func (s Step) Has(t Trigger) bool {
	for _, x := range s.Triggers {
		if x == t {
			return true
		}
	}
	return false
}

type Policy struct {
	ID          uuid.UUID `json:"id,omitempty"`
	QueueID     uuid.UUID `json:"queue_id"`
	Active      bool      `json:"active"`
	MaxSteps    int       `json:"max_steps"`
	OnExhausted string    `json:"on_exhausted"`
	Revision    int       `json:"revision,omitempty"`
	Steps       []Step    `json:"steps"`
}

func validTrigger(t Trigger) bool {
	for _, x := range AllTriggers {
		if x == t {
			return true
		}
	}
	return false
}

// Validate refuses everything the engine could not run safely. origin is the queue that owns the policy.
func (p Policy) Validate(origin uuid.UUID) error {
	if p.MaxSteps < 1 || p.MaxSteps > MaxStepsLimit {
		return fmt.Errorf("max_steps must be between 1 and %d", MaxStepsLimit)
	}
	switch p.OnExhausted {
	case ExhaustedKeepWaiting, ExhaustedContingency, ExhaustedManual:
	default:
		return fmt.Errorf("on_exhausted must be keep_waiting, contingency or manual")
	}
	if len(p.Steps) < 1 || len(p.Steps) > MaxStepsLimit {
		return fmt.Errorf("a policy has 1 to %d steps", MaxStepsLimit)
	}
	seen := map[string]bool{DestKey(DestQueue, origin): true} // the origin counts as visited: a step may never send the conversation back to it
	for i, s := range p.Steps {
		if s.Position != i+1 {
			return fmt.Errorf("step positions must be 1..%d in order (found %d at index %d)", len(p.Steps), s.Position, i)
		}
		if len(s.Triggers) < 1 {
			return fmt.Errorf("step %d: at least one trigger", s.Position)
		}
		dup := map[Trigger]bool{}
		for _, t := range s.Triggers {
			if !validTrigger(t) {
				return fmt.Errorf("step %d: unknown trigger %q", s.Position, t)
			}
			if dup[t] {
				return fmt.Errorf("step %d: trigger %q twice", s.Position, t)
			}
			dup[t] = true
		}
		if s.WaitSeconds < 0 || time.Duration(s.WaitSeconds)*time.Second > MaxWait7Days {
			return fmt.Errorf("step %d: wait must be between 0 and 7 days", s.Position)
		}
		if s.Has(MaxWait) && s.WaitSeconds == 0 {
			return fmt.Errorf("step %d: the max_wait trigger needs a wait greater than zero", s.Position)
		}
		switch s.DestKind {
		case DestQueue, DestOnCall, DestAgent, DestSector, DestFlow:
			if s.DestQueueID != nil {
				return fmt.Errorf("step %d: destination_queue_id is only for a Hub destination", s.Position)
			}
		case DestHub:
			if s.DestQueueID == nil || *s.DestQueueID == uuid.Nil {
				return fmt.Errorf("step %d: a Hub destination needs the company queue it serves from (destination_queue_id)", s.Position)
			}
			if *s.DestQueueID == origin {
				return fmt.Errorf("step %d: the Hub cannot serve from the origin queue (a chain cannot revisit it)", s.Position)
			}
			if seen[DestKey(DestQueue, *s.DestQueueID)] {
				return fmt.Errorf("step %d: that queue is already used by another step", s.Position)
			}
			seen[DestKey(DestQueue, *s.DestQueueID)] = true
		default:
			return fmt.Errorf("step %d: destination kind must be queue, oncall, agent, sector, hub or flow", s.Position)
		}
		if s.DestID == uuid.Nil {
			return fmt.Errorf("step %d: destination is required", s.Position)
		}
		if len(s.Name) > 60 {
			return fmt.Errorf("step %d: name longer than 60 characters", s.Position)
		}
		if seen[s.Key()] {
			return fmt.Errorf("step %d: destination already used (a chain cannot revisit a destination, the origin included)", s.Position)
		}
		seen[s.Key()] = true
	}
	return nil
}

// Skip says why a candidate step was not entered.
type Skip struct {
	Position int
	Reason   string
}

const (
	SkipVisited  = "destination_visited"
	SkipHopLimit = "hop_limit"
	SkipRejected = "destination_rejected" // set by the engine when the live destination check fails
)

// Selection is the answer of Next.
type Selection struct {
	Step      *Step         // the step to enter now; nil if none
	Remaining time.Duration // > 0: the next candidate exists but its wait has not elapsed; come back in this long
	WaitFor   *Step         // the step we are waiting for (when Remaining > 0)
	Skipped   []Skip
	Exhausted bool // the trigger holds, the policy has steps for it, but none can be entered (all visited / over the hop limit)
}

// Next picks the first step AFTER `current` (0 = the origin) that has the trigger, was not visited, and fits in the hop limit. The wait
// of a step counts from the moment the conversation entered `current`; elapsed(true) is that time in the origin's business time,
// elapsed(false) in wall-clock time. Candidates are evaluated in position order and the first whose wait has not elapsed ends the search:
// steps are ordered by their waits, and a later step must never jump the queue.
func (p Policy) Next(current int, t Trigger, visited map[string]bool, hops int, elapsed func(business bool) time.Duration) Selection {
	var sel Selection
	matched := false
	cands := make([]Step, 0, len(p.Steps))
	for _, s := range p.Steps {
		if s.Position > current && s.Has(t) {
			cands = append(cands, s)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Position < cands[j].Position })
	for i := range cands {
		s := cands[i]
		matched = true
		if visited[s.Key()] {
			sel.Skipped = append(sel.Skipped, Skip{s.Position, SkipVisited})
			continue
		}
		if hops >= p.MaxSteps {
			sel.Skipped = append(sel.Skipped, Skip{s.Position, SkipHopLimit})
			continue
		}
		wait := time.Duration(s.WaitSeconds) * time.Second
		if e := elapsed(s.WaitBusiness); e < wait {
			sel.Remaining = wait - e
			sel.WaitFor = &cands[i]
			return sel
		}
		sel.Step = &cands[i]
		return sel
	}
	sel.Exhausted = matched
	return sel
}
