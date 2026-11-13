package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	aiports "github.com/omnira/omnira/internal/ai/ports"
	"github.com/omnira/omnira/internal/aiusage"
	"github.com/omnira/omnira/internal/intelligence/contextdelta"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// WindowAnalyzerPromptVersion is stored with every analysis so a change of recipe is traceable (CONTEXT.2B).
const WindowAnalyzerPromptVersion = "context-window-analyze-v1"

const maxWindowTopicCandidates = 8

// WindowAnalyzerService is CONTEXT.2B's WindowAnalyzer: it turns one closed window's messages into typed,
// evidence-backed facts (need/observation/commitment/suggested_action/possible_resolution/risk). It never applies a
// fact — `possible_resolution` is a hypothesis for a person to confirm or dismiss, same as every other kind — and
// never links a message to anything stronger than DecisionAI (a human or deterministic decision always outranks it,
// enforced by TopicRepository.LinkMessage itself).
type WindowAnalyzerService struct {
	windows   ports.ContextWindowStore
	analysis  ports.ContextAnalysisRepository
	topics    ports.TopicRepository
	summaries ports.SummaryRepository
	models    *ModelRouter
	ledger    aiusage.Ledger // nil = calls are not accounted
	now       func() time.Time
}

func NewWindowAnalyzerService(windows ports.ContextWindowStore, analysis ports.ContextAnalysisRepository, topics ports.TopicRepository,
	summaries ports.SummaryRepository, models *ModelRouter) *WindowAnalyzerService {
	return &WindowAnalyzerService{windows: windows, analysis: analysis, topics: topics, summaries: summaries, models: models, now: time.Now}
}

// WithLedger makes every model call of the analyzer (successful or not) leave a row in the AI usage ledger.
func (s *WindowAnalyzerService) WithLedger(l aiusage.Ledger) *WindowAnalyzerService {
	s.ledger = l
	return s
}

func (s *WindowAnalyzerService) account(ctx context.Context, tenantID uuid.UUID, route ModelRoute, resp aiports.GenerateResponse, ref uuid.UUID, ok bool, reason string) {
	if s.ledger == nil {
		return
	}
	rec := aiusage.Record{TenantID: tenantID, Provider: route.Provider, Model: route.Model, Task: string(TaskContextWindowAnalyze), InputTokens: resp.InputTokens,
		OutputTokens: resp.OutputTokens, NoCost: true, Success: ok, Reason: reason, Ref: ref}
	if rec.Provider == "" {
		rec.Provider = "unknown"
	}
	if err := s.ledger.Record(ctx, rec); err != nil {
		log.Printf("intelligence: cannot record context window analysis usage: %v", err)
	}
}

var _ WindowAnalyzer = (*WindowAnalyzerService)(nil)

