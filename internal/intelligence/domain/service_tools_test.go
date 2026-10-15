package domain

import (
	"strings"
	"testing"
)

// ADR-0049: the AI's service tools are a closed, small set. Reads execute; every write the AI asks for waits for a person; nothing that closes a session, touches a
// case or asks an ERP is a tool.
func TestServiceToolsAreReadOrProposeOnly(t *testing.T) {
	allowed := map[string]ToolRisk{"service.current_session": RiskRead, "service.open_tasks": RiskRead, "service.propose_task": RiskLowWrite, "service.propose_classification": RiskLowWrite}
	for name, spec := range ToolRegistry() {
		if !strings.HasPrefix(name, "service.") {
			continue
		}
		risk, ok := allowed[name]
		if !ok {
			t.Errorf("%s: an unexpected service tool", name)
			continue
		}
		if spec.Risk != risk {
			t.Errorf("%s: risk %s, want %s", name, spec.Risk, risk)
		}
		if risk == RiskLowWrite && DecideTool(spec, SourceAIToolCall) != DecisionNeedsApproval {
			t.Errorf("%s: a write asked for by the AI must wait for a person", name)
		}
		if !strings.HasPrefix(spec.Permission, "service.") || strings.Contains(spec.Permission, "request_external") {
			t.Errorf("%s: permission %q", name, spec.Permission)
		}
	}
	for name := range allowed {
		if _, ok := ToolRegistry()[name]; !ok {
			t.Errorf("%s is missing", name)
		}
	}
}
