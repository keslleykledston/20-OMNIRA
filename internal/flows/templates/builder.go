// Package templates is the system library of Flow Templates and Template Packs (ADR-0019). Definitions are code: typed,
// compiled, reviewed in diffs and deterministic. A published (slug, version) is IMMUTABLE: a golden hash file makes the test
// suite fail when its content changes without a version bump. Nothing here touches a database; installing a template clones
// it into a tenant-owned DRAFT (see application.TemplateService) and never links back to it.
package templates

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/omnira/omnira/internal/flows/domain"
)

// Builder assembles a flow definition. Add* connects the new node from the cursor (the previous node's "next" port); nodes
// with several outputs, and terminal nodes, clear the cursor so the author wires each branch with From(...) or Connect(...).
type Builder struct {
	nodes     []domain.Node
	edges     []domain.Edge
	cursorID  string
	cursorOut string
	settings  domain.Settings
	vars      []domain.Variable
	err       error
}

func NewBuilder() *Builder { return &Builder{} }

func (b *Builder) fail(format string, a ...any) *Builder {
	if b.err == nil {
		b.err = fmt.Errorf(format, a...)
	}
	return b
}

func (b *Builder) cfg(c any) json.RawMessage {
	if c == nil {
		return nil
	}
	raw, err := json.Marshal(c)
	if err != nil {
		b.fail("marshal config: %v", err)
	}
	return raw
}

func (b *Builder) has(id string) bool {
	for _, n := range b.nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// add appends a node and wires it from the cursor. branching/terminal nodes leave the cursor empty.
func (b *Builder) add(id string, t domain.NodeType, c any, continues bool) *Builder {
	if b.has(id) {
		return b.fail("duplicate node id %q", id)
	}
	b.nodes = append(b.nodes, domain.Node{ID: id, Type: t, Config: b.cfg(c)})
	if b.cursorID != "" {
		b.Connect(b.cursorID, b.cursorOut, id)
	}
	if continues {
		b.cursorID, b.cursorOut = id, "next"
	} else {
		b.cursorID, b.cursorOut = "", ""
	}
	return b
}

// From moves the cursor: the next Add is wired from (node, port).
func (b *Builder) From(id, port string) *Builder {
	if !b.has(id) {
		return b.fail("From: unknown node %q", id)
	}
	b.cursorID, b.cursorOut = id, port
	return b
}

// Connect wires an explicit edge (merges: several branches into one node).
func (b *Builder) Connect(src, port, dst string) *Builder {
	b.edges = append(b.edges, domain.Edge{ID: fmt.Sprintf("e%d", len(b.edges)+1), Source: src, SourcePort: port, Target: dst})
	if b.cursorID == src && b.cursorOut == port {
		b.cursorID, b.cursorOut = "", "" // the cursor's own exit now has its destination: the next Add must not wire it again
	}
	return b
}

func (b *Builder) Settings(s domain.Settings) *Builder { b.settings = s; return b }

func (b *Builder) Var(name, typ, description string) *Builder {
	b.vars = append(b.vars, domain.Variable{Name: name, Type: typ, Description: description})
	return b
}

// ---- node constructors ----------------------------------------------------------------------------------------

func (b *Builder) Start() *Builder { return b.add("start", domain.NodeTrigger, nil, true) }
func (b *Builder) Say(id, text string) *Builder {
	return b.add(id, domain.NodeSendMessage, domain.SendMessageConfig{Text: text}, true)
}

// Ask continues on "next"; its "timeout" port must be wired by the author (a template never leaves a wait without fallback).
func (b *Builder) Ask(id, text, variable string) *Builder {
	return b.add(id, domain.NodeAsk, domain.AskConfig{Text: text, Variable: variable}, true)
}
func (b *Builder) AskAs(id, text, variable, validation string) *Builder {
	return b.add(id, domain.NodeAsk, domain.AskConfig{Text: text, Variable: variable, Validation: validation}, true)
}

type Opt struct{ ID, Label, Value string }

func (b *Builder) Choice(id, text, variable string, opts ...Opt) *Builder {
	o := make([]domain.ChoiceOption, len(opts))
	for i, x := range opts {
		o[i] = domain.ChoiceOption{ID: x.ID, Label: x.Label, Value: x.Value}
	}
	return b.add(id, domain.NodeChoice, domain.ChoiceConfig{Text: text, Variable: variable, Options: o}, false)
}
func (b *Builder) Condition(id, variable, op string, value any) *Builder {
	return b.add(id, domain.NodeCondition, domain.ConditionConfig{Variable: variable, Op: op, Value: value}, false)
}

type Case struct {
	ID, Op string
	Value  any
}

func (b *Builder) Switch(id, variable string, cases ...Case) *Builder {
	c := make([]domain.SwitchCase, len(cases))
	for i, x := range cases {
		c[i] = domain.SwitchCase{ID: x.ID, Op: x.Op, Value: x.Value}
	}
	return b.add(id, domain.NodeSwitch, domain.SwitchConfig{Variable: variable, Cases: c}, false)
}
func (b *Builder) Set(id, variable string, value any) *Builder {
	return b.add(id, domain.NodeSetVariable, domain.SetVariableConfig{Assignments: []domain.Assignment{{Variable: variable, Value: value}}}, true)
}
func (b *Builder) Hours(id string, c domain.BusinessHoursConfig) *Builder {
	return b.add(id, domain.NodeBusinessHours, c, false)
}
func (b *Builder) Contact(id string) *Builder {
	return b.add(id, domain.NodeResolveContact, nil, false)
}
func (b *Builder) Company(id string) *Builder {
	return b.add(id, domain.NodeResolveCustomerCtx, nil, false)
}
func (b *Builder) CompanyChoice(id, text string) *Builder {
	return b.add(id, domain.NodeCustomerChoice, domain.CustomerChoiceConfig{Text: text}, false)
}
func (b *Builder) FindTickets(id string) *Builder {
	return b.add(id, domain.NodeFindOpenTickets, nil, false)
}
func (b *Builder) Ticket(id, subject, priority string) *Builder {
	return b.add(id, domain.NodeCreateTicket, domain.CreateTicketConfig{Subject: subject, Priority: priority}, true)
}
func (b *Builder) Queue(id, ref string) *Builder {
	return b.add(id, domain.NodeAssignQueue, domain.AssignQueueConfig{Queue: domain.ResourceField{Ref: ref}}, true)
}

// Handoff ends the flow handing the conversation to humans (queue placeholder optional).
func (b *Builder) Handoff(id, queueRef, summary string) *Builder {
	c := domain.HumanHandoffConfig{Summary: summary}
	if queueRef != "" {
		c.Queue = &domain.ResourceField{Ref: queueRef}
	}
	return b.add(id, domain.NodeHumanHandoff, c, false)
}
func (b *Builder) Sub(id, slug string) *Builder {
	return b.add(id, domain.NodeSubflow, domain.SubflowConfig{Flow: slug}, true)
}
func (b *Builder) End(id, outcome string) *Builder {
	return b.add(id, domain.NodeEnd, domain.EndConfig{Outcome: outcome}, false)
}

// Build lays the graph out (left to right by depth) and serialises it exactly as the runtime reads it.
func (b *Builder) Build() (json.RawMessage, error) {
	if b.err != nil {
		return nil, b.err
	}
	depth := map[string]int{}
	out := map[string][]string{}
	for _, e := range b.edges {
		out[e.Source] = append(out[e.Source], e.Target)
	}
	if len(b.nodes) > 0 {
		queue := []string{b.nodes[0].ID}
		depth[b.nodes[0].ID] = 0
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, nx := range out[cur] {
				if d, seen := depth[nx]; !seen || depth[cur]+1 > d {
					if !seen {
						queue = append(queue, nx)
					}
					depth[nx] = depth[cur] + 1
				}
			}
		}
	}
	rows := map[int]int{}
	nodes := make([]domain.Node, len(b.nodes))
	copy(nodes, b.nodes)
	for i := range nodes {
		d := depth[nodes[i].ID]
		nodes[i].Position = domain.Position{X: float64(d * 260), Y: float64(rows[d] * 130)}
		rows[d]++
	}
	vars := append([]domain.Variable(nil), b.vars...)
	sort.SliceStable(vars, func(i, j int) bool { return vars[i].Name < vars[j].Name })
	def := domain.Definition{SchemaVersion: 1, Nodes: nodes, Edges: b.edges, Variables: vars, Settings: b.settings, Metadata: map[string]any{}}
	if def.Variables == nil {
		def.Variables = []domain.Variable{}
	}
	return json.Marshal(def)
}

