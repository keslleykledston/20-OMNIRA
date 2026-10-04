package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

var (
	ErrToolGatewayDisabled = errors.New("intelligence: the AI tool gateway is disabled")
	ErrBadIdempotencyKey   = errors.New("intelligence: invalid idempotency key")
)

// ToolAuthorizer answers, for the CURRENT session's user, what the gateway must check before anything runs.
type ToolAuthorizer interface {
	Has(ctx context.Context, permission string) (bool, error)
	OperatesTopic(ctx context.Context, topicID uuid.UUID) (bool, error)
}

// ToolExecutor runs one tool for a topic with the current session's authority. source says who asked (it decides the
// origin recorded on whatever the tool creates).
type ToolExecutor func(ctx context.Context, topicID uuid.UUID, args json.RawMessage, source domain.ToolSource) (any, error)

const maxToolResultBytes = 12 * 1024

// ToolGateway is the only door through which an AI feature may act (ADR-0017 Wave 12). It is deliberately small:
//   - a closed registry of REAL tools (tools without an executor are not offered);
//   - strict arguments, tenant always from the session;
//   - the requesting user's own permissions apply to every tool;
//   - a write asked for by the AI waits in an approval queue until a person approves it;
//   - one audited row per request, idempotent by key.
type ToolGateway struct {
	registry map[string]domain.ToolSpec
	execs    map[string]ToolExecutor
	repo     ports.ToolCallRepository
	topics   ports.TopicRepository
	auth     ToolAuthorizer
	flags    Flags
}

func NewToolGateway(repo ports.ToolCallRepository, topics ports.TopicRepository, auth ToolAuthorizer, execs map[string]ToolExecutor, flags Flags) *ToolGateway {
	return &ToolGateway{registry: domain.ToolRegistry(), execs: execs, repo: repo, topics: topics, auth: auth, flags: flags}
}

// ToolView is a catalog entry.
type ToolView struct {
	Name        string
	Description string
	Risk        domain.ToolRisk
	Permission  string
	// AIRequiresApproval: when the AI asks for it, a person must approve it first.
	AIRequiresApproval bool
}

// Enabled reports whether the feature flag is on.
func (g *ToolGateway) Enabled() bool { return g.flags.AIToolGatewayEnabled }

// Catalog lists only tools that really can run here.
func (g *ToolGateway) Catalog() []ToolView {
	out := []ToolView{}
	for name, spec := range g.registry {
		if _, ok := g.execs[name]; !ok {
			continue
		}
		out = append(out, ToolView{Name: name, Description: spec.Description, Risk: spec.Risk, Permission: spec.Permission, AIRequiresApproval: domain.DecideTool(spec, domain.SourceAIToolCall) == domain.DecisionNeedsApproval})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (g *ToolGateway) tenant(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("intelligence: tenant context required")
	}
	return tc, nil
}

func (g *ToolGateway) authorizeTool(ctx context.Context, spec domain.ToolSpec, topicID uuid.UUID) error {
	if ok, err := g.auth.Has(ctx, spec.Permission); err != nil {
		return err
	} else if !ok {
		return domain.ErrToolForbidden
	}
	if spec.Risk == domain.RiskLowWrite {
		if ok, err := g.auth.OperatesTopic(ctx, topicID); err != nil {
			return err
		} else if !ok {
			return domain.ErrToolForbidden
		}
	}
	return nil
}

func actorPtr(tc *tenancydomain.TenantContext) *uuid.UUID {
	if tc.ActorID == uuid.Nil {
		return nil
	}
	a := tc.ActorID
	return &a
}

// Invoke handles a request. The returned call carries the outcome: executed, pending_approval, denied (with
// domain.ErrToolForbidden) or failed. A replay of the same idempotency key returns the first outcome and acts at most once.
func (g *ToolGateway) Invoke(ctx context.Context, topicID uuid.UUID, tool string, rawArgs json.RawMessage, key string, source domain.ToolSource) (*domain.ToolCall, error) {
	if !g.flags.AIToolGatewayEnabled {
		return nil, ErrToolGatewayDisabled
	}
	tc, err := g.tenant(ctx)
	if err != nil {
		return nil, err
	}
	spec, ok := g.registry[tool]
	if _, runnable := g.execs[tool]; !ok || !runnable {
		return nil, domain.ErrUnknownTool
	}
	if source != domain.SourceAIToolCall && source != domain.SourceAgentTool {
		return nil, fmt.Errorf("%w: unknown source", domain.ErrInvalidArgs)
	}
	if !domain.ValidIdempotencyKey(key) {
		return nil, ErrBadIdempotencyKey
	}
	args, err := spec.Args(rawArgs)
	if err != nil {
		return nil, err
	}
	if _, err := g.topics.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return nil, err
	}
	call := &domain.ToolCall{TenantID: tc.TenantID, TopicThreadID: topicID, Tool: tool, Risk: spec.Risk, Source: source, Args: args, IdempotencyKey: key, RequestedBy: actorPtr(tc)}

	if err := g.authorizeTool(ctx, spec, topicID); err != nil {
		if !errors.Is(err, domain.ErrToolForbidden) {
			return nil, err
		}
		call.Status, call.Error = domain.ToolDenied, "permission"
		stored, _, berr := g.repo.Begin(ctx, call)
		if berr != nil {
			return nil, berr
		}
		return stored, domain.ErrToolForbidden
	}

	switch domain.DecideTool(spec, source) {
	case domain.DecisionNeedsApproval:
		call.Status = domain.ToolPendingApproval
		stored, _, err := g.repo.Begin(ctx, call)
		return stored, err
	default:
		call.Status = "running"
		stored, inserted, err := g.repo.Begin(ctx, call)
		if err != nil || !inserted {
			return stored, err // a replay: the first outcome, nothing executes again
		}
		return g.execute(ctx, tc, stored, "running", source, nil)
	}
}

