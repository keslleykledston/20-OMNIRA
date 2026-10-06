package application

import (
	"fmt"
	"strings"

	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

const defaultMaxAttempts = 3

func sendKey(in StepInput) string { return fmt.Sprintf("flow:%s:%d", in.RunID, in.Seq) }

// hasEdge tells whether an OPTIONAL output port was wired by the author.
func hasEdge(in StepInput, port string) bool { return in.Def.Next(in.Node.ID, port) != "" }

// say sends text to the contact. A closed 24h window or a conversation without a text channel is an expected outcome the
// author may route; without a destination for it the run fails cleanly (and the conversation returns to humans).
func say(in StepInput, text string) (port string, err error) {
	status, err := in.Effects.SendText(in.Ctx, in.Facts.ID, text, sendKey(in))
	if err != nil {
		return "", err
	}
	switch status {
	case ports.SendQueued, ports.SendReplayed:
		return "", nil
	case ports.SendWindowClosed:
		if hasEdge(in, "window_closed") {
			return "window_closed", nil
		}
		return "", fmt.Errorf("the 24h customer-service window is closed and node %q has no window_closed destination", in.Node.ID)
	default:
		if hasEdge(in, "error") {
			return "error", nil
		}
		return "", fmt.Errorf("the conversation has no usable text channel")
	}
}

func attempts(state map[string]any) int {
	if n, ok := state["attempts"].(float64); ok {
		return int(n)
	}
	if n, ok := state["attempts"].(int); ok {
		return n
	}
	return 0
}

func maxAttempts(configured int) int {
	if configured > 0 {
		return configured
	}
	return defaultMaxAttempts
}

// ---- pure nodes -------------------------------------------------------------------------------------------------

type triggerExec struct{}

func (triggerExec) Type() domain.NodeType { return domain.NodeTrigger }
func (triggerExec) Execute(StepInput) (StepResult, error) {
	return StepResult{Port: "next"}, nil
}

type endExec struct{}

func (endExec) Type() domain.NodeType { return domain.NodeEnd }
func (endExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.EndConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	return StepResult{End: true, Output: map[string]any{"outcome": c.Outcome}}, nil
}

type conditionExec struct{}

func (conditionExec) Type() domain.NodeType { return domain.NodeCondition }
func (conditionExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.ConditionConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	left, exists := domain.Lookup(in.Vars, c.Variable)
	if ok := domain.EvalOp(c.Op, left, c.Value, exists); ok {
		return StepResult{Port: "true", Output: map[string]any{"result": true}}, nil
	}
	return StepResult{Port: "false", Output: map[string]any{"result": false}}, nil
}

type switchExec struct{}

func (switchExec) Type() domain.NodeType { return domain.NodeSwitch }
func (switchExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.SwitchConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	left, exists := domain.Lookup(in.Vars, c.Variable)
	for _, cs := range c.Cases {
		op := cs.Op
		if op == "" {
			op = "eq"
		}
		if domain.EvalOp(op, left, cs.Value, exists) {
			return StepResult{Port: cs.ID, Output: map[string]any{"case": cs.ID}}, nil
		}
	}
	return StepResult{Port: "default", Output: map[string]any{"case": "default"}}, nil
}

type setVariableExec struct{}

func (setVariableExec) Type() domain.NodeType { return domain.NodeSetVariable }
func (setVariableExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.SetVariableConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	set := map[string]any{}
	for _, a := range c.Assignments {
		if s, ok := a.Value.(string); ok {
			set[a.Variable] = domain.Interpolate(s, in.Vars)
		} else {
			set[a.Variable] = a.Value
		}
	}
	return StepResult{Port: "next", SetVars: set, Output: map[string]any{"set": len(set)}}, nil
}

type businessHoursExec struct{}

func (businessHoursExec) Type() domain.NodeType { return domain.NodeBusinessHours }
func (businessHoursExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.BusinessHoursConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	open, err := domain.IsOpen(c, in.Now)
	if err != nil {
		return StepResult{}, err
	}
	if open {
		return StepResult{Port: "open", Output: map[string]any{"open": true}}, nil
	}
	return StepResult{Port: "closed", Output: map[string]any{"open": false}}, nil
}

type subflowExec struct{}

func (subflowExec) Type() domain.NodeType { return domain.NodeSubflow }
func (subflowExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.SubflowConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	id, ok := in.Subflows[c.Flow]
	if !ok {
		return StepResult{}, fmt.Errorf("subflow %q was not pinned when this version was published", c.Flow)
	}
	return StepResult{Call: &SubflowCall{VersionID: id}, Output: map[string]any{"subflow": c.Flow}}, nil
}

// ---- conversation nodes ------------------------------------------------------------------------------------------

type sendMessageExec struct{}

func (sendMessageExec) Type() domain.NodeType { return domain.NodeSendMessage }
func (sendMessageExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.SendMessageConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	port, err := say(in, domain.Interpolate(c.Text, in.Vars))
	if err != nil {
		return StepResult{}, err
	}
	if port == "" {
		port = "next"
	}
	return StepResult{Port: port, Output: map[string]any{"sent": port == "next"}}, nil
}

type askExec struct{}

func (askExec) Type() domain.NodeType { return domain.NodeAsk }
func (askExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.AskConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	wait := in.Def.Settings.EffectiveInputTimeout(c.TimeoutSeconds)
	prompt := domain.Interpolate(c.Text, in.Vars)
	if !in.Resuming {
		if _, err := say(in, prompt); err != nil {
			return StepResult{}, err
		}
		return StepResult{Wait: true, WaitSecs: wait, State: map[string]any{"attempts": 0}, Output: map[string]any{"asked": true}}, nil
	}
	if in.TimedOut {
		return StepResult{Port: "timeout", Output: map[string]any{"timeout": true}}, nil
	}
	answer := ""
	if in.Message != nil {
		answer = in.Message.Text
	}
	if v, ok := domain.NormalizeAnswer(c.Validation, answer); ok {
		return StepResult{Port: "next", SetVars: map[string]any{c.Variable: v}, Output: map[string]any{"answered": true}}, nil
	}
	n := attempts(in.State) + 1
	if n >= maxAttempts(c.MaxAttempts) {
		return StepResult{Port: "timeout", Output: map[string]any{"attempts_exhausted": true}}, nil
	}
	if _, err := say(in, "Não consegui entender. "+prompt); err != nil {
		return StepResult{}, err
	}
	return StepResult{Wait: true, WaitSecs: wait, State: map[string]any{"attempts": n}, Output: map[string]any{"invalid": true, "attempts": n}}, nil
}

type choiceExec struct{}

func (choiceExec) Type() domain.NodeType { return domain.NodeChoice }

func menuText(text string, opts []domain.ChoiceOption) string {
	var b strings.Builder
	b.WriteString(text)
	for i, o := range opts {
		fmt.Fprintf(&b, "\n%d) %s", i+1, o.Label)
	}
	return b.String()
}

func (choiceExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.ChoiceConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	wait := in.Def.Settings.EffectiveInputTimeout(c.TimeoutSeconds)
	menu := menuText(domain.Interpolate(c.Text, in.Vars), c.Options)
	if !in.Resuming {
		// Every channel gets the numbered text menu: interactive buttons/lists are a capability layer for later; the
		// text form works on WAHA and Meta alike and is what the contact's reply is matched against.
		if _, err := say(in, menu); err != nil {
			return StepResult{}, err
		}
		return StepResult{Wait: true, WaitSecs: wait, State: map[string]any{"attempts": 0}, Output: map[string]any{"asked": true}}, nil
	}
	if in.TimedOut {
		return StepResult{Port: "timeout", Output: map[string]any{"timeout": true}}, nil
	}
	reply := ""
	if in.Message != nil {
		reply = in.Message.Text
	}
	if i := domain.MatchOption(reply, c.Options); i >= 0 {
		o := c.Options[i]
		val := o.Value
		if val == "" {
			val = o.Label
		}
		return StepResult{Port: o.ID, SetVars: map[string]any{c.Variable: val}, Output: map[string]any{"option": o.ID}}, nil
	}
	if hasEdge(in, "other") {
		return StepResult{Port: "other", SetVars: map[string]any{c.Variable: strings.TrimSpace(reply)}, Output: map[string]any{"option": "other"}}, nil
	}
	n := attempts(in.State) + 1
	if n >= maxAttempts(c.MaxAttempts) {
		return StepResult{Port: "timeout", Output: map[string]any{"attempts_exhausted": true}}, nil
	}
	if _, err := say(in, "Não consegui entender. "+menu); err != nil {
		return StepResult{}, err
	}
	return StepResult{Wait: true, WaitSecs: wait, State: map[string]any{"attempts": n}, Output: map[string]any{"invalid": true, "attempts": n}}, nil
}

// DefaultExecutors are the nodes that need no data/action side effects; the data and action nodes are in executors_data.go.
func PureExecutors() []Executor {
	return []Executor{triggerExec{}, endExec{}, conditionExec{}, switchExec{}, setVariableExec{}, businessHoursExec{}, subflowExec{}, sendMessageExec{}, askExec{}, choiceExec{}}
}
