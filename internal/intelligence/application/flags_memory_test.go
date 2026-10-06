package application

import "testing"

func TestContactMemoryFlagIsOffByDefaultAndReadFromTheEnvironment(t *testing.T) {
	if DefaultFlags().CopilotContactMemoryEnabled {
		t.Fatal("the contact memory is opt-in")
	}
	on := FlagsFromEnv(func(k string) string {
		if k == "OMNIRA_COPILOT_CONTACT_MEMORY_ENABLED" {
			return "true"
		}
		return ""
	})
	if !on.CopilotContactMemoryEnabled || on.CopilotEnabled {
		t.Fatalf("only the memory flag is set: %+v", on)
	}
}
