package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/omnira/omnira/internal/aiusage"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// SummaryService owns the lifecycle of topic summaries: machine generation (idempotent), confirmation, correction.
// Versions are append-only; a person's words always outrank a machine's.
type SummaryService struct {
	topics     ports.TopicRepository
	summaries  ports.SummaryRepository
	data       ports.ContextRepository
	routing    ports.RoutingRepository
	builder    *ContextBuilder
	summarizer ports.TopicSummarizer // nil = no summarizer configured
	ledger     aiusage.Ledger        // nil = calls are not accounted
	flags      Flags
	// FirstSummaryAt and RegenerateEvery debounce the automatic step: a model is not called for every single message.
	FirstSummaryAt  int
	RegenerateEvery int
	now             func() time.Time
}

func NewSummaryService(topics ports.TopicRepository, summaries ports.SummaryRepository, data ports.ContextRepository, routing ports.RoutingRepository,
	summarizer ports.TopicSummarizer, flags Flags) *SummaryService {
	return &SummaryService{topics: topics, summaries: summaries, data: data, routing: routing, builder: NewContextBuilder(topics, data, summaries),
		summarizer: summarizer, flags: flags, FirstSummaryAt: 3, RegenerateEvery: 5, now: time.Now}
}

// WithLedger makes every model call of the service (successful or not) leave a row in the AI usage ledger.
func (s *SummaryService) WithLedger(l aiusage.Ledger) *SummaryService {
	s.ledger = l
	return s
}

func (s *SummaryService) account(ctx context.Context, tenantID uuid.UUID, res ports.SummaryResult, ref uuid.UUID, ok bool, reason string) {
	if s.ledger == nil {
		return
	}
	rec := aiusage.Record{TenantID: tenantID, Provider: res.Provider, Model: res.Model, Task: string(TaskTopicSummary), InputTokens: res.InputTokens,
		OutputTokens: res.OutputTokens, NoCost: true, Success: ok, Reason: reason, Ref: ref}
	if rec.Provider == "" {
		rec.Provider = "unknown"
	}
	if err := s.ledger.Record(ctx, rec); err != nil {
		log.Printf("intelligence: cannot record summary usage: %v", err)
	}
}

func (s *SummaryService) tenant(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("intelligence: tenant context required")
	}
	return tc, nil
}

func sourceCount(sum *domain.TopicSummary) int {
	if sum == nil {
		return -1
	}
	var c struct {
		N int `json:"source_message_count"`
	}
	if json.Unmarshal(sum.StructuredContext, &c) != nil {
		return -1
	}
	return c.N
}

// Generate creates the next machine summary of a topic from its current state. It is idempotent: when the topic has no
// new message since the latest summary, that summary is returned and nothing is created (created = false). The topic is
// locked for the duration, so two workers never generate twice for the same state.
func (s *SummaryService) Generate(ctx context.Context, topicID uuid.UUID) (summary *domain.TopicSummary, created bool, err error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, false, err
	}
	if s.summarizer == nil {
		return nil, false, ports.ErrSummarizerUnavailable
	}
	if _, err := s.topics.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return nil, false, err
	}
	if err := s.summaries.LockForGeneration(ctx, tc.TenantID, topicID); err != nil {
		return nil, false, err
	}
	stats, err := s.data.TopicStats(ctx, tc.TenantID, topicID)
	if err != nil {
		return nil, false, err
	}
	if stats.Messages == 0 {
		return nil, false, ErrNothingToSummarize
	}
	latest, err := s.summaries.Latest(ctx, tc.TenantID, topicID)
	if err != nil {
		return nil, false, err
	}
	if latest != nil && sourceCount(latest) == stats.Messages {
		return latest, false, nil
	}
	in, err := s.builder.Build(ctx, topicID, nil)
	if err != nil {
		return nil, false, err
	}
	res, err := s.summarizer.SummarizeTopic(ctx, in)
	if err != nil {
		s.account(ctx, tc.TenantID, res, topicID, false, "provider_error")
		return nil, false, err
	}
	text := domain.SanitizeDerivedText(res.Text)
	if text == "" {
		s.account(ctx, tc.TenantID, res, topicID, false, "empty_output")
		return nil, false, fmt.Errorf("%w: empty summary", ports.ErrSummarizerUnavailable)
	}
	meta := map[string]any{"source_message_count": stats.Messages, "truncated": in.Truncated}
	if stats.LastAt != nil {
		meta["source_last_message_at"] = stats.LastAt.UTC().Format(time.RFC3339)
	}
	structured, _ := json.Marshal(meta)
	row := &domain.TopicSummary{TenantID: tc.TenantID, TopicThreadID: topicID, SummaryText: text, StructuredContext: structured, Status: domain.SummaryAIInferred}
	if res.Provider != "" {
		row.ModelProvider = &res.Provider
	}
	if res.Model != "" {
		row.ModelName = &res.Model
	}
	if res.PromptVersion != "" {
		row.PromptVersion = &res.PromptVersion
	}
	if latest != nil {
		id := latest.ID
		row.SourceSummaryID = &id
	}
	created2, err := s.summaries.Create(ctx, row)
	if err != nil {
		return nil, false, err
	}
	s.account(ctx, tc.TenantID, res, created2.ID, true, "")
	if err := s.summaries.SupersedeInferredBefore(ctx, tc.TenantID, topicID, created2.Version); err != nil {
		return nil, false, err
	}
	return created2, true, nil
}