// Analyze runs inside the window's own tenant session (ContextWindowRunner.handle via WithSystemTenantSession). An
// error here is a TRANSIENT failure for the runner to retry/back off — it must never create a partial
// context_window_analyses row; only a definitive outcome (facts extracted, or genuinely nothing to analyze) is
// persisted, exactly like SummaryService only creates a TopicSummary row on success.
func (s *WindowAnalyzerService) Analyze(ctx context.Context, w ports.ContextWindow) error {
	// Crash recovery (Codex review round 2): if the worker died after Save committed but before the runner's
	// separate Complete call, the lease expires and this window gets reclaimed and re-handed to Analyze. Without
	// this check it would call the model again and then fail Save's UNIQUE(tenant_id, window_id) — recognize the
	// already-persisted receipt and let the runner's Complete finish the job instead.
	if exists, err := s.analysis.ExistsForWindow(ctx, w.TenantID, w.ID); err != nil {
		return err
	} else if exists {
		return nil
	}
	route, err := s.models.Route(TaskContextWindowAnalyze)
	if err != nil {
		return nil // no model configured: CONTEXT.2B simply does not run yet, same degrade-never-fail rule as the classifier
	}
	ids, err := s.windows.MessageIDs(ctx, w)
	if err != nil {
		return err
	}
	rows, err := s.analysis.WindowMessages(ctx, w.TenantID, w.ConversationID, ids)
	if err != nil {
		return err
	}
	msgs, msgByAlias := buildMessages(rows)
	if len(msgs) == 0 {
		_, err := s.analysis.Save(ctx, ports.AnalysisRecord{TenantID: w.TenantID, ConversationID: w.ConversationID, WindowID: w.ID, Status: "empty"})
		return err
	}

	items, err := s.topics.ListConversationTopics(ctx, w.TenantID, w.ConversationID)
	if err != nil {
		return err
	}
	candidates, topicByAlias := buildCandidates(ctx, s, w.TenantID, items)

	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	input, scope := contextdelta.BuildInput(hex.EncodeToString(nonce), msgs, candidates)

	callCtx := ctx
	if route.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, route.Timeout)
		defer cancel()
	}
	started := s.now()
	resp, err := route.Generator.Generate(callCtx, aiports.GenerateRequest{Instructions: contextdelta.Instructions, Input: input, MaxOutputTokens: route.MaxOutputTokens})
	latency := int(s.now().Sub(started) / time.Millisecond)
	analyzedIDs := make([]uuid.UUID, len(msgs))
	for i, m := range msgs {
		analyzedIDs[i] = msgByAlias[m.Alias]
	}
	if err != nil {
		s.account(ctx, w.TenantID, route, resp, w.ID, false, "provider_error")
		return err
	}
	proposal, err := contextdelta.Parse(resp.OutputText, scope)
	if err != nil {
		s.account(ctx, w.TenantID, route, resp, w.ID, false, "invalid_output")
		return err
	}
	s.account(ctx, w.TenantID, route, resp, w.ID, true, "")

	if len(proposal.Facts) == 0 {
		_, err := s.analysis.Save(ctx, ports.AnalysisRecord{
			TenantID: w.TenantID, ConversationID: w.ConversationID, WindowID: w.ID, Status: "empty", SummaryText: domain.SanitizeDerivedText(proposal.Summary),
			ModelProvider: route.Provider, ModelName: route.Model, PromptVersion: WindowAnalyzerPromptVersion,
			InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens, LatencyMS: latency, AnalyzedMessageIDs: analyzedIDs,
		})
		return err
	}

	// Every fact aliased "NEW" shares ONE freshly created topic (contextdelta offers a single NEW sentinel, not
	// NEW1/NEW2 — a model proposing two unrelated new subjects in the same window is a known, accepted limitation).
	var newTopicID *uuid.UUID
	resolved := make([]ports.ResolvedFact, 0, len(proposal.Facts))
	for _, f := range proposal.Facts {
		topicID, ok := topicByAlias[f.TopicAlias]
		if !ok {
			if newTopicID == nil {
				id, err := s.createTopicForNewFact(ctx, w.TenantID, w.ConversationID, proposal.Summary, f.Text)
				if err != nil {
					return err
				}
				newTopicID = &id
			}
			topicID = *newTopicID
		}
		evidence := make([]uuid.UUID, 0, len(f.Evidence))
		for _, alias := range f.Evidence {
			if id, ok := msgByAlias[alias]; ok {
				evidence = append(evidence, id)
			}
		}
		if len(evidence) == 0 {
			continue // Parse already requires >=1 evidence alias in scope; this only guards a future scope bug
		}
		resolved = append(resolved, ports.ResolvedFact{TopicThreadID: topicID, Kind: string(f.Kind), Text: f.Text, EvidenceMessageIDs: evidence})
		// Supporting evidence, never primary: a window-level grouping decision is weaker authority than per-message
		// routing. LinkMessage itself refuses to downgrade an existing stronger decision (ADR-0017).
		for _, mid := range evidence {
			link, err := domain.NewMessageTopicLink(w.TenantID, mid, topicID, domain.RelationSupporting, domain.DecisionAI, nil)
			if err != nil {
				return err
			}
			if err := s.topics.LinkMessage(ctx, link); err != nil {
				return err
			}
		}
	}

	_, err = s.analysis.Save(ctx, ports.AnalysisRecord{
		TenantID: w.TenantID, ConversationID: w.ConversationID, WindowID: w.ID, Status: "ok", SummaryText: domain.SanitizeDerivedText(proposal.Summary),
		ModelProvider: route.Provider, ModelName: route.Model, PromptVersion: WindowAnalyzerPromptVersion,
		InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens, LatencyMS: latency, AnalyzedMessageIDs: analyzedIDs, Facts: resolved,
	})
	return err
}

