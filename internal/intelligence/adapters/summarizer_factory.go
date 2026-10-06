package adapters

import (
	"os"
	"strings"
	"time"

	aiadapters "github.com/omnira/omnira/internal/ai/adapters"
	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/ports"
	"github.com/omnira/omnira/internal/platform/config"
)

// NewModelRouterFromConfig reuses the provider the conversation summary already uses (same provider, key, timeout and
// global AI switch). Each task may use its own model: OMNIRA_AI_MODEL_TOPIC_CLASSIFY, OMNIRA_AI_MODEL_TOPIC_SUMMARY and OMNIRA_AI_MODEL_COPILOT
// override OMNIRA_AI_MODEL (a small model to classify, a better one to summarize). When AI is not ready the router is
// empty: every task is simply unavailable, never a half-configured attempt that fails at request time.
func NewModelRouterFromConfig(cfg *config.Config) *application.ModelRouter {
	router := application.NewModelRouter()
	if cfg == nil || !cfg.AIReady() {
		return router
	}
	timeout := time.Duration(cfg.AITimeoutSeconds) * time.Second
	add := func(task application.Task, envName string, maxTokens int) {
		model := strings.TrimSpace(os.Getenv(envName))
		if model == "" {
			model = cfg.AIModel
		}
		gen, err := aiadapters.NewOpenAIGenerator(aiadapters.OpenAIConfig{APIKey: cfg.AIAPIKey, Model: model, Timeout: timeout})
		if err != nil {
			return
		}
		router.Set(task, application.ModelRoute{Provider: cfg.AIProvider, Model: model, Generator: gen, MaxOutputTokens: maxTokens, Timeout: timeout})
	}
	add(application.TaskTopicClassify, "OMNIRA_AI_MODEL_TOPIC_CLASSIFY", 200)
	add(application.TaskTopicSummary, "OMNIRA_AI_MODEL_TOPIC_SUMMARY", 600)
	add(application.TaskCopilotReply, "OMNIRA_AI_MODEL_COPILOT", 700)
	add(application.TaskClosingSuggest, "OMNIRA_AI_MODEL_CLOSING_SUGGEST", 700) // ADR-0020: closing summary and follow-up items
	return router
}

// NewTopicSummarizerFromConfig is the summary task of the router as a TopicSummarizer (nil when unavailable).
func NewTopicSummarizerFromConfig(cfg *config.Config) ports.TopicSummarizer {
	r, err := NewModelRouterFromConfig(cfg).Route(application.TaskTopicSummary)
	if err != nil {
		return nil
	}
	s, err := application.NewAITopicSummarizer(r.Generator, r.Provider, r.Model, r.MaxOutputTokens)
	if err != nil {
		return nil
	}
	return s
}