// MaybeGenerateForMessage runs after routing: for every topic the message belongs to, generate a summary when enough has
// accumulated. A failure is logged and swallowed: a summary problem must never block, fail or delay message processing.
func (s *SummaryService) MaybeGenerateForMessage(ctx context.Context, ref ports.MessageRef) {
	if s == nil || !s.flags.TopicSummariesEnabled || s.summarizer == nil {
		return
	}
	tc, err := s.tenant(ctx)
	if err != nil {
		return
	}
	topics, err := s.routing.TopicsOfMessage(ctx, tc.TenantID, ref)
	if err != nil {
		log.Printf("intelligence: summary step could not list topics: %v", err)
		return
	}
	for _, topicID := range topics {
		if !s.due(ctx, tc.TenantID, topicID) {
			continue
		}
		if _, _, err := s.Generate(ctx, topicID); err != nil && !errors.Is(err, ErrNothingToSummarize) {
			log.Printf("intelligence: topic summary skipped (%v)", err)
		}
	}
}

func (s *SummaryService) due(ctx context.Context, tenantID, topicID uuid.UUID) bool {
	stats, err := s.data.TopicStats(ctx, tenantID, topicID)
	if err != nil {
		return false
	}
	latest, err := s.summaries.Latest(ctx, tenantID, topicID)
	if err != nil {
		return false
	}
	if latest == nil {
		return stats.Messages >= s.FirstSummaryAt
	}
	prev := sourceCount(latest)
	return prev >= 0 && stats.Messages-prev >= s.RegenerateEvery
}

func (s *SummaryService) List(ctx context.Context, topicID uuid.UUID) ([]domain.TopicSummary, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.topics.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return nil, err
	}
	return s.summaries.List(ctx, tc.TenantID, topicID)
}

// Confirm records that an agent (or the customer) stands behind the CURRENT summary. A confirmation never lowers
// authority (customer > agent > machine) and a superseded version cannot be confirmed.
func (s *SummaryService) Confirm(ctx context.Context, topicID uuid.UUID, source domain.DecisionSource) (*domain.TopicSummary, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	var status domain.SummaryStatus
	switch source {
	case domain.DecisionAgent:
		status = domain.SummaryAgentConfirmed
	case domain.DecisionCustomer:
		status = domain.SummaryCustomerConfirmed
	default:
		return nil, fmt.Errorf("%w: only an agent or the customer confirms a summary", domain.ErrInvalidTopic)
	}
	if _, err := s.topics.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return nil, err
	}
	latest, err := s.summaries.Latest(ctx, tc.TenantID, topicID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, domain.ErrReferenceNotFound
	}
	if latest.Status == domain.SummarySuperseded || latest.Status == domain.SummaryCorrected {
		return nil, fmt.Errorf("%w: summary is %s", domain.ErrInvalidTransition, latest.Status)
	}
	if !domain.CanReplace(truthOfSummary(latest.Status), truthOfSummary(status)) {
		return nil, fmt.Errorf("%w: summary is already %s", domain.ErrInvalidTransition, latest.Status)
	}
	var by *uuid.UUID
	if tc.ActorID != uuid.Nil {
		a := tc.ActorID
		by = &a
	}
	ok, err := s.summaries.Confirm(ctx, tc.TenantID, latest.ID, status, by)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: summary can no longer be confirmed", domain.ErrInvalidTransition)
	}
	return s.summaries.Latest(ctx, tc.TenantID, topicID)
}

// Correct stores the agent's own text as a NEW version, marks the previous one superseded (it stays readable) and stands
// as the confirmed summary. Later machine summaries are context for it, never a replacement.
func (s *SummaryService) Correct(ctx context.Context, topicID uuid.UUID, text string) (*domain.TopicSummary, error) {
	tc, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	clean := domain.SanitizeDerivedText(text)
	if clean == "" {
		return nil, fmt.Errorf("%w: the corrected summary is empty", domain.ErrInvalidTopic)
	}
	if _, err := s.topics.GetTopic(ctx, tc.TenantID, topicID); err != nil {
		return nil, err
	}
	if err := s.summaries.LockForGeneration(ctx, tc.TenantID, topicID); err != nil {
		return nil, err
	}
	previous, err := s.summaries.Latest(ctx, tc.TenantID, topicID)
	if err != nil {
		return nil, err
	}
	stats, err := s.data.TopicStats(ctx, tc.TenantID, topicID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	meta, _ := json.Marshal(map[string]any{"source_message_count": stats.Messages, "authored_by": "agent"})
	row := &domain.TopicSummary{TenantID: tc.TenantID, TopicThreadID: topicID, SummaryText: clean, StructuredContext: meta, Status: domain.SummaryCorrected, ConfirmedAt: &now}
	if tc.ActorID != uuid.Nil {
		a := tc.ActorID
		row.CreatedByUserID = &a
	}
	if previous != nil {
		id := previous.ID
		row.SourceSummaryID = &id
	}
	created, err := s.summaries.Create(ctx, row)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		if err := s.summaries.Supersede(ctx, tc.TenantID, previous.ID); err != nil {
			return nil, err
		}
	}
	return created, nil
}

// SummaryPipeline runs the routing pipeline and then, as a best-effort extra step, the topic summary.
type SummaryPipeline struct {
	Next      Pipeline
	Summaries *SummaryService
}

func (p SummaryPipeline) Process(ctx context.Context, job ports.Job) error {
	if err := p.Next.Process(ctx, job); err != nil {
		return err
	}
	p.Summaries.MaybeGenerateForMessage(ctx, job.Ref)
	return nil
}
