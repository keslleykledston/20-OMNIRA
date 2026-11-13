package application

import "testing"

// CONTEXT.2A: the flag must be off by default (FOUNDATION CODE, not cleared for production) and read independently
// of every other flag.
func TestContextIntelligenceV2FlagIsOffByDefaultAndReadFromTheEnvironment(t *testing.T) {
	if DefaultFlags().ContextIntelligenceV2Enabled {
		t.Fatal("context intelligence v2 is opt-in: FOUNDATION CODE, not cleared for production")
	}
	on := FlagsFromEnv(func(k string) string {
		if k == "OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED" {
			return "true"
		}
		return ""
	})
	if !on.ContextIntelligenceV2Enabled || on.CopilotEnabled || on.TopicSummariesEnabled {
		t.Fatalf("only the context intelligence v2 flag is set: %+v", on)
	}
}
