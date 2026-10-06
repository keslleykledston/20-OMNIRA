package application

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/omnira/omnira/internal/flows/domain"
)

// Metrics is what the engine and the sweeper report. Labels are bounded (event, node type, status): never a tenant, flow or
// conversation id, so cardinality stays flat and nothing identifying leaves through /metrics.
type Metrics interface {
	Run(event string)
	Node(nodeType domain.NodeType, status domain.NodeExecStatus, d time.Duration)
	Sweep(kind string, n int)
}

type noMetrics struct{}

func (noMetrics) Run(string)                                                 {}
func (noMetrics) Node(domain.NodeType, domain.NodeExecStatus, time.Duration) {}
func (noMetrics) Sweep(string, int)                                          {}

// Counters renders the flow runtime metrics as Prometheus text (no new dependency), like the Conversation Intelligence ones.
type Counters struct {
	mu       sync.Mutex
	runs     map[string]uint64
	nodes    map[[2]string]uint64
	sweeps   map[string]uint64
	durN     uint64
	durSumMs float64
}

func NewCounters() *Counters {
	return &Counters{runs: map[string]uint64{}, nodes: map[[2]string]uint64{}, sweeps: map[string]uint64{}}
}

func (c *Counters) Run(event string) {
	c.mu.Lock()
	c.runs[event]++
	c.mu.Unlock()
}

func (c *Counters) Node(t domain.NodeType, s domain.NodeExecStatus, d time.Duration) {
	c.mu.Lock()
	c.nodes[[2]string{string(t), string(s)}]++
	c.durN++
	c.durSumMs += float64(d.Microseconds()) / 1000
	c.mu.Unlock()
}

func (c *Counters) Sweep(kind string, n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.sweeps[kind] += uint64(n)
	c.mu.Unlock()
}

func (c *Counters) Render() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	b.WriteString("# HELP omnira_flow_runs_total Flow run events (started, completed, failed, cancelled, handoff, duplicate, ignored).\n# TYPE omnira_flow_runs_total counter\n")
	for _, k := range sortedStr(c.runs) {
		fmt.Fprintf(&b, "omnira_flow_runs_total{event=%q} %d\n", k, c.runs[k])
	}
	b.WriteString("# HELP omnira_flow_node_executions_total Node executions by type and outcome.\n# TYPE omnira_flow_node_executions_total counter\n")
	keys := make([][2]string, 0, len(c.nodes))
	for k := range c.nodes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "omnira_flow_node_executions_total{type=%q,status=%q} %d\n", k[0], k[1], c.nodes[k])
	}
	b.WriteString("# HELP omnira_flow_node_duration_ms Time spent executing nodes.\n# TYPE omnira_flow_node_duration_ms summary\n")
	fmt.Fprintf(&b, "omnira_flow_node_duration_ms_sum %g\nomnira_flow_node_duration_ms_count %d\n", c.durSumMs, c.durN)
	b.WriteString("# HELP omnira_flow_sweeper_total Sweeper actions (timeout, released, cancelled).\n# TYPE omnira_flow_sweeper_total counter\n")
	for _, k := range sortedStr(c.sweeps) {
		fmt.Fprintf(&b, "omnira_flow_sweeper_total{kind=%q} %d\n", k, c.sweeps[k])
	}
	return b.String()
}

func sortedStr(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
