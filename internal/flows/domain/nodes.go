package domain

import (
	"fmt"
	"strings"
)

// SideEffect classifies what a node does outside the flow's own variables; the simulator and the retry policy use it.
type SideEffect string

const (
	EffectNone     SideEffect = "none"     // pure: safe to simulate, retry irrelevant
	EffectLocal    SideEffect = "local"    // writes OMNIRA data in the run's transaction (ticket, queue): idempotent per run+seq
	EffectExternal SideEffect = "external" // leaves OMNIRA (a message to the contact): outbox + system idempotency key
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one finding of the validator; Severity=error blocks publishing.
type Issue struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	NodeID   string   `json:"node_id,omitempty"`
	EdgeID   string   `json:"edge_id,omitempty"`
	Message  string   `json:"message"`
}

type Port struct {
	Name     string
	Required bool
}

// NodeAnalysis is everything the validator needs to know about one node, derived from its typed config.
type NodeAnalysis struct {
	Ports   []Port
	Issues  []Issue
	Refs    []ResourceRef
	Reads   []string // variable paths the node reads
	Defines []string // variables the node assigns
}

// NodeSpec describes a node kind. Every spec must have a runtime executor (a test enforces the parity).
type NodeSpec struct {
	Type       NodeType
	Label      string
	Category   string
	SideEffect SideEffect
	Waits      bool // may suspend the run until the contact answers
	Terminal   bool // ends or hands off the run: no outgoing edges
	Analyze    func(n Node) NodeAnalysis
}

var specs = map[NodeType]NodeSpec{}

func register(s NodeSpec) { specs[s.Type] = s }

func SpecFor(t NodeType) (NodeSpec, bool) { s, ok := specs[t]; return s, ok }

// Specs returns the catalog (for the UI node library and for tests).
func Specs() []NodeSpec {
	order := []NodeType{NodeTrigger, NodeSendMessage, NodeAsk, NodeChoice, NodeCondition, NodeSwitch, NodeSetVariable,
		NodeBusinessHours, NodeResolveContact, NodeResolveCustomerCtx, NodeCustomerChoice, NodeFindOpenTickets,
		NodeCreateTicket, NodeAssignQueue, NodeHumanHandoff, NodeSubflow, NodeAIClassify, NodeAIExtract, NodeAISummarize, NodeEnd}
	out := make([]NodeSpec, 0, len(order))
	for _, t := range order {
		if s, ok := specs[t]; ok {
			out = append(out, s)
		}
	}
	return out
}

func errIssue(n Node, code, msg string) Issue {
	return Issue{Severity: SeverityError, Code: code, NodeID: n.ID, Message: msg}
}

func warnIssue(n Node, code, msg string) Issue {
	return Issue{Severity: SeverityWarning, Code: code, NodeID: n.ID, Message: msg}
}

// sendOutcomePorts are the optional outcomes of ANY node that sends text to the contact: the 24h Meta window closed, or no
// usable text channel. Unwired, the run fails cleanly and the conversation returns to humans (see application.say).
var sendOutcomePorts = []Port{{"window_closed", false}, {"error", false}}

func req(names ...string) []Port {
	out := make([]Port, len(names))
	for i, n := range names {
		out[i] = Port{Name: n, Required: true}
	}
	return out
}

// cfg decodes a node config and reports a decoding problem as an issue.
func cfg[T any](n Node, a *NodeAnalysis) (T, bool) {
	c, err := DecodeConfig[T](n.Config)
	if err != nil {
		a.Issues = append(a.Issues, errIssue(n, "invalid_config", fmt.Sprintf("%q (%s): %v", n.ID, n.Type, err)))
		return c, false
	}
	return c, true
}

func checkText(n Node, a *NodeAnalysis, field, text string, required bool) {
	trimmed := strings.TrimSpace(text)
	if required && trimmed == "" {
		a.Issues = append(a.Issues, errIssue(n, "missing_text", fmt.Sprintf("%q: %s is required", n.ID, field)))
	}
	if len([]rune(text)) > MaxMessageRunes {
		a.Issues = append(a.Issues, errIssue(n, "text_too_long", fmt.Sprintf("%q: %s exceeds %d characters", n.ID, field, MaxMessageRunes)))
	}
	for _, m := range templateRefExpr.FindAllStringSubmatch(text, -1) {
		a.Reads = append(a.Reads, m[1])
	}
}

