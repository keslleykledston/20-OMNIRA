package application

import (
	"errors"
	"time"

	aiports "github.com/omnira/omnira/internal/ai/ports"
)

// Task is a kind of AI work. Each task can use a different model (a cheap one to classify, a better one to summarize).
type Task string

const (
	TaskTopicClassify Task = "topic_classify"
	TaskTopicSummary  Task = "topic_summary"
)

// ErrNoModel: no model is configured for the task. The caller degrades (deterministic router, manual work); it never fails.
var ErrNoModel = errors.New("intelligence: no model configured for this task")

// ModelRoute is one task's provider, model and hard limits. The generator is the provider-neutral port, so a provider is
// replaceable without touching the use cases.
type ModelRoute struct {
	Provider        string
	Model           string
	Generator       aiports.TextGenerator
	MaxOutputTokens int
	Timeout         time.Duration
}

// ModelRouter picks the route for a task (ADR-0017 Wave 6). A task without a route is simply unavailable.
type ModelRouter struct{ routes map[Task]ModelRoute }

func NewModelRouter() *ModelRouter { return &ModelRouter{routes: map[Task]ModelRoute{}} }

// Set registers a route; a route without a generator is ignored so a half-configured task stays absent.
func (m *ModelRouter) Set(t Task, r ModelRoute) *ModelRouter {
	if r.Generator != nil {
		m.routes[t] = r
	}
	return m
}

func (m *ModelRouter) Route(t Task) (ModelRoute, error) {
	if m == nil {
		return ModelRoute{}, ErrNoModel
	}
	r, ok := m.routes[t]
	if !ok {
		return ModelRoute{}, ErrNoModel
	}
	return r, nil
}
