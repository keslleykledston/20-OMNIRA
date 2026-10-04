package application

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Counters is an in-process Metrics implementation that renders Prometheus text, so the worker's /metrics can
// include the media pipeline without a new dependency.
type Counters struct {
	mu     sync.Mutex
	counts map[[2]string]uint64
	avUp   int // 1 up, 0 down, -1 unknown
}

func NewCounters() *Counters { return &Counters{counts: map[[2]string]uint64{}, avUp: -1} }

func (c *Counters) Inc(stage, outcome string) {
	c.mu.Lock()
	c.counts[[2]string{stage, outcome}]++
	c.mu.Unlock()
}

// SetAntivirusUp records the last health probe of the antivirus.
func (c *Counters) SetAntivirusUp(up bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if up {
		c.avUp = 1
	} else {
		c.avUp = 0
	}
}

func (c *Counters) Render() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([][2]string, 0, len(c.counts))
	for k := range c.counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	var sb strings.Builder
	sb.WriteString("\n# HELP omnira_media_events_total Inbound media pipeline events by stage and outcome\n# TYPE omnira_media_events_total counter\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "omnira_media_events_total{stage=%q,outcome=%q} %d\n", k[0], k[1], c.counts[k])
	}
	if c.avUp >= 0 {
		sb.WriteString("# HELP omnira_media_antivirus_up 1 when clamd answered the last probe\n# TYPE omnira_media_antivirus_up gauge\n")
		fmt.Fprintf(&sb, "omnira_media_antivirus_up %d\n", c.avUp)
	}
	return sb.String()
}