// execute runs the tool and records the outcome, moving the call from `from`. The state transition happens BEFORE the tool
// runs for approvals (see Approve) so two approvers can never both execute.
func (g *ToolGateway) execute(ctx context.Context, tc *tenancydomain.TenantContext, call *domain.ToolCall, from domain.ToolCallStatus, source domain.ToolSource, decidedBy *uuid.UUID) (*domain.ToolCall, error) {
	out, runErr := g.execs[call.Tool](ctx, call.TopicThreadID, call.Args, source)
	to, errText := domain.ToolExecuted, ""
	var result json.RawMessage
	if runErr != nil {
		to, errText = domain.ToolFailed, safeToolError(runErr)
	} else if b, err := json.Marshal(out); err != nil || len(b) > maxToolResultBytes {
		to, errText = domain.ToolFailed, "result_too_large"
	} else {
		result = b
	}
	if _, err := g.repo.Transition(ctx, tc.TenantID, call.ID, from, to, result, errText, decidedBy); err != nil {
		return nil, err
	}
	call.Status, call.Result, call.Error = to, result, errText
	if decidedBy != nil {
		call.DecidedBy = decidedBy
	}
	return call, nil
}

// safeToolError never hands raw error text to the caller: a short fixed reason.
func safeToolError(err error) string {
	switch {
	case errors.Is(err, domain.ErrInvalidTransition):
		return "not_allowed_now"
	case errors.Is(err, domain.ErrTopicNotFound), errors.Is(err, domain.ErrReferenceNotFound):
		return "not_found"
	case errors.Is(err, ErrNothingToSummarize):
		return "nothing_to_do"
	case errors.Is(err, ports.ErrSummarizerUnavailable):
		return "provider_unavailable"
	}
	return "failed"
}

// Approve executes a pending call with the APPROVER's authority. The approver must be allowed to use the tool and to
// operate the topic, exactly as if they had asked for it themselves.
func (g *ToolGateway) Approve(ctx context.Context, topicID, callID uuid.UUID) (*domain.ToolCall, error) {
	if !g.flags.AIToolGatewayEnabled {
		return nil, ErrToolGatewayDisabled
	}
	tc, err := g.tenant(ctx)
	if err != nil {
		return nil, err
	}
	call, err := g.repo.Get(ctx, tc.TenantID, callID)
	if err != nil {
		return nil, err
	}
	if call.TopicThreadID != topicID {
		return nil, domain.ErrReferenceNotFound
	}
	spec, ok := g.registry[call.Tool]
	if _, runnable := g.execs[call.Tool]; !ok || !runnable {
		return nil, domain.ErrUnknownTool
	}
	if call.Status != domain.ToolPendingApproval {
		return nil, domain.ErrInvalidTransition
	}
	if err := g.authorizeTool(ctx, spec, topicID); err != nil {
		return nil, err
	}
	args, err := spec.Args(call.Args) // validated again: the stored arguments are never trusted blindly
	if err != nil {
		return nil, err
	}
	call.Args = args
	by := actorPtr(tc)
	won, err := g.repo.Transition(ctx, tc.TenantID, callID, domain.ToolPendingApproval, "running", nil, "", by)
	if err != nil {
		return nil, err
	}
	if !won {
		return nil, domain.ErrInvalidTransition // another approver (or a rejection) got there first
	}
	return g.execute(ctx, tc, call, "running", call.Source, by)
}

