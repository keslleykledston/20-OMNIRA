package application

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

// ADR-0044 routing nodes. They are THIN: each one asks a shared service (schedule, eligibility, sector choice, the routing engine) and maps
// the answer to a port. No rule about hours, shifts, capacity or overflow lives here, so a flow can never disagree with the Inbox.

func routing(in StepInput) (ports.RoutingEffects, error) {
	r, ok := in.Effects.(ports.RoutingEffects)
	if !ok {
		return nil, fmt.Errorf("the routing services are not available in this environment")
	}
	return r, nil
}

// place reads the queue or sector a check node is about; the id is a stable reference, never a name.
func place(in StepInput) (kind string, id uuid.UUID, err error) {
	c, err := domain.DecodeConfig[domain.ScheduleCheckConfig](in.Node.Config)
	if err != nil {
		return "", uuid.Nil, err
	}
	switch {
	case c.Queue != nil && !c.Queue.IsZero():
		kind = "queue"
		if v, ok := c.Queue.UUID(); ok {
			return kind, v, nil
		}
	case c.Sector != nil && !c.Sector.IsZero():
		kind = "sector"
		if v, ok := c.Sector.UUID(); ok {
			return kind, v, nil
		}
	}
	return "", uuid.Nil, fmt.Errorf("the queue or sector reference is not a valid id")
}

type checkScheduleExec struct{}

func (checkScheduleExec) Type() domain.NodeType { return domain.NodeCheckSchedule }
func (checkScheduleExec) Execute(in StepInput) (StepResult, error) {
	kind, id, err := place(in)
	if err != nil {
		return StepResult{}, err
	}
	r, err := routing(in)
	if err != nil {
		return StepResult{}, err
	}
	state, err := r.Schedule(in.Ctx, kind, id)
	if err != nil {
		return StepResult{}, err
	}
	switch state {
	case "open", "closed":
		return StepResult{Port: state, Output: map[string]any{kind: id.String(), "schedule": state}}, nil
	}
	// gone or deactivated: an old flow does not break, it takes the "unavailable" port when the author wired one, otherwise "closed"
	// (the safe reading: do not promise attendance that may not exist)
	if hasEdge(in, "unavailable") {
		return StepResult{Port: "unavailable", Output: map[string]any{kind: id.String(), "schedule": "unavailable"}}, nil
	}
	return StepResult{Port: "closed", Output: map[string]any{kind: id.String(), "schedule": "unavailable"}}, nil
}

type checkAvailabilityExec struct{}

func (checkAvailabilityExec) Type() domain.NodeType { return domain.NodeCheckAvailability }
func (checkAvailabilityExec) Execute(in StepInput) (StepResult, error) {
	kind, id, err := place(in)
	if err != nil {
		return StepResult{}, err
	}
	r, err := routing(in)
	if err != nil {
		return StepResult{}, err
	}
	state, err := r.Availability(in.Ctx, kind, id)
	if err != nil {
		return StepResult{}, err
	}
	port := state
	switch state {
	case "available", "unavailable":
	case "closed":
		if !hasEdge(in, "closed") {
			port = "unavailable"
		}
	default: // "missing": the place is gone
		if !hasEdge(in, "missing") {
			port = "unavailable"
		}
		state = "missing"
	}
	return StepResult{Port: port, Output: map[string]any{kind: id.String(), "availability": state}}, nil
}

type selectSectorExec struct{}

func (selectSectorExec) Type() domain.NodeType { return domain.NodeSelectSector }

func sectorOptionsFromState(v any) []domain.ChoiceOption {
	items, _ := v.([]any)
	out := make([]domain.ChoiceOption, 0, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		id, _ := m["id"].(string)
		name, _ := m["name"].(string)
		if id != "" {
			out = append(out, domain.ChoiceOption{ID: id, Label: name})
		}
	}
	return out
}

