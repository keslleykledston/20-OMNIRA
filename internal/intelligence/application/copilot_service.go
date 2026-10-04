package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	aiports "github.com/omnira/omnira/internal/ai/ports"
	"github.com/omnira/omnira/internal/aiusage"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// TaskCopilotReply is the model task of the reply copilot.
const (
	TaskCopilotReply     Task = "copilot_reply"
	CopilotPromptVersion      = "copilot-reply-v1"
)

var (
	ErrCopilotDisabled    = errors.New("intelligence: copilot is disabled")
	ErrCopilotUnavailable = errors.New("intelligence: copilot is unavailable")
	ErrCopilotThrottled   = errors.New("intelligence: copilot was asked too soon for this topic")
	ErrNoCustomerMessage  = errors.New("intelligence: the topic has no customer message to answer")
)

// CopilotResult is a DRAFT for a person. Nothing here is ever sent: the copilot has no path to the message sender.
type CopilotResult struct {
	Suggestion        domain.CopilotSuggestion
	BasedOnMessages   int
	ConfirmedSummary  *int // version of the confirmed summary the model saw, if any
	AnsweredMessageID uuid.UUID
	Model             string
	PromptVersion     string
}

// CopilotService drafts a reply for the topic's latest customer message (ADR-0017 Wave 11). Suggest-only: the draft is
// returned to the attendant, who edits and sends it through the normal message path (which has its own permissions,
// audit and delivery rules). Every call is accounted in the usage ledger.
type CopilotService struct {
	builder  *ContextBuilder
	data     ports.ContextRepository
	models   *ModelRouter
	ledger   aiusage.Ledger
	flags    Flags
	now      func() time.Time
	MinEvery time.Duration

	mu   sync.Mutex
	last map[[2]uuid.UUID]time.Time
}

func NewCopilotService(builder *ContextBuilder, data ports.ContextRepository, models *ModelRouter, flags Flags) *CopilotService {
	return &CopilotService{builder: builder, data: data, models: models, flags: flags, now: time.Now, MinEvery: 4 * time.Second, last: map[[2]uuid.UUID]time.Time{}}
}

func (s *CopilotService) WithLedger(l aiusage.Ledger) *CopilotService {
	s.ledger = l
	return s
}

// allow is a small per-(tenant, topic) throttle so a double click or a script cannot turn the copilot into a cost loop.
func (s *CopilotService) allow(tenantID, topicID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.last) > 4096 {
		for k, t := range s.last {
			if now.Sub(t) > time.Minute {
				delete(s.last, k)
			}
		}
	}
	key := [2]uuid.UUID{tenantID, topicID}
	if t, ok := s.last[key]; ok && now.Sub(t) < s.MinEvery {
		return false
	}
	s.last[key] = now
	return true
}

func (s *CopilotService) account(ctx context.Context, tenantID uuid.UUID, route ModelRoute, resp aiports.GenerateResponse, topicID uuid.UUID, ok bool, reason string) {
	if s.ledger == nil {
		return
	}
	rec := aiusage.Record{TenantID: tenantID, Provider: route.Provider, Model: route.Model, Task: string(TaskCopilotReply), InputTokens: resp.InputTokens,
		OutputTokens: resp.OutputTokens, NoCost: true, Success: ok, Reason: reason, Ref: topicID}
	if rec.Provider == "" {
		rec.Provider = "unknown"
	}
	if err := s.ledger.Record(ctx, rec); err != nil {
		log.Printf("intelligence: cannot record copilot usage: %v", err)
	}
}

func (s *CopilotService) Suggest(ctx context.Context, topicID uuid.UUID) (*CopilotResult, error) {
	if !s.flags.CopilotEnabled {
		return nil, ErrCopilotDisabled
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("intelligence: tenant context required")
	}
	route, err := s.models.Route(TaskCopilotReply)
	if err != nil {
		return nil, ErrCopilotUnavailable
	}
	// the throttle comes after the topic is known to belong to the tenant (the builder returns not-found otherwise)
	recent, err := s.data.RecentTopicMessages(ctx, tc.TenantID, topicID, 10)
	if err != nil {
		return nil, err
	}
	var current *ports.ContextRow
	for i := range recent {
		if recent[i].Role == domain.RoleCustomer || recent[i].Role == domain.RoleParticipant {
			current = &recent[i]
			break
		}
	}
	ref := (*ports.MessageRef)(nil)
	if current != nil {
		ref = &ports.MessageRef{Kind: current.Kind, ID: current.ID}
	}
	in, err := s.builder.Build(ctx, topicID, ref) // not-found for a topic of another tenant
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, ErrNoCustomerMessage
	}
	if !s.allow(tc.TenantID, topicID) {
		return nil, ErrCopilotThrottled
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	rendered := in.Render(hex.EncodeToString(nonce))
	input := "DADOS CONFIÁVEIS DO SISTEMA\n" + rendered.Trusted + "\n" + rendered.Untrusted

	callCtx := ctx
	if route.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, route.Timeout)
		defer cancel()
	}
	resp, err := route.Generator.Generate(callCtx, aiports.GenerateRequest{Instructions: domain.CopilotInstructions, Input: input, MaxOutputTokens: route.MaxOutputTokens})
	if err != nil {
		s.account(ctx, tc.TenantID, route, resp, topicID, false, "provider_error")
		return nil, ErrCopilotUnavailable
	}
	sug, err := domain.ParseCopilotSuggestion(resp.OutputText)
	if err != nil {
		s.account(ctx, tc.TenantID, route, resp, topicID, false, "invalid_output")
		return nil, ErrCopilotUnavailable
	}
	sug.Warnings = domain.CheckSuggestion(sug.Reply, in.PlainText())
	s.account(ctx, tc.TenantID, route, resp, topicID, true, "")
	res := &CopilotResult{Suggestion: sug, BasedOnMessages: in.MessageCount, AnsweredMessageID: current.ID, Model: route.Model, PromptVersion: CopilotPromptVersion}
	if in.ConfirmedSummary != nil {
		v := in.ConfirmedSummary.Version
		res.ConfirmedSummary = &v
	}
	return res, nil
}
