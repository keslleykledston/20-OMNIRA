package opsmetrics

import (
	"strings"
	"testing"
)

func TestCounterExportsEveryDeclaredValueAndDropsTheRest(t *testing.T) {
	c := NewCounter("test_events_total", "Test events", "outcome", "ok", "error")
	c.Inc("ok")
	c.Add("ok", 2)
	c.Inc("a-company-id-from-a-request")
	out := Render()
	for _, want := range []string{
		`test_events_total{outcome="error"} 0`,
		`test_events_total{outcome="ok"} 3`,
		`test_events_total_dropped_total 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "a-company-id") {
		t.Error("a label value outside the declared set reached the output")
	}
}

func TestGaugeIsNotRenderedUntilSet(t *testing.T) {
	g := NewGauge("test_last_success_timestamp_seconds", "Test gauge")
	if strings.Contains(Render(), "test_last_success_timestamp_seconds") {
		t.Fatal("an unset gauge is rendered as if it were zero")
	}
	g.Set(42)
	if !strings.Contains(Render(), "test_last_success_timestamp_seconds 42") {
		t.Errorf("gauge missing:\n%s", Render())
	}
}

func TestRegisteringTheSameNameTwicePanics(t *testing.T) {
	NewCounter("test_dup_total", "x", "")
	defer func() {
		if recover() == nil {
			t.Error("a second registration of the same name did not panic")
		}
	}()
	NewCounter("test_dup_total", "x", "")
}
