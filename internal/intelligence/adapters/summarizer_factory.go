package adapters

import (
	"time"

	aiadapters "github.com/omnira/omnira/internal/ai/adapters"
	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/ports"
	"github.com/omnira/omnira/internal/platform/config"
)

// NewTopicSummarizerFromConfig reuses the text generator the conversation summary already uses (same provider, model, key
// and timeout, same global AI switch). It returns nil when AI is not ready: summaries are then simply absent, never a
// half-configured attempt that fails at request time.
func NewTopicSummarizerFromConfig(cfg *config.Config) ports.TopicSummarizer {
	if cfg == nil || !cfg.AIReady() {
		return nil
	}
	gen, err := aiadapters.NewOpenAIGenerator(aiadapters.OpenAIConfig{APIKey: cfg.AIAPIKey, Model: cfg.AIModel, Timeout: time.Duration(cfg.AITimeoutSeconds) * time.Second})
	if err != nil {
		return nil
	}
	s, err := application.NewAITopicSummarizer(gen, cfg.AIProvider, cfg.AIModel, 600)
	if err != nil {
		return nil
	}
	return s
}
