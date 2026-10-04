package application

import (
	"os"
	"strings"
)

// Flags are the independent switches of Conversation Intelligence (ADR-0017). Automation defaults to OFF: the code is
// ready, the rollout is a decision. Read once at startup from the environment (same mechanism as the rest of OMNIRA).
type Flags struct {
	TopicThreadsEnabled       bool // topics, links and their API: on
	TopicAutoRoutingEnabled   bool // deterministic router attaches messages automatically
	TopicAIRoutingEnabled     bool // AI proposes a topic (shadow first)
	TopicSummariesEnabled     bool
	PrivateHandoffEnabled     bool
	MultimodalAnalysisEnabled bool
	CopilotEnabled            bool
	AIToolGatewayEnabled      bool
	AutoTicketPolicyEnabled   bool // never creates tickets by itself unless this is on
}

// DefaultFlags is the safe default: topics exist, nothing acts on its own.
func DefaultFlags() Flags {
	return Flags{TopicThreadsEnabled: true}
}

// FlagsFromEnv overrides the defaults from OMNIRA_<FLAG> variables ("true"/"false"). Unknown values keep the default.
func FlagsFromEnv(get func(string) string) Flags {
	if get == nil {
		get = os.Getenv
	}
	f := DefaultFlags()
	set := func(name string, dst *bool) {
		switch strings.ToLower(strings.TrimSpace(get(name))) {
		case "true", "1", "yes", "on":
			*dst = true
		case "false", "0", "no", "off":
			*dst = false
		}
	}
	set("OMNIRA_TOPIC_THREADS_ENABLED", &f.TopicThreadsEnabled)
	set("OMNIRA_TOPIC_AUTO_ROUTING_ENABLED", &f.TopicAutoRoutingEnabled)
	set("OMNIRA_TOPIC_AI_ROUTING_ENABLED", &f.TopicAIRoutingEnabled)
	set("OMNIRA_TOPIC_SUMMARIES_ENABLED", &f.TopicSummariesEnabled)
	set("OMNIRA_PRIVATE_HANDOFF_ENABLED", &f.PrivateHandoffEnabled)
	set("OMNIRA_MULTIMODAL_ANALYSIS_ENABLED", &f.MultimodalAnalysisEnabled)
	set("OMNIRA_COPILOT_ENABLED", &f.CopilotEnabled)
	set("OMNIRA_AI_TOOL_GATEWAY_ENABLED", &f.AIToolGatewayEnabled)
	set("OMNIRA_AUTO_TICKET_POLICY_ENABLED", &f.AutoTicketPolicyEnabled)
	return f
}
