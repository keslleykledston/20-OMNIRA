package application

import (
	"strings"
	"testing"
)

func TestCountersRenderPrometheusTextAndAntivirusGauge(t *testing.T) {
	c := NewCounters()
	if strings.Contains(c.Render(), "antivirus_up") {
		t.Fatal("the gauge must be absent until the first probe")
	}
	c.Inc("scan", "clean")
	c.Inc("scan", "clean")
	c.Inc("scan", "infected")
	c.SetAntivirusUp(false)
	out := c.Render()
	for _, want := range []string{
		`omnira_media_events_total{stage="scan",outcome="clean"} 2`,
		`omnira_media_events_total{stage="scan",outcome="infected"} 1`,
		"omnira_media_antivirus_up 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	c.SetAntivirusUp(true)
	if !strings.Contains(c.Render(), "omnira_media_antivirus_up 1") {
		t.Fatal("gauge must flip to 1")
	}
}
