package application

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ServiceBridge is what the AI tools need from the service-context module (ADR-0049). It uses plain types on purpose: the module implements it without
// importing this package. Every call runs with the REQUESTING (or approving) person's own session, so the module's permissions apply to the person, never to the AI.
type ServiceBridge interface {
	// CurrentSession describes the active session of the conversation, or the latest one; nil, nil when it never had one.
	CurrentSession(ctx context.Context, conversationID uuid.UUID) (map[string]any, error)
	OpenTasks(ctx context.Context, conversationID uuid.UUID, limit int) ([]map[string]any, error)
	// ProposeTask makes a pending task; byAI records that the idea came from the AI (origin "ai") although a person approved it.
	ProposeTask(ctx context.Context, conversationID uuid.UUID, title, priority string, byAI bool) (uuid.UUID, error)
	Classify(ctx context.Context, conversationID uuid.UUID, categoryCode string) error
}

// NewServiceExecutors binds the service.* tools to the bridge. A nil bridge offers no tool (the module is off).
func NewServiceExecutors(topics ports.TopicRepository, br ServiceBridge) map[string]ToolExecutor {
	ex := map[string]ToolExecutor{}
	if br == nil || topics == nil {
		return ex
	}
	conversationOf := func(ctx context.Context, topicID uuid.UUID) (uuid.UUID, error) {
		tc, err := tenancydomain.FromContext(ctx)
		if err != nil || tc.TenantID == uuid.Nil {
			return uuid.Nil, errors.New("intelligence: tenant context required")
		}
		topic, err := topics.GetTopic(ctx, tc.TenantID, topicID)
		if err != nil {
			return uuid.Nil, err
		}
		if topic.OriginConversationID == nil {
			return uuid.Nil, domain.ErrReferenceNotFound
		}
		return *topic.OriginConversationID, nil
	}
	ex["service.current_session"] = func(ctx context.Context, topicID uuid.UUID, _ json.RawMessage, _ domain.ToolSource) (any, error) {
		conv, err := conversationOf(ctx, topicID)
		if err != nil {
			return nil, err
		}
		s, err := br.CurrentSession(ctx, conv)
		if err != nil {
			return nil, err
		}
		return map[string]any{"session": s}, nil
	}
	ex["service.open_tasks"] = func(ctx context.Context, topicID uuid.UUID, args json.RawMessage, _ domain.ToolSource) (any, error) {
		limit, err := limitOf(args, 10)
		if err != nil {
			return nil, err
		}
		conv, err := conversationOf(ctx, topicID)
		if err != nil {
			return nil, err
		}
		list, err := br.OpenTasks(ctx, conv, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tasks": list}, nil
	}
	ex["service.propose_task"] = func(ctx context.Context, topicID uuid.UUID, args json.RawMessage, source domain.ToolSource) (any, error) {
		var a struct {
			Title    string `json:"title"`
			Priority string `json:"priority"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, domain.ErrInvalidArgs
		}
		conv, err := conversationOf(ctx, topicID)
		if err != nil {
			return nil, err
		}
		id, err := br.ProposeTask(ctx, conv, a.Title, a.Priority, source == domain.SourceAIToolCall)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": id.String(), "origin": originOf(source)}, nil
	}
	ex["service.propose_classification"] = func(ctx context.Context, topicID uuid.UUID, args json.RawMessage, _ domain.ToolSource) (any, error) {
		var a struct {
			Category string `json:"category"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, domain.ErrInvalidArgs
		}
		conv, err := conversationOf(ctx, topicID)
		if err != nil {
			return nil, err
		}
		if err := br.Classify(ctx, conv, a.Category); err != nil {
			return nil, err
		}
		return map[string]any{"category": a.Category}, nil
	}
	return ex
}

func originOf(source domain.ToolSource) string {
	if source == domain.SourceAIToolCall {
		return "ai"
	}
	return "human"
}
