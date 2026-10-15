package adapters

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// AIBridge is the AI tool gateway's way into the module (ADR-0049 phase 6). It implements intelligence's ServiceBridge with plain types. Every call runs under the
// session of the PERSON who asked or approved, so the module asks that person's keys (service.read / service.create / service.update): the AI has none of its own.
// Only reading, proposing a task and proposing a classification exist here. There is deliberately no way to close a session, touch a case, link an ERP record or
// ask an ERP for anything through the AI.
type AIBridge struct{ svc *application.Service }

func NewAIBridge(svc *application.Service) *AIBridge { return &AIBridge{svc: svc} }

func (b *AIBridge) CurrentSession(ctx context.Context, conversationID uuid.UUID) (map[string]any, error) {
	sess, _, err := b.svc.ForConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if sess == nil { // the attendance never had a service session
		return nil, nil
	}
	out := map[string]any{"protocol": sess.Protocol, "status": string(sess.Status), "opened_at": sess.OpenedAt, "reopen_count": sess.ReopenCount}
	if sess.CategoryID != nil {
		out["has_category"] = true
	}
	if sess.ResolutionCanonical != nil {
		out["result"] = *sess.ResolutionCanonical
	}
	return out, nil
}

func (b *AIBridge) OpenTasks(ctx context.Context, conversationID uuid.UUID, limit int) ([]map[string]any, error) {
	sess, _, err := b.svc.ForConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return []map[string]any{}, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	list, _, err := b.svc.ListTasks(ctx, ports.TaskFilter{SessionID: &sess.ID, PendingOnly: true, Limit: limit})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		it := map[string]any{"title": t.Title, "status": string(t.Status), "priority": string(t.Priority), "origin": t.OriginType, "overdue": t.Overdue(now)}
		out = append(out, it)
	}
	return out, nil
}

func (b *AIBridge) ProposeTask(ctx context.Context, conversationID uuid.UUID, title, priority string, byAI bool) (uuid.UUID, error) {
	origin := domain.OriginHuman
	if byAI {
		origin = domain.OriginAI // a person approved it, but the idea came from the AI: the record says so
	}
	in := domain.TaskOpenInput{Title: title, Priority: domain.Priority(priority), OriginType: origin}
	sess, _, err := b.svc.ForConversation(ctx, conversationID)
	if err != nil {
		return uuid.Nil, err
	}
	if sess != nil && sess.Status.Active() {
		in.SessionID = &sess.ID // a task for the attendance in progress; without one it is a task of the contact, which is allowed
	}
	res, err := b.svc.CreateTask(ctx, in)
	if err != nil {
		return uuid.Nil, err
	}
	return res.Task.ID, nil
}

func (b *AIBridge) Classify(ctx context.Context, conversationID uuid.UUID, categoryCode string) error {
	return b.svc.ClassifyByCode(ctx, conversationID, categoryCode)
}