func (s *WindowAnalyzerService) createTopicForNewFact(ctx context.Context, tenantID, conversationID uuid.UUID, summary, factText string) (uuid.UUID, error) {
	title := domain.SanitizeDerivedText(summary)
	if title == "" {
		title = domain.SanitizeDerivedText(factText)
	}
	t, err := domain.NewTopicThread(tenantID, title, domain.SourceAI, s.now().UTC())
	if err != nil {
		return uuid.Nil, fmt.Errorf("context window analyzer: cannot title a topic proposed by the model: %w", err)
	}
	t.OriginConversationID = &conversationID
	if err := s.topics.CreateTopic(ctx, t); err != nil {
		return uuid.Nil, err
	}
	now := s.now().UTC()
	if err := s.topics.LinkConversation(ctx, &domain.TopicConversationLink{TenantID: tenantID, TopicThreadID: t.ID, ConversationID: conversationID,
		Relation: domain.ConversationActive, FirstActivityAt: now, LastActivityAt: now}); err != nil {
		return uuid.Nil, err
	}
	return t.ID, nil
}

// buildMessages keeps only messages with usable text (a pure-media message whose transcript/description is not
// done yet contributes nothing a fact could cite) and assigns M1..Mn in the given (already oldest-first) order.
func buildMessages(rows []ports.WindowMessageRow) ([]contextdelta.Message, map[string]uuid.UUID) {
	msgs := make([]contextdelta.Message, 0, len(rows))
	byAlias := make(map[string]uuid.UUID, len(rows))
	for _, r := range rows {
		if r.Text == "" {
			continue
		}
		alias := fmt.Sprintf("M%d", len(msgs)+1)
		role := "atendente"
		if r.Inbound {
			role = "cliente"
		}
		msgs = append(msgs, contextdelta.Message{Alias: alias, Role: role, Text: r.Text, At: r.At, FromMedia: r.FromMedia})
		byAlias[alias] = r.ID
	}
	return msgs, byAlias
}

func buildCandidates(ctx context.Context, s *WindowAnalyzerService, tenantID uuid.UUID, items []ports.TopicListItem) ([]contextdelta.TopicCandidate, map[string]uuid.UUID) {
	cands := make([]contextdelta.TopicCandidate, 0, maxWindowTopicCandidates)
	byAlias := make(map[string]uuid.UUID, maxWindowTopicCandidates)
	for _, it := range items {
		if it.Topic.Status != domain.TopicOpen || len(cands) >= maxWindowTopicCandidates {
			continue
		}
		alias := fmt.Sprintf("T%d", len(cands)+1)
		byAlias[alias] = it.Topic.ID
		cand := contextdelta.TopicCandidate{Alias: alias, Title: it.Topic.Title}
		if sum, err := s.summaries.LatestConfirmed(ctx, tenantID, it.Topic.ID); err == nil && sum != nil {
			cand.Summary = sum.SummaryText
		} else if sum, err := s.summaries.LatestInferred(ctx, tenantID, it.Topic.ID); err == nil && sum != nil {
			cand.Summary = sum.SummaryText
		}
		cands = append(cands, cand)
	}
	return cands, byAlias
}
