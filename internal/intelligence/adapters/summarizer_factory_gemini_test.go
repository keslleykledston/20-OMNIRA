package adapters

import (
	"testing"

	aiadapters "github.com/omnira/omnira/internal/ai/adapters"
	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/platform/config"
)

func TestModelRouterBuildsEveryTaskWithGeminiAndPerTaskModels(t *testing.T) {
	t.Setenv("OMNIRA_AI_MODEL_COPILOT", "gemini-3.5-flash-lite")
	t.Setenv("OMNIRA_AI_MODEL_TOPIC_SUMMARY", "")
	cfg := &config.Config{AIEnabled: true, AIProvider: "gemini", AIModel: "gemini-3.1-flash-lite", AIAPIKey: "k", AITimeoutSeconds: 5}
	r := NewModelRouterFromConfig(cfg)
	for task, wantModel := range map[application.Task]string{
		application.TaskTopicClassify: "gemini-3.1-flash-lite", application.TaskTopicSummary: "gemini-3.1-flash-lite",
		application.TaskCopilotReply: "gemini-3.5-flash-lite", application.TaskClosingSuggest: "gemini-3.1-flash-lite",
	} {
		route, err := r.Route(task)
		if err != nil {
			t.Fatalf("%s: %v", task, err)
		}
		if route.Provider != "gemini" || route.Model != wantModel {
			t.Fatalf("%s: route = %s/%s, want gemini/%s", task, route.Provider, route.Model, wantModel)
		}
		if _, ok := route.Generator.(*aiadapters.GeminiGenerator); !ok {
			t.Fatalf("%s: generator is %T, want *GeminiGenerator", task, route.Generator)
		}
	}
	// a per-task model that is not a plain model name leaves that task unavailable instead of building a bad URL
	t.Setenv("OMNIRA_AI_MODEL_COPILOT", "../admin")
	if _, err := NewModelRouterFromConfig(cfg).Route(application.TaskCopilotReply); err == nil {
		t.Fatal("an unsafe per-task model must leave the task unavailable")
	}
}