func (g *ToolGateway) Reject(ctx context.Context, topicID, callID uuid.UUID) error {
	if !g.flags.AIToolGatewayEnabled {
		return ErrToolGatewayDisabled
	}
	tc, err := g.tenant(ctx)
	if err != nil {
		return err
	}
	call, err := g.repo.Get(ctx, tc.TenantID, callID)
	if err != nil {
		return err
	}
	if call.TopicThreadID != topicID {
		return domain.ErrReferenceNotFound
	}
	spec, ok := g.registry[call.Tool]
	if !ok {
		return domain.ErrUnknownTool
	}
	if err := g.authorizeTool(ctx, spec, topicID); err != nil {
		return err
	}
	won, err := g.repo.Transition(ctx, tc.TenantID, callID, domain.ToolPendingApproval, domain.ToolRejected, nil, "", actorPtr(tc))
	if err != nil {
		return err
	}
	if !won {
		return domain.ErrInvalidTransition
	}
	return nil
}

func (g *ToolGateway) List(ctx context.Context, topicID uuid.UUID) ([]domain.ToolCall, error) {
	tc, err := g.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := g.topics.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return nil, err
	}
	return g.repo.List(ctx, tc.TenantID, topicID, 50)
}

// NewToolExecutors binds the registry to the REAL services. A nil service simply leaves its tools out of the catalog.
func NewToolExecutors(topicSvc *TopicService, summaries *SummaryService, tickets *TopicTicketService) map[string]ToolExecutor {
	ex := map[string]ToolExecutor{}
	if summaries != nil {
		ex["topic.get_summary"] = func(ctx context.Context, topicID uuid.UUID, _ json.RawMessage, _ domain.ToolSource) (any, error) {
			list, err := summaries.List(ctx, topicID)
			if err != nil {
				return nil, err
			}
			type item struct {
				Version int    `json:"version"`
				Status  string `json:"status"`
				Text    string `json:"summary_text"`
			}
			out := []item{}
			for i, s := range list {
				if i == 3 {
					break
				}
				out = append(out, item{s.Version, string(s.Status), s.SummaryText})
			}
			return map[string]any{"summaries": out}, nil
		}
		ex["topic.generate_summary"] = func(ctx context.Context, topicID uuid.UUID, _ json.RawMessage, _ domain.ToolSource) (any, error) {
			s, created, err := summaries.Generate(ctx, topicID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"version": s.Version, "status": string(s.Status), "created": created}, nil
		}
	}
	if topicSvc != nil {
		ex["topic.list_tickets"] = func(ctx context.Context, topicID uuid.UUID, _ json.RawMessage, _ domain.ToolSource) (any, error) {
			list, err := topicSvc.ListTopicTickets(ctx, topicID)
			if err != nil {
				return nil, err
			}
			type item struct {
				TicketID string `json:"ticket_id"`
				Relation string `json:"relation"`
				Status   string `json:"status"`
			}
			out := []item{}
			for _, t := range list {
				out = append(out, item{t.ID.String(), string(t.Relation), t.Status})
			}
			return map[string]any{"tickets": out}, nil
		}
	}
	if tickets != nil {
		ex["topic.ticket_policy_advice"] = func(ctx context.Context, topicID uuid.UUID, _ json.RawMessage, _ domain.ToolSource) (any, error) {
			adv, err := tickets.Advise(ctx, topicID)
			if err != nil {
				return nil, err
			}
			allowed := []string{}
			for _, a := range adv.Allowed {
				allowed = append(allowed, string(a))
			}
			return map[string]any{"action": string(adv.Action), "reason": adv.Reason, "allowed_actions": allowed}, nil
		}
		ex["topic.apply_ticket_policy"] = func(ctx context.Context, topicID uuid.UUID, args json.RawMessage, source domain.ToolSource) (any, error) {
			var a struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, domain.ErrInvalidArgs
			}
			origin := domain.TicketLinkAgent
			if source == domain.SourceAIToolCall {
				origin = domain.TicketLinkAI // a person approved it, but the idea came from the AI: the record says so
			}
			res, err := tickets.Apply(ctx, topicID, domain.TicketAction(a.Action), origin)
			if err != nil {
				return nil, err
			}
			return map[string]any{"action": string(res.Action), "ticket_id": res.TicketID.String(), "relation": string(res.Relation), "created": res.Created}, nil
		}
	}
	return ex
}