func checkVarName(n Node, a *NodeAnalysis, field, name string) {
	if !variableName.MatchString(name) {
		a.Issues = append(a.Issues, errIssue(n, "invalid_variable", fmt.Sprintf("%q: %s %q must match [a-z][a-z0-9_]* (max 41)", n.ID, field, name)))
		return
	}
	a.Defines = append(a.Defines, name)
}

func checkTimeout(n Node, a *NodeAnalysis, seconds, attempts int) {
	if seconds < 0 || seconds > 30*24*3600 {
		a.Issues = append(a.Issues, errIssue(n, "invalid_timeout", fmt.Sprintf("%q: timeout_seconds must be between 0 and 30 days", n.ID)))
	}
	if attempts < 0 || attempts > 10 {
		a.Issues = append(a.Issues, errIssue(n, "invalid_attempts", fmt.Sprintf("%q: max_attempts must be between 0 and 10", n.ID)))
	}
}

func checkResource(n Node, a *NodeAnalysis, kind string, f ResourceField, required bool) {
	switch {
	case f.IsZero():
		if required {
			a.Issues = append(a.Issues, errIssue(n, "missing_resource", fmt.Sprintf("%q: %s is required", n.ID, kind)))
		}
	case f.Ref != "":
		// Legal only inside a system template; ValidateOptions.AllowPlaceholders gates it in Validate.
		a.Issues = append(a.Issues, Issue{Severity: SeverityError, Code: "placeholder", NodeID: n.ID, Message: fmt.Sprintf("%q: %s is the unresolved template placeholder %q", n.ID, kind, f.Ref)})
	default:
		id, ok := f.UUID()
		if !ok {
			a.Issues = append(a.Issues, errIssue(n, "invalid_resource", fmt.Sprintf("%q: %s %q is not a valid id", n.ID, kind, f.ID)))
			return
		}
		a.Refs = append(a.Refs, ResourceRef{Kind: kind, ID: id, NodeID: n.ID})
	}
}

var conditionOps = map[string]bool{"eq": true, "neq": true, "gt": true, "gte": true, "lt": true, "lte": true, "contains": true, "exists": true, "not_exists": true, "in": true}

func checkOp(n Node, a *NodeAnalysis, op string, value any) {
	if !conditionOps[op] {
		a.Issues = append(a.Issues, errIssue(n, "invalid_operator", fmt.Sprintf("%q: unknown operator %q", n.ID, op)))
		return
	}
	if op != "exists" && op != "not_exists" && value == nil {
		a.Issues = append(a.Issues, errIssue(n, "missing_value", fmt.Sprintf("%q: operator %s needs a value", n.ID, op)))
	}
	if op == "in" {
		if _, ok := value.([]any); !ok {
			a.Issues = append(a.Issues, errIssue(n, "invalid_value", fmt.Sprintf("%q: operator in needs a list", n.ID)))
		}
	}
}

