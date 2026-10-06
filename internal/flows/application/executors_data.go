package application

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

func candidatesVar(cands []ports.CustomerCandidate) []any {
	out := make([]any, len(cands))
	for i, c := range cands {
		out[i] = map[string]any{"id": c.AccountID.String(), "name": c.Name}
	}
	return out
}

func customerVar(accountID *uuid.UUID, name string, count int) map[string]any {
	m := map[string]any{"candidates_count": float64(count), "name": name}
	if accountID != nil {
		m["account_id"] = accountID.String()
	}
	return m
}

// routeError turns a failed effect into the node's optional error port when the author wired one; otherwise the error
// fails the run (and the conversation returns to humans). There is no blind retry: effects are idempotent per run+step.
func routeError(in StepInput, err error) (StepResult, error) {
	if hasEdge(in, "error") {
		return StepResult{Port: "error", Output: map[string]any{"error": truncate(err.Error(), 300)}}, nil
	}
	return StepResult{}, err
}

type resolveContactExec struct{}

func (resolveContactExec) Type() domain.NodeType { return domain.NodeResolveContact }
func (resolveContactExec) Execute(in StepInput) (StepResult, error) {
	if in.Facts.ContactID == nil {
		return StepResult{}, fmt.Errorf("the conversation has no contact")
	}
	// Unknown is not "new customer": an unclassified contact stays pending classification and no company is created.
	if k := in.Facts.ContactKind; k == "" || k == "unclassified" {
		return StepResult{Port: "unknown", Output: map[string]any{"kind": "unclassified"}}, nil
	}
	return StepResult{Port: "known", Output: map[string]any{"kind": in.Facts.ContactKind}}, nil
}

type resolveCustomerExec struct{}

func (resolveCustomerExec) Type() domain.NodeType { return domain.NodeResolveCustomerCtx }
func (resolveCustomerExec) Execute(in StepInput) (StepResult, error) {
	if in.Facts.ContactID == nil {
		return StepResult{}, fmt.Errorf("the conversation has no contact")
	}
	cands, err := in.Effects.CustomerCandidates(in.Ctx, *in.Facts.ContactID)
	if err != nil {
		return StepResult{}, err
	}
	// A company already confirmed for this conversation (and still linked) is kept: never re-asked, never replaced.
	if active := in.Facts.ActiveCustomerAccountID; active != nil {
		for _, c := range cands {
			if c.AccountID == *active {
				id := c.AccountID
				return StepResult{Port: "single", ActiveCustomer: &id, Reserved: map[string]any{"customer": customerVar(&id, c.Name, len(cands))},
					Output: map[string]any{"confirmed": true, "candidates": len(cands)}}, nil
			}
		}
	}
	switch len(cands) {
	case 0:
		return StepResult{Port: "none", Reserved: map[string]any{"customer": customerVar(nil, "", 0)}, Output: map[string]any{"candidates": 0}}, nil
	case 1:
		c := cands[0]
		if err := in.Effects.ValidateCustomer(in.Ctx, in.Facts.ID, c.AccountID); err != nil {
			return StepResult{}, err
		}
		id := c.AccountID
		return StepResult{Port: "single", ActiveCustomer: &id, Reserved: map[string]any{"customer": customerVar(&id, c.Name, 1)}, Output: map[string]any{"candidates": 1}}, nil
	}
	// Several companies: NEVER pick one (not the first, not the primary). The contact chooses in customer_choice.
	return StepResult{Port: "multiple", Reserved: map[string]any{"customer": customerVar(nil, "", len(cands)), "_candidates": candidatesVar(cands)},
		Output: map[string]any{"candidates": len(cands)}}, nil
}

type customerChoiceExec struct{}

func (customerChoiceExec) Type() domain.NodeType { return domain.NodeCustomerChoice }

