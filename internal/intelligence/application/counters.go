package application

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Counters renders Conversation Intelligence metrics as Prometheus text without a new dependency. It implements the
// small metric interfaces of the routing service and the job runner.
type Counters struct {
	mu        sync.Mutex
	decisions map[[3]string]uint64
	jobs      map[string]uint64
	shadow    map[string]uint64
	latencyN  uint64
	latencyS  float64
}

func NewCounters() *Counters {
	return &Counters{decisions: map[[3]string]uint64{}, jobs: map[string]uint64{}, shadow: map[string]uint64{}}
}

func (c *Counters) Decision(status string, applied bool, source string) {
	c.mu.Lock()
	c.decisions[[3]string{status, fmt.Sprint(applied), source}]++
	c.mu.Unlock()
}

func (c *Counters) Latency(d time.Duration) {
	c.mu.Lock()
	c.latencyN++
	c.latencyS += d.Seconds()
	c.mu.Unlock()
}

func (c *Counters) Shadow(outcome string) {
	c.mu.Lock()
	c.shadow[outcome]++
	c.mu.Unlock()
}

func (c *Counters) Job(state string) {
	c.mu.Lock()
	c.jobs[state]++
	c.mu.Unlock()
}

func (c *Counters) Render() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("\n# HELP topic_router_decisions_total Topic routing decisions by status, whether applied and the strongest evidence\n# TYPE topic_router_decisions_total counter\n")
	keys := make([][3]string, 0, len(c.decisions))
	for k := range c.decisions {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, k := range keys {
		fmt.Fprintf(&sb, "topic_router_decisions_total{status=%q,applied=%q,source=%q} %d\n", k[0], k[1], k[2], c.decisions[k])
	}
	sb.WriteString("# HELP topic_router_latency_seconds Total routing time (count and sum)\n# TYPE topic_router_latency_seconds summary\n")
	fmt.Fprintf(&sb, "topic_router_latency_seconds_count %d\ntopic_router_latency_seconds_sum %f\n", c.latencyN, c.latencyS)
	sb.WriteString("# HELP intelligence_jobs_total Pipeline jobs by outcome\n# TYPE intelligence_jobs_total counter\n")
	states := make([]string, 0, len(c.jobs))
	for k := range c.jobs {
		states = append(states, k)
	}
	sort.Strings(states)
	for _, s := range states {
		fmt.Fprintf(&sb, "intelligence_jobs_total{state=%q} %d\n", s, c.jobs[s])
	}
	sb.WriteString("# HELP topic_ai_shadow_total AI shadow classifications by outcome (existing/new/none or why none was recorded)\n# TYPE topic_ai_shadow_total counter\n")
	outs := make([]string, 0, len(c.shadow))
	for k := range c.shadow {
		outs = append(outs, k)
	}
	sort.Strings(outs)
	for _, o := range outs {
		fmt.Fprintf(&sb, "topic_ai_shadow_total{outcome=%q} %d\n", o, c.shadow[o])
	}
	return sb.String()
}