func init() {
	register(NodeSpec{Type: NodeTrigger, Label: "Start", Category: "flow", SideEffect: EffectNone,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("next")}
			cfg[TriggerConfig](n, &a)
			return a
		}})

	register(NodeSpec{Type: NodeSendMessage, Label: "Send message", Category: "conversation", SideEffect: EffectExternal,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: []Port{{"next", true}, {"window_closed", false}, {"error", false}}}
			if c, ok := cfg[SendMessageConfig](n, &a); ok {
				checkText(n, &a, "text", c.Text, true)
			}
			return a
		}})

	register(NodeSpec{Type: NodeAsk, Label: "Ask a question", Category: "conversation", SideEffect: EffectExternal, Waits: true,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: append(req("next", "timeout"), sendOutcomePorts...)}
			if c, ok := cfg[AskConfig](n, &a); ok {
				checkText(n, &a, "text", c.Text, true)
				checkVarName(n, &a, "variable", c.Variable)
				switch c.Validation {
				case "", "none", "number", "email", "phone":
				default:
					a.Issues = append(a.Issues, errIssue(n, "invalid_validation", fmt.Sprintf("%q: validation must be none, number, email or phone", n.ID)))
				}
				checkTimeout(n, &a, c.TimeoutSeconds, c.MaxAttempts)
			}
			return a
		}})

	register(NodeSpec{Type: NodeChoice, Label: "Menu / choice", Category: "conversation", SideEffect: EffectExternal, Waits: true,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{}
			c, ok := cfg[ChoiceConfig](n, &a)
			if !ok {
				a.Ports = req("timeout")
				return a
			}
			checkText(n, &a, "text", c.Text, true)
			checkVarName(n, &a, "variable", c.Variable)
			checkTimeout(n, &a, c.TimeoutSeconds, c.MaxAttempts)
			if len(c.Options) < 2 || len(c.Options) > 10 {
				a.Issues = append(a.Issues, errIssue(n, "invalid_options", fmt.Sprintf("%q: a choice needs 2 to 10 options", n.ID)))
			}
			seen := map[string]bool{}
			for _, o := range c.Options {
				if !idPattern.MatchString(o.ID) || o.ID == "timeout" || o.ID == "other" || o.ID == "window_closed" || o.ID == "error" {
					a.Issues = append(a.Issues, errIssue(n, "invalid_option_id", fmt.Sprintf("%q: option id %q is invalid or reserved", n.ID, o.ID)))
				}
				if seen[o.ID] {
					a.Issues = append(a.Issues, errIssue(n, "duplicate_option", fmt.Sprintf("%q: duplicate option id %q", n.ID, o.ID)))
				}
				seen[o.ID] = true
				if strings.TrimSpace(o.Label) == "" {
					a.Issues = append(a.Issues, errIssue(n, "missing_text", fmt.Sprintf("%q: option %q has no label", n.ID, o.ID)))
				}
				a.Ports = append(a.Ports, Port{Name: o.ID, Required: true})
			}
			a.Ports = append(a.Ports, Port{"timeout", true}, Port{"other", false})
			a.Ports = append(a.Ports, sendOutcomePorts...)
			return a
		}})

	register(NodeSpec{Type: NodeCondition, Label: "Condition", Category: "logic", SideEffect: EffectNone,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("true", "false")}
			if c, ok := cfg[ConditionConfig](n, &a); ok {
				a.Reads = append(a.Reads, c.Variable)
				checkOp(n, &a, c.Op, c.Value)
			}
			return a
		}})

	register(NodeSpec{Type: NodeSwitch, Label: "Switch", Category: "logic", SideEffect: EffectNone,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{}
			c, ok := cfg[SwitchConfig](n, &a)
			if !ok {
				a.Ports = req("default")
				return a
			}
			a.Reads = append(a.Reads, c.Variable)
			if len(c.Cases) < 1 || len(c.Cases) > 20 {
				a.Issues = append(a.Issues, errIssue(n, "invalid_cases", fmt.Sprintf("%q: a switch needs 1 to 20 cases", n.ID)))
			}
			seen := map[string]bool{}
			for _, cs := range c.Cases {
				if !idPattern.MatchString(cs.ID) || cs.ID == "default" {
					a.Issues = append(a.Issues, errIssue(n, "invalid_case_id", fmt.Sprintf("%q: case id %q is invalid or reserved", n.ID, cs.ID)))
				}
				if seen[cs.ID] {
					a.Issues = append(a.Issues, errIssue(n, "duplicate_case", fmt.Sprintf("%q: duplicate case id %q", n.ID, cs.ID)))
				}
				seen[cs.ID] = true
				op := cs.Op
				if op == "" {
					op = "eq"
				}
				checkOp(n, &a, op, cs.Value)
				a.Ports = append(a.Ports, Port{Name: cs.ID, Required: true})
			}
			a.Ports = append(a.Ports, Port{"default", true})
			return a
		}})

	register(NodeSpec{Type: NodeSetVariable, Label: "Set variable", Category: "logic", SideEffect: EffectNone,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("next")}
			if c, ok := cfg[SetVariableConfig](n, &a); ok {
				if len(c.Assignments) < 1 || len(c.Assignments) > 20 {
					a.Issues = append(a.Issues, errIssue(n, "invalid_assignments", fmt.Sprintf("%q: 1 to 20 assignments", n.ID)))
				}
				for _, as := range c.Assignments {
					checkVarName(n, &a, "variable", as.Variable)
					if s, ok := as.Value.(string); ok {
						checkText(n, &a, "value", s, false)
					}
				}
			}
			return a
		}})

	register(NodeSpec{Type: NodeBusinessHours, Label: "Business hours", Category: "logic", SideEffect: EffectNone,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("open", "closed")}
			if c, ok := cfg[BusinessHoursConfig](n, &a); ok {
				if err := ValidateBusinessHours(c); err != nil {
					a.Issues = append(a.Issues, errIssue(n, "invalid_hours", fmt.Sprintf("%q: %v", n.ID, err)))
				}
			}
			return a
		}})

	register(NodeSpec{Type: NodeResolveContact, Label: "Resolve contact", Category: "data", SideEffect: EffectNone,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("known", "unknown")}
			cfg[ResolveContactConfig](n, &a)
			return a
		}})

	register(NodeSpec{Type: NodeResolveCustomerCtx, Label: "Resolve customer context", Category: "data", SideEffect: EffectLocal,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("none", "single", "multiple")}
			cfg[ResolveCustomerContextConfig](n, &a)
			return a
		}})

	register(NodeSpec{Type: NodeCustomerChoice, Label: "Choose company", Category: "conversation", SideEffect: EffectExternal, Waits: true,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: append(req("selected", "timeout"), sendOutcomePorts...)}
			if c, ok := cfg[CustomerChoiceConfig](n, &a); ok {
				checkText(n, &a, "text", c.Text, false)
				checkTimeout(n, &a, c.TimeoutSeconds, c.MaxAttempts)
			}
			a.Reads = append(a.Reads, "customer.candidates_count")
			return a
		}})

	register(NodeSpec{Type: NodeFindOpenTickets, Label: "Find open tickets", Category: "data", SideEffect: EffectNone,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("none", "found")}
			cfg[FindOpenTicketsConfig](n, &a)
			return a
		}})

	register(NodeSpec{Type: NodeCreateTicket, Label: "Open ticket", Category: "action", SideEffect: EffectLocal,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: []Port{{"next", true}, {"error", false}}}
			if c, ok := cfg[CreateTicketConfig](n, &a); ok {
				checkText(n, &a, "subject", c.Subject, true)
				switch c.Priority {
				case "", "low", "medium", "high", "critical":
				default:
					a.Issues = append(a.Issues, errIssue(n, "invalid_priority", fmt.Sprintf("%q: priority must be low, medium, high or critical (severity is a deterministic rule, never free text)", n.ID)))
				}
			}
			return a
		}})

	register(NodeSpec{Type: NodeAssignQueue, Label: "Assign queue", Category: "action", SideEffect: EffectLocal,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: []Port{{"next", true}, {"error", false}}}
			if c, ok := cfg[AssignQueueConfig](n, &a); ok {
				checkResource(n, &a, "queue", c.Queue, true)
			}
			return a
		}})

	register(NodeSpec{Type: NodeHumanHandoff, Label: "Hand off to a human", Category: "action", SideEffect: EffectLocal, Terminal: true,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{}
			if c, ok := cfg[HumanHandoffConfig](n, &a); ok {
				if c.Queue != nil {
					checkResource(n, &a, "queue", *c.Queue, false)
				}
				checkText(n, &a, "summary", c.Summary, false)
			}
			return a
		}})

	register(NodeSpec{Type: NodeSubflow, Label: "Run subflow", Category: "flow", SideEffect: EffectNone,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("next")}
			if c, ok := cfg[SubflowConfig](n, &a); ok {
				if err := ValidateSlug(c.Flow); err != nil {
					a.Issues = append(a.Issues, errIssue(n, "invalid_subflow", fmt.Sprintf("%q: subflow must name a flow slug", n.ID)))
				}
			}
			return a
		}})

	register(NodeSpec{Type: NodeAIClassify, Label: "AI: classify intent", Category: "ai", SideEffect: EffectExternal,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{}
			c, ok := cfg[AIClassifyConfig](n, &a)
			if !ok {
				a.Ports = req("low_confidence", "error")
				return a
			}
			if len(c.Intents) < 2 || len(c.Intents) > 10 {
				a.Issues = append(a.Issues, errIssue(n, "invalid_intents", fmt.Sprintf("%q: classification needs 2 to 10 intents", n.ID)))
			}
			if c.MinConfidence < 0 || c.MinConfidence > 1 {
				a.Issues = append(a.Issues, errIssue(n, "invalid_confidence", fmt.Sprintf("%q: min_confidence must be between 0 and 1", n.ID)))
			}
			seen := map[string]bool{}
			for _, it := range c.Intents {
				if !idPattern.MatchString(it.ID) || it.ID == "low_confidence" || it.ID == "error" {
					a.Issues = append(a.Issues, errIssue(n, "invalid_intent_id", fmt.Sprintf("%q: intent id %q is invalid or reserved", n.ID, it.ID)))
				}
				if seen[it.ID] {
					a.Issues = append(a.Issues, errIssue(n, "duplicate_intent", fmt.Sprintf("%q: duplicate intent id %q", n.ID, it.ID)))
				}
				seen[it.ID] = true
				if strings.TrimSpace(it.Label) == "" {
					a.Issues = append(a.Issues, errIssue(n, "missing_text", fmt.Sprintf("%q: intent %q has no label", n.ID, it.ID)))
				}
				a.Ports = append(a.Ports, Port{Name: it.ID, Required: true})
			}
			a.Ports = append(a.Ports, Port{"low_confidence", true}, Port{"error", true})
			return a
		}})

	register(NodeSpec{Type: NodeAIExtract, Label: "AI: extract data", Category: "ai", SideEffect: EffectExternal,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("next", "error")}
			if c, ok := cfg[AIExtractConfig](n, &a); ok {
				if len(c.Fields) < 1 || len(c.Fields) > 10 {
					a.Issues = append(a.Issues, errIssue(n, "invalid_fields", fmt.Sprintf("%q: 1 to 10 fields", n.ID)))
				}
				for _, f := range c.Fields {
					checkVarName(n, &a, "variable", f.Variable)
					switch f.Type {
					case "string", "number", "boolean", "email", "phone":
					default:
						a.Issues = append(a.Issues, errIssue(n, "invalid_field_type", fmt.Sprintf("%q: field %q type must be string, number, boolean, email or phone", n.ID, f.Variable)))
					}
				}
			}
			return a
		}})

	register(NodeSpec{Type: NodeAISummarize, Label: "AI: summarize conversation", Category: "ai", SideEffect: EffectExternal,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{Ports: req("next", "error")}
			if c, ok := cfg[AISummarizeConfig](n, &a); ok {
				checkVarName(n, &a, "variable", c.Variable)
				if c.MaxMessages < 0 || c.MaxMessages > 30 {
					a.Issues = append(a.Issues, errIssue(n, "invalid_max_messages", fmt.Sprintf("%q: max_messages must be between 1 and 30", n.ID)))
				}
			}
			return a
		}})

	register(NodeSpec{Type: NodeEnd, Label: "End", Category: "flow", SideEffect: EffectNone, Terminal: true,
		Analyze: func(n Node) NodeAnalysis {
			a := NodeAnalysis{}
			if c, ok := cfg[EndConfig](n, &a); ok {
				switch c.Outcome {
				case "", "resolved", "abandoned", "informational":
				default:
					a.Issues = append(a.Issues, errIssue(n, "invalid_outcome", fmt.Sprintf("%q: outcome must be resolved, abandoned or informational", n.ID)))
				}
			}
			return a
		}})
}
