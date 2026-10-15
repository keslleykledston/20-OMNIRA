package application

import (
	"fmt"
	"strings"

	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

// The service-session nodes (ADR-0049 phase 6). Each is a thin call to the service-context module through ports.ServiceFlow: the module decides, the node
// only asks. Failing (no module, a closed conversation, an unknown code) takes the wired `error` port, or fails the run so the conversation returns to humans.

func serviceFlow(in StepInput) (ports.ServiceFlow, error) {
	sf, ok := in.Effects.(ports.ServiceFlow)
	if !ok {
		return nil, fmt.Errorf("the service context is not available in this environment")
	}
	return sf, nil
}

// text interpolates the author's variables and trims; the module bounds the size.
func serviceText(in StepInput, s string) string {
	return strings.TrimSpace(domain.Interpolate(s, in.Vars))
}

type startServiceSessionExec struct{}

func (startServiceSessionExec) Type() domain.NodeType { return domain.NodeStartServiceSession }
func (startServiceSessionExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.StartServiceSessionConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	sf, err := serviceFlow(in)
	if err != nil {
		return routeError(in, err)
	}
	protocol, created, err := sf.ServiceStart(in.Ctx, in.Facts.ID, in.RunID, in.Seq)
	if err != nil {
		return routeError(in, err)
	}
	res := StepResult{Port: "next", Output: map[string]any{"protocol": protocol, "created": created}}
	if c.Variable != "" {
		res.SetVars = map[string]any{c.Variable: protocol}
	}
	return res, nil
}

type classifyServiceSessionExec struct{}

func (classifyServiceSessionExec) Type() domain.NodeType { return domain.NodeClassifyServiceSession }
func (classifyServiceSessionExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.ClassifyServiceSessionConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	sf, err := serviceFlow(in)
	if err != nil {
		return routeError(in, err)
	}
	if err := sf.ServiceClassify(in.Ctx, in.Facts.ID, in.RunID, in.Seq, c.Category); err != nil {
		return routeError(in, err)
	}
	return StepResult{Port: "next", Output: map[string]any{"category": c.Category}}, nil
}

type createServiceTaskExec struct{}

func (createServiceTaskExec) Type() domain.NodeType { return domain.NodeCreateServiceTask }
func (createServiceTaskExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.CreateServiceTaskConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	title := serviceText(in, c.Title)
	if title == "" {
		return routeError(in, fmt.Errorf("the task title is empty after substituting variables"))
	}
	sf, err := serviceFlow(in)
	if err != nil {
		return routeError(in, err)
	}
	priority := c.Priority
	if priority == "" {
		priority = "normal"
	}
	if err := sf.ServiceTask(in.Ctx, in.Facts.ID, in.RunID, in.Seq, title, serviceText(in, c.Description), priority); err != nil {
		return routeError(in, err)
	}
	return StepResult{Port: "next", Output: map[string]any{"priority": priority}}, nil
}

type openServiceCaseExec struct{}

func (openServiceCaseExec) Type() domain.NodeType { return domain.NodeOpenServiceCase }
func (openServiceCaseExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.OpenServiceCaseConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	title := serviceText(in, c.Title)
	if title == "" {
		return routeError(in, fmt.Errorf("the case title is empty after substituting variables"))
	}
	sf, err := serviceFlow(in)
	if err != nil {
		return routeError(in, err)
	}
	priority := c.Priority
	if priority == "" {
		priority = "normal"
	}
	number, err := sf.ServiceOpenCase(in.Ctx, in.Facts.ID, in.RunID, in.Seq, title, serviceText(in, c.Description), priority)
	if err != nil {
		return routeError(in, err)
	}
	res := StepResult{Port: "next", Output: map[string]any{"case_number": number, "priority": priority}}
	if c.Variable != "" {
		res.SetVars = map[string]any{c.Variable: number}
	}
	return res, nil
}

type closeServiceSessionExec struct{}

func (closeServiceSessionExec) Type() domain.NodeType { return domain.NodeCloseServiceSession }
func (closeServiceSessionExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.CloseServiceSessionConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	sf, err := serviceFlow(in)
	if err != nil {
		return routeError(in, err)
	}
	closed, err := sf.ServiceClose(in.Ctx, in.Facts.ID, in.RunID, in.Seq, c.Result, serviceText(in, c.Note))
	if err != nil {
		return routeError(in, err)
	}
	return StepResult{Port: "next", Output: map[string]any{"closed": closed, "result": c.Result}}, nil
}

// ServiceExecutors are the nodes that work the service session.
func ServiceExecutors() []Executor {
	return []Executor{startServiceSessionExec{}, classifyServiceSessionExec{}, createServiceTaskExec{}, openServiceCaseExec{}, closeServiceSessionExec{}}
}