// Step is one element of a linear question sequence (see Steps).
type Step interface{ stepID() string }

type AskS struct{ ID, Text, Var, Validation string }
type ChoiceS struct {
	ID, Text, Var string
	Opts          []Opt
}
type SayS struct{ ID, Text string }

func (a AskS) stepID() string    { return a.ID }
func (c ChoiceS) stepID() string { return c.ID }
func (s SayS) stepID() string    { return s.ID }

// Steps adds a linear sequence: every step flows into the next one (a choice sends ALL its options on), every wait's timeout
// goes to timeoutTo (so no wait is ever left without a fallback), and the last step flows into `then`, a node the author adds
// next (it may not exist yet; the registry's validator proves every edge resolves).
func (b *Builder) Steps(then, timeoutTo string, steps ...Step) *Builder {
	for i, st := range steps {
		next := then
		if i+1 < len(steps) {
			next = steps[i+1].stepID()
		}
		switch x := st.(type) {
		case AskS:
			b.nodes = append(b.nodes, domain.Node{ID: x.ID, Type: domain.NodeAsk, Config: b.cfg(domain.AskConfig{Text: x.Text, Variable: x.Var, Validation: x.Validation})})
			b.Connect(x.ID, "next", next).Connect(x.ID, "timeout", timeoutTo)
		case ChoiceS:
			o := make([]domain.ChoiceOption, len(x.Opts))
			for j, op := range x.Opts {
				o[j] = domain.ChoiceOption{ID: op.ID, Label: op.Label, Value: op.Value}
				b.Connect(x.ID, op.ID, next)
			}
			b.nodes = append(b.nodes, domain.Node{ID: x.ID, Type: domain.NodeChoice, Config: b.cfg(domain.ChoiceConfig{Text: x.Text, Variable: x.Var, Options: o})})
			b.Connect(x.ID, "timeout", timeoutTo)
		case SayS:
			b.nodes = append(b.nodes, domain.Node{ID: x.ID, Type: domain.NodeSendMessage, Config: b.cfg(domain.SendMessageConfig{Text: x.Text})})
			b.Connect(x.ID, "next", next)
		}
	}
	if len(steps) > 0 {
		first := steps[0].stepID()
		if b.cursorID != "" {
			b.Connect(b.cursorID, b.cursorOut, first)
		}
	}
	b.cursorID, b.cursorOut = "", ""
	return b
}

// Yes/No and similar frequent option sets.
func YesNo(yes, no string) []Opt { return []Opt{{"sim", yes, "sim"}, {"nao", no, "nao"}} }
