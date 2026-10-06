package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// Definition is the executable graph. The server validates and runs it; the UI only edits it.
type Definition struct {
	SchemaVersion int            `json:"schema_version"`
	Nodes         []Node         `json:"nodes"`
	Edges         []Edge         `json:"edges"`
	Variables     []Variable     `json:"variables"`
	Settings      Settings       `json:"settings"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Node struct {
	ID       string          `json:"id"`
	Type     NodeType        `json:"type"`
	Label    string          `json:"label,omitempty"`
	Position Position        `json:"position"`
	Config   json.RawMessage `json:"config,omitempty"`
}

// Edge connects a named output port of a node to a node. One destination per (source, sourcePort).
type Edge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourcePort string `json:"sourcePort"`
	Target     string `json:"target"`
	TargetPort string `json:"targetPort,omitempty"`
}

type Variable struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // string | number | boolean
	Description string `json:"description,omitempty"`
}

// Settings bound a run. Zero means "use the default".
type Settings struct {
	MaxNodeExecutions   int `json:"max_node_executions,omitempty"`
	InputTimeoutSeconds int `json:"input_timeout_seconds,omitempty"`
}

func (s Settings) EffectiveMaxExecutions() int {
	switch {
	case s.MaxNodeExecutions <= 0:
		return DefaultMaxExecutions
	case s.MaxNodeExecutions > HardMaxExecutions:
		return HardMaxExecutions
	}
	return s.MaxNodeExecutions
}

// DefaultInputTimeout is how long a run waits for the contact when neither the node nor the flow says otherwise.
const DefaultInputTimeoutSeconds = 24 * 3600

func (s Settings) EffectiveInputTimeout(nodeSeconds int) int {
	switch {
	case nodeSeconds > 0:
		return nodeSeconds
	case s.InputTimeoutSeconds > 0:
		return s.InputTimeoutSeconds
	}
	return DefaultInputTimeoutSeconds
}

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	variableName    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	templateRefExpr = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_.]*)\s*\}\}`)
)

// ParseDefinition decodes strictly (unknown fields are an error) and enforces the structural size limits. It does not
// judge the graph: that is Validate.
func ParseDefinition(raw []byte) (*Definition, error) {
	if len(raw) > MaxDefinitionBytes {
		return nil, fmt.Errorf("%w: definition exceeds %d bytes", ErrInvalid, MaxDefinitionBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Definition
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if d.SchemaVersion != 1 {
		return nil, fmt.Errorf("%w: schema_version must be 1", ErrInvalid)
	}
	if len(d.Nodes) > MaxNodes || len(d.Edges) > MaxEdges || len(d.Variables) > MaxVariables {
		return nil, fmt.Errorf("%w: limits exceeded (max %d nodes, %d edges, %d variables)", ErrInvalid, MaxNodes, MaxEdges, MaxVariables)
	}
	for _, n := range d.Nodes {
		if len(n.Config) > MaxNodeConfigBytes {
			return nil, fmt.Errorf("%w: node %q config exceeds %d bytes", ErrInvalid, n.ID, MaxNodeConfigBytes)
		}
	}
	return &d, nil
}

func (d *Definition) NodeByID(id string) *Node {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			return &d.Nodes[i]
		}
	}
	return nil
}

// Next returns the target of (node, port), or "" when the port is not connected.
func (d *Definition) Next(nodeID, port string) string {
	for _, e := range d.Edges {
		if e.Source == nodeID && e.SourcePort == port {
			return e.Target
		}
	}
	return ""
}

func (d *Definition) Trigger() *Node {
	for i := range d.Nodes {
		if d.Nodes[i].Type == NodeTrigger {
			return &d.Nodes[i]
		}
	}
	return nil
}