func candidateOptions(v any) []domain.ChoiceOption {
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

func (customerChoiceExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.CustomerChoiceConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	opts := candidateOptions(in.Vars["_candidates"])
	if len(opts) < 2 {
		return StepResult{}, fmt.Errorf("customer_choice needs the candidates found by resolve_customer_context")
	}
	prompt := c.Text
	if strings.TrimSpace(prompt) == "" {
		prompt = "Sobre qual empresa é este atendimento?"
	}
	menu := menuText(domain.Interpolate(prompt, in.Vars), opts)
	wait := in.Def.Settings.EffectiveInputTimeout(c.TimeoutSeconds)
	if !in.Resuming {
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
	if i := domain.MatchOption(reply, opts); i >= 0 {
		id, perr := uuid.Parse(opts[i].ID)
		if perr != nil {
			return StepResult{}, perr
		}
		if err := in.Effects.ValidateCustomer(in.Ctx, in.Facts.ID, id); err != nil {
			return StepResult{}, err
		}
		return StepResult{Port: "selected", ActiveCustomer: &id, Reserved: map[string]any{"customer": customerVar(&id, opts[i].Label, len(opts)), "_candidates": nil},
			Output: map[string]any{"selected": opts[i].Label}}, nil
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

type findOpenTicketsExec struct{}

func (findOpenTicketsExec) Type() domain.NodeType { return domain.NodeFindOpenTickets }
func (findOpenTicketsExec) Execute(in StepInput) (StepResult, error) {
	sum, err := in.Effects.OpenTickets(in.Ctx, in.Facts)
	if err != nil {
		return StepResult{}, err
	}
	res := StepResult{Reserved: map[string]any{"tickets": map[string]any{"count": float64(sum.Count), "first_id": sum.FirstID, "first_subject": sum.FirstSubject}},
		Output: map[string]any{"count": sum.Count}}
	if sum.Count == 0 {
		res.Port = "none"
	} else {
		res.Port = "found"
	}
	return res, nil
}

type createTicketExec struct{}

func (createTicketExec) Type() domain.NodeType { return domain.NodeCreateTicket }
func (createTicketExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.CreateTicketConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	priority := c.Priority
	if priority == "" {
		priority = "medium"
	}
	subject := strings.TrimSpace(domain.Interpolate(c.Subject, in.Vars))
	if subject == "" {
		return StepResult{}, fmt.Errorf("the ticket subject is empty after substituting variables")
	}
	if len([]rune(subject)) > 200 {
		subject = string([]rune(subject)[:200])
	}
	id, created, err := in.Effects.EnsureTicket(in.Ctx, in.Facts.ID, subject, priority, in.Facts.ActiveCustomerAccountID)
	if err != nil {
		return routeError(in, err)
	}
	return StepResult{Port: "next", Reserved: map[string]any{"tickets": map[string]any{"count": float64(1), "first_id": id.String(), "first_subject": subject}},
		Output: map[string]any{"ticket_id": id.String(), "created": created, "priority": priority}}, nil
}

type assignQueueExec struct{}

func (assignQueueExec) Type() domain.NodeType { return domain.NodeAssignQueue }
func (assignQueueExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.AssignQueueConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	qid, ok := c.Queue.UUID()
	if !ok {
		return StepResult{}, fmt.Errorf("the queue reference is not a valid id")
	}
	if err := in.Effects.AssignQueue(in.Ctx, in.Facts.ID, &qid); err != nil {
		return routeError(in, err)
	}
	return StepResult{Port: "next", Output: map[string]any{"queue_id": qid.String()}}, nil
}

type humanHandoffExec struct{}

func (humanHandoffExec) Type() domain.NodeType { return domain.NodeHumanHandoff }
func (humanHandoffExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.HumanHandoffConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	var queue *uuid.UUID
	if c.Queue != nil && !c.Queue.IsZero() {
		id, ok := c.Queue.UUID()
		if !ok {
			return StepResult{}, fmt.Errorf("the queue reference is not a valid id")
		}
		queue = &id
	}
	if err := in.Effects.Handoff(in.Ctx, in.Facts.ID, queue); err != nil {
		return StepResult{}, err
	}
	// Context for the operator: what the bot collected and why it escalated. It lives on the run (visible with
	// flow_run.view); it never contains secrets, and answers are redacted like every other record.
	summary := strings.TrimSpace(domain.Interpolate(c.Summary, in.Vars))
	handoff := map[string]any{"summary": summary, "at": in.Now.Format(time.RFC3339)}
	if queue != nil {
		handoff["queue_id"] = queue.String()
	}
	return StepResult{Handoff: true, Reserved: map[string]any{"_handoff": handoff}, Output: map[string]any{"summary": summary}}, nil
}

// DataExecutors are the nodes that read or change OMNIRA data through Effects.
func DataExecutors() []Executor {
	return []Executor{resolveContactExec{}, resolveCustomerExec{}, customerChoiceExec{}, findOpenTicketsExec{}, createTicketExec{}, assignQueueExec{}, humanHandoffExec{}}
}

// AllExecutors is the full catalog without an AI gateway (AI nodes take their error port); a test enforces parity with domain.Specs().
func AllExecutors() []Executor { return AllExecutorsWith(nil) }