func (selectSectorExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.SelectSectorConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	variable := c.Variable
	if variable == "" {
		variable = "sector"
	}
	wait := in.Def.Settings.EffectiveInputTimeout(c.TimeoutSeconds)
	prompt := c.Text
	if strings.TrimSpace(prompt) == "" {
		prompt = "Com qual setor você quer falar?"
	}
	if !in.Resuming {
		r, err := routing(in)
		if err != nil {
			return StepResult{}, err
		}
		sectors, err := r.ActiveSectors(in.Ctx) // read NOW: a new sector appears and a deactivated one disappears without editing the flow
		if err != nil {
			return StepResult{}, err
		}
		if len(sectors) == 0 {
			return StepResult{Port: "none", Output: map[string]any{"sectors": 0}}, nil
		}
		shown := make([]any, len(sectors))
		opts := make([]domain.ChoiceOption, len(sectors))
		for i, s := range sectors {
			shown[i] = map[string]any{"id": s.ID.String(), "name": s.Name}
			opts[i] = domain.ChoiceOption{ID: s.ID.String(), Label: s.Name}
		}
		if port, err := say(in, menuText(domain.Interpolate(prompt, in.Vars), opts)); err != nil {
			return StepResult{}, err
		} else if port != "" {
			return StepResult{Port: port, Output: map[string]any{"sent": false}}, nil
		}
		// the options shown are kept with the run: the answer is matched against what the contact SAW, even if the list changed meanwhile
		return StepResult{Wait: true, WaitSecs: wait, State: map[string]any{"attempts": 0, "options": shown}, Output: map[string]any{"asked": true, "sectors": len(sectors)}}, nil
	}
	if in.TimedOut {
		return StepResult{Port: "timeout", Output: map[string]any{"timeout": true}}, nil
	}
	opts := sectorOptionsFromState(in.State["options"])
	reply := ""
	if in.Message != nil {
		reply = in.Message.Text
	}
	if i := domain.MatchOption(reply, opts); i >= 0 {
		return StepResult{Port: "selected", SetVars: map[string]any{variable: map[string]any{"id": opts[i].ID, "name": opts[i].Label}},
			Output: map[string]any{"selected": opts[i].Label}}, nil
	}
	n := attempts(in.State) + 1
	if n >= maxAttempts(c.MaxAttempts) {
		return StepResult{Port: "timeout", Output: map[string]any{"attempts_exhausted": true}}, nil
	}
	if port, err := say(in, "Não consegui entender. "+menuText(domain.Interpolate(prompt, in.Vars), opts)); err != nil {
		return StepResult{}, err
	} else if port != "" {
		return StepResult{Port: port, Output: map[string]any{"sent": false}}, nil
	}
	return StepResult{Wait: true, WaitSecs: wait, State: map[string]any{"attempts": n, "options": in.State["options"]}, Output: map[string]any{"invalid": true, "attempts": n}}, nil
}

type assignSectorExec struct{}

func (assignSectorExec) Type() domain.NodeType { return domain.NodeAssignSector }
func (assignSectorExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.AssignSectorConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	var sector uuid.UUID
	switch {
	case c.Sector != nil && !c.Sector.IsZero():
		id, ok := c.Sector.UUID()
		if !ok {
			return StepResult{}, fmt.Errorf("the sector reference is not a valid id")
		}
		sector = id
	case c.FromVariable != "":
		m, _ := in.Vars[c.FromVariable].(map[string]any)
		raw, _ := m["id"].(string)
		id, perr := uuid.Parse(raw)
		if perr != nil || id == uuid.Nil {
			if hasEdge(in, "unavailable") { // the contact has not chosen (or the variable is empty): the author decides what happens
				return StepResult{Port: "unavailable", Output: map[string]any{"reason": "no_sector_chosen"}}, nil
			}
			return StepResult{}, fmt.Errorf("the variable %q holds no sector", c.FromVariable)
		}
		sector = id
	default:
		return StepResult{}, fmt.Errorf("no sector configured")
	}
	r, err := routing(in)
	if err != nil {
		return routeError(in, err)
	}
	outcome, err := r.AssignSector(in.Ctx, in.Facts.ID, sector)
	if err != nil {
		return routeError(in, err)
	}
	if outcome != "assigned" {
		if hasEdge(in, "unavailable") {
			return StepResult{Port: "unavailable", Output: map[string]any{"sector": sector.String(), "outcome": outcome}}, nil
		}
		return routeError(in, fmt.Errorf("the sector is not available (%s)", outcome))
	}
	return StepResult{Port: "next", Output: map[string]any{"sector": sector.String()}}, nil
}

type applyOverflowExec struct{}

func (applyOverflowExec) Type() domain.NodeType { return domain.NodeApplyOverflow }
func (applyOverflowExec) Execute(in StepInput) (StepResult, error) {
	r, err := routing(in)
	if err != nil {
		return routeError(in, err)
	}
	outcome, err := r.ApplyOverflow(in.Ctx, in.Facts.ID)
	if err != nil {
		return routeError(in, err)
	}
	port := outcome
	if (outcome == "waiting_open" || outcome == "manual") && !hasEdge(in, outcome) {
		port = "queued" // an author who did not wire the finer outcome gets the generic one
	}
	switch port {
	case "assigned", "queued", "waiting_open", "manual":
		return StepResult{Port: port, Output: map[string]any{"outcome": outcome}}, nil
	}
	return routeError(in, fmt.Errorf("unexpected overflow outcome %q", outcome))
}

// RoutingExecutors are the ADR-0044 nodes.
func RoutingExecutors() []Executor {
	return []Executor{checkScheduleExec{}, checkAvailabilityExec{}, selectSectorExec{}, assignSectorExec{}, applyOverflowExec{}}
}
