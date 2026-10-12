// Package opsmetrics is a tiny in-process registry of Prometheus counters and gauges for the operational signals of the Hub (projector freshness, delegated
// serving outcomes, refused session contexts). It renders the text format by itself, so /metrics of api and worker can include it without a new dependency
// (the media pipeline and the flow runtime already do the same with their own counters).
//
// Cardinality is a rule, not a hope (docs/architecture/OBSERVABILITY.md): label VALUES come from closed sets written in code (an outcome, a kind). Never a company,
// person, hub, conversation or any id from a request: those belong to logs and traces. A Counter refuses (drops and counts) a label value outside the
// closed set it was declared with, so a bug cannot turn a metric into an unbounded series.
package opsmetrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	regMu    sync.Mutex
	registry []renderer
)

type renderer interface {
	name() string
	render(*strings.Builder)
}

// Counter is a monotonically increasing value per label tuple.
type Counter struct {
	n, help string
	label   string
	allowed map[string]bool
	mu      sync.Mutex
	vals    map[string]uint64
	dropped uint64
}

// NewCounter registers a counter with ONE label whose allowed values are fixed (an empty label name means no label).
func NewCounter(name, help, label string, allowed ...string) *Counter {
	c := &Counter{n: name, help: help, label: label, allowed: map[string]bool{}, vals: map[string]uint64{}}
	for _, a := range allowed {
		c.allowed[a] = true
	}
	register(c)
	return c
}

// Inc adds one for the value; a value outside the declared set is dropped (and counted in <name>_dropped_total).
func (c *Counter) Inc(value string) { c.Add(value, 1) }

// Add adds n for the value.
func (c *Counter) Add(value string, n uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.label == "" {
		c.vals[""] += n
		return
	}
	if !c.allowed[value] {
		c.dropped += n
		return
	}
	c.vals[value] += n
}

// Value returns the current count for the value (tests).
func (c *Counter) Value(value string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vals[value]
}

func (c *Counter) name() string { return c.n }
func (c *Counter) render(sb *strings.Builder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(sb, "# HELP %s %s\n# TYPE %s counter\n", c.n, c.help, c.n)
	if c.label == "" {
		fmt.Fprintf(sb, "%s %d\n", c.n, c.vals[""])
		return
	}
	// every allowed value is exported (zero included): an absent series cannot be told from a metric that is not wired
	keys := make([]string, 0, len(c.allowed))
	for k := range c.allowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(sb, "%s{%s=%q} %d\n", c.n, c.label, k, c.vals[k])
	}
	if c.dropped > 0 {
		fmt.Fprintf(sb, "# HELP %s_dropped_total Increments refused because the label value is not in the declared set\n# TYPE %s_dropped_total counter\n%s_dropped_total %d\n", c.n, c.n, c.n, c.dropped)
	}
}

// Gauge is a value that goes up and down; it has no labels.
type Gauge struct {
	n, help string
	mu      sync.Mutex
	v       float64
	set     bool
}

// NewGauge registers a gauge. Until the first Set it is not rendered (an unknown value is not zero).
func NewGauge(name, help string) *Gauge {
	g := &Gauge{n: name, help: help}
	register(g)
	return g
}

func (g *Gauge) Set(v float64) {
	g.mu.Lock()
	g.v, g.set = v, true
	g.mu.Unlock()
}

// SetNow records the current Unix time in seconds.
func (g *Gauge) SetNow() { g.Set(float64(time.Now().Unix())) }

func (g *Gauge) Value() (float64, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.v, g.set
}

func (g *Gauge) name() string { return g.n }
func (g *Gauge) render(sb *strings.Builder) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.set || math.IsNaN(g.v) {
		return
	}
	fmt.Fprintf(sb, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", g.n, g.help, g.n, g.n, g.v)
}

func register(r renderer) {
	regMu.Lock()
	defer regMu.Unlock()
	for _, e := range registry {
		if e.name() == r.name() {
			panic("opsmetrics: metric registered twice: " + r.name())
		}
	}
	registry = append(registry, r)
}

// Render is the Prometheus text of every registered metric, ordered by name.
func Render() string {
	regMu.Lock()
	rs := append([]renderer(nil), registry...)
	regMu.Unlock()
	sort.Slice(rs, func(i, j int) bool { return rs[i].name() < rs[j].name() })
	var sb strings.Builder
	sb.WriteString("\n")
	for _, r := range rs {
		r.render(&sb)
	}
	return sb.String()
}
