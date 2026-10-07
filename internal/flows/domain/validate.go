package domain

import (
	"encoding/json"
	"fmt"
	"sort"
)

type ValidateOptions struct {
	// AllowPlaceholders lets {"$ref": "queue.x"} through. Only system templates may carry placeholders; a tenant flow
	// with one is not publishable.
	AllowPlaceholders bool
}

func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ExtractRefs lists the tenant resources a definition points at, to be checked against the session tenant.
func ExtractRefs(def *Definition) []ResourceRef {
	var refs []ResourceRef
	for _, n := range def.Nodes {
		if spec, ok := specs[n.Type]; ok {
			refs = append(refs, spec.Analyze(n).Refs...)
		}
	}
	return refs
}

// Validate is the server-side judge of a definition. It is pure: resource existence/tenancy is checked by the
// application layer with ExtractRefs. Errors block publishing; warnings do not.
func Validate(def *Definition, opts ValidateOptions) []Issue {
	var issues []Issue
	add := func(i Issue) { issues = append(issues, i) }

	if def.Settings.MaxNodeExecutions < 0 || def.Settings.MaxNodeExecutions > HardMaxExecutions {
		add(Issue{Severity: SeverityError, Code: "invalid_settings", Message: fmt.Sprintf("max_node_executions must be between 0 and %d", HardMaxExecutions)})
	}
	if def.Settings.InputTimeoutSeconds < 0 || def.Settings.InputTimeoutSeconds > 30*24*3600 {
		add(Issue{Severity: SeverityError, Code: "invalid_settings", Message: "input_timeout_seconds must be between 0 and 30 days"})
	}

	if def.Settings.CustomerExit != nil {
		if err := def.Settings.CustomerExit.Validate(); err != nil {
			add(Issue{Severity: SeverityError, Code: "invalid_settings", Message: "customer_exit: " + err.Error()})
		}
	}

	// Variables declared by the author.
	defined := map[string]bool{}
	for _, v := range def.Variables {
		switch {
		case !variableName.MatchString(v.Name):
			add(Issue{Severity: SeverityError, Code: "invalid_variable", Message: fmt.Sprintf("variable %q must match [a-z][a-z0-9_]*", v.Name)})
		case IsReservedRoot(v.Name):
			add(Issue{Severity: SeverityError, Code: "reserved_variable", Message: fmt.Sprintf("variable %q is a reserved built-in namespace", v.Name)})
		case defined[v.Name]:
			add(Issue{Severity: SeverityError, Code: "duplicate_variable", Message: fmt.Sprintf("variable %q is declared twice", v.Name)})
		}
		switch v.Type {
		case "", "string", "number", "boolean":
		default:
			add(Issue{Severity: SeverityError, Code: "invalid_variable_type", Message: fmt.Sprintf("variable %q: type must be string, number or boolean", v.Name)})
		}
		defined[v.Name] = true
	}

	// Nodes.
	nodeByID := map[string]Node{}
	analysis := map[string]NodeAnalysis{}
	triggers := 0
	for _, n := range def.Nodes {
		if !idPattern.MatchString(n.ID) {
			add(Issue{Severity: SeverityError, Code: "invalid_node_id", NodeID: n.ID, Message: fmt.Sprintf("node id %q must match [A-Za-z][A-Za-z0-9_-]* (max 64)", n.ID)})
			continue
		}
		if _, dup := nodeByID[n.ID]; dup {
			add(Issue{Severity: SeverityError, Code: "duplicate_node", NodeID: n.ID, Message: fmt.Sprintf("duplicate node id %q", n.ID)})
			continue
		}
		spec, ok := specs[n.Type]
		if !ok {
			add(Issue{Severity: SeverityError, Code: "unknown_node_type", NodeID: n.ID, Message: fmt.Sprintf("node %q has unknown type %q", n.ID, n.Type)})
			continue
		}
		nodeByID[n.ID] = n
		a := spec.Analyze(n)
		analysis[n.ID] = a
		for _, i := range a.Issues {
			if i.Code == "placeholder" && opts.AllowPlaceholders {
				continue
			}
			add(i)
		}
		if spec.Type == NodeTrigger {
			triggers++
		}
		// Secrets never live in a flow: reject secret-looking keys/values anywhere in the config.
		if len(n.Config) > 0 {
			var raw any
			if json.Unmarshal(n.Config, &raw) == nil {
				if key, found := findSecret(raw); found {
					add(Issue{Severity: SeverityError, Code: "secret_in_flow", NodeID: n.ID, Message: fmt.Sprintf("node %q: %q looks like a secret; reference a credential by id instead of embedding it", n.ID, key)})
				}
			}
		}
		for _, d := range a.Defines {
			if IsReservedRoot(d) {
				add(Issue{Severity: SeverityError, Code: "reserved_variable", NodeID: n.ID, Message: fmt.Sprintf("node %q assigns %q, a reserved built-in namespace", n.ID, d)})
			}
			defined[d] = true
		}
	}
	switch {
	case triggers == 0:
		add(Issue{Severity: SeverityError, Code: "missing_trigger", Message: "the flow has no start node (trigger)"})
	case triggers > 1:
		add(Issue{Severity: SeverityError, Code: "multiple_triggers", Message: "the flow has more than one start node"})
	}

	// Variable reads must be built-ins or defined somewhere in the flow.
	for _, n := range def.Nodes {
		a, ok := analysis[n.ID]
		if !ok {
			continue
		}
		for _, path := range a.Reads {
			if path == "" || IsBuiltinPath(path) || defined[rootOf(path)] || defined[path] {
				continue
			}
			add(Issue{Severity: SeverityError, Code: "unknown_variable", NodeID: n.ID, Message: fmt.Sprintf("node %q reads %q, which is neither built-in nor set by any node nor declared", n.ID, path)})
		}
	}

	// Edges.
	outgoing := map[string]map[string]string{} // node -> port -> target
	seenEdge := map[string]bool{}
	for _, e := range def.Edges {
		if e.ID != "" {
			if seenEdge[e.ID] {
				add(Issue{Severity: SeverityError, Code: "duplicate_edge", EdgeID: e.ID, Message: fmt.Sprintf("duplicate edge id %q", e.ID)})
			}
			seenEdge[e.ID] = true
		}
		src, okS := nodeByID[e.Source]
		_, okT := nodeByID[e.Target]
		if !okS || !okT {
			add(Issue{Severity: SeverityError, Code: "dangling_edge", EdgeID: e.ID, Message: fmt.Sprintf("edge %q connects %q to %q, and one of them does not exist", e.ID, e.Source, e.Target)})
			continue
		}
		if nodeByID[e.Target].Type == NodeTrigger {
			add(Issue{Severity: SeverityError, Code: "edge_into_trigger", EdgeID: e.ID, Message: "the start node cannot have incoming edges"})
		}
		a := analysis[src.ID]
		validPort := false
		for _, p := range a.Ports {
			if p.Name == e.SourcePort {
				validPort = true
			}
		}
		if !validPort {
			add(Issue{Severity: SeverityError, Code: "invalid_port", NodeID: src.ID, EdgeID: e.ID, Message: fmt.Sprintf("edge %q leaves %q by port %q, which that node does not have", e.ID, src.ID, e.SourcePort)})
			continue
		}
		if outgoing[src.ID] == nil {
			outgoing[src.ID] = map[string]string{}
		}
		if _, dup := outgoing[src.ID][e.SourcePort]; dup {
			add(Issue{Severity: SeverityError, Code: "port_already_connected", NodeID: src.ID, EdgeID: e.ID, Message: fmt.Sprintf("port %q of %q already has a destination", e.SourcePort, src.ID)})
			continue
		}
		outgoing[src.ID][e.SourcePort] = e.Target
	}

	// Required ports must lead somewhere; optional ones warn.
	for _, n := range def.Nodes {
		a, ok := analysis[n.ID]
		if !ok {
			continue
		}
		for _, p := range a.Ports {
			if _, connected := outgoing[n.ID][p.Name]; connected {
				continue
			}
			if p.Required {
				add(Issue{Severity: SeverityError, Code: "unconnected_port", NodeID: n.ID, Message: fmt.Sprintf("port %q of %q is not connected (every outcome needs a destination, including fallbacks)", p.Name, n.ID)})
			} else {
				add(Issue{Severity: SeverityWarning, Code: "unconnected_optional_port", NodeID: n.ID, Message: fmt.Sprintf("optional port %q of %q is not connected: if that outcome happens the run fails", p.Name, n.ID)})
			}
		}
	}

	// Reachability (orphans are warnings) and cycles (errors: a flow must always terminate).
	if trig := def.Trigger(); trig != nil && triggers == 1 {
		reach := map[string]bool{}
		var visit func(id string)
		visit = func(id string) {
			if reach[id] {
				return
			}
			reach[id] = true
			for _, to := range outgoing[id] {
				visit(to)
			}
		}
		visit(trig.ID)
		for _, n := range def.Nodes {
			if _, ok := nodeByID[n.ID]; ok && !reach[n.ID] {
				add(Issue{Severity: SeverityWarning, Code: "unreachable_node", NodeID: n.ID, Message: fmt.Sprintf("node %q cannot be reached from the start", n.ID)})
			}
		}
		if cyc := findCycle(trig.ID, outgoing); cyc != "" {
			add(Issue{Severity: SeverityError, Code: "cycle", NodeID: cyc, Message: fmt.Sprintf("the flow loops back through %q; loops are not supported (every path must end at an End or a human handoff)", cyc)})
		}
	}

	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Severity == SeverityError && issues[j].Severity != SeverityError })
	return issues
}

func rootOf(path string) string {
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			return path[:i]
		}
	}
	return path
}

// findCycle returns a node on a cycle reachable from start, or "".
func findCycle(start string, outgoing map[string]map[string]string) string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var found string
	var dfs func(id string) bool
	dfs = func(id string) bool {
		color[id] = grey
		targets := make([]string, 0, len(outgoing[id]))
		for _, to := range outgoing[id] {
			targets = append(targets, to)
		}
		sort.Strings(targets)
		for _, to := range targets {
			switch color[to] {
			case grey:
				found = to
				return true
			case white:
				if dfs(to) {
					return true
				}
			}
		}
		color[id] = black
		return false
	}
	dfs(start)
	return found
}

func findSecret(v any) (string, bool) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if LooksLikeSecretKey(k) {
				return k, true
			}
			if key, ok := findSecret(x[k]); ok {
				return key, true
			}
		}
	case []any:
		for _, item := range x {
			if key, ok := findSecret(item); ok {
				return key, true
			}
		}
	case string:
		if LooksLikeSecretValue(x) {
			return "a string value", true
		}
	}
	return "", false
}
