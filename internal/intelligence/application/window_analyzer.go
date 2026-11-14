package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	windows        ports.ContextWindowStore
	analysis       ports.ContextAnalysisRepository
	topics         ports.TopicRepository
	summaries      ports.SummaryRepository
	models         *ModelRouter
	ledger         aiusage.Ledger // nil = calls are not accounted
	mediaPipelines ports.MediaPipelines
	now            func() time.Time
}

func NewWindowAnalyzerService(windows ports.ContextWindowStore, analysis ports.ContextAnalysisRepository, topics ports.TopicRepository,
	summaries ports.SummaryRepository, models *ModelRouter) *WindowAnalyzerService {
	return &WindowAnalyzerService{windows: windows, analysis: analysis, topics: topics, summaries: summaries, models: models,
		mediaPipelines: ports.AllMediaPipelinesLive, now: time.Now}
}

// WithMediaPipelines tells the analyzer which of ADR-0016's two media pipelines are actually live right now
// (main.go's own OMNIRA_WHISPER_URL/OMNIRA_MULTIMODAL_ANALYSIS_ENABLED flags) — without it, every analyzer
// assumes both are live (Codex review round 7; see ports.MediaPipelines).
func (s *WindowAnalyzerService) WithMediaPipelines(p ports.MediaPipelines) *WindowAnalyzerService {
	s.mediaPipelines = p
	return s
}

// WithLedger makes every model call of the analyzer (successful or not) leave a row in the AI usage ledger.
func (s *WindowAnalyzerService) WithLedger(l aiusage.Ledger) *WindowAnalyzerService {
	s.ledger = l
	return s
}

// pendingUsageKey/PendingUsage: PostgresLedger.Record always opens its OWN pool connection — correct in general
// (a ledger write must survive even when the caller's own transaction later rolls back, e.g. on a provider
// failure: "accounted even on failure" is the whole point), but calling it while Analyze still holds the
// runner's OUTER tenant-session connection is a second, nested acquisition from the SAME pool. With up to
// ClaimLimit windows analyzed at once, that alone can exhaust a pool no bigger than the claim batch and deadlock
// the runner (Codex review round 5 — the same class of bug as MessageIDs, round 4). WithPendingUsage lets the
// RUNNER collect these records on ctx instead, and perform the actual Record calls itself AFTER its session has
// released the connection. Without one on ctx (any direct call to Analyze, outside the runner), account() falls
// back to writing immediately, exactly as before.
type pendingUsageKey struct{}

type PendingUsage struct {
	mu    sync.Mutex
	items []aiusage.Record
}

func WithPendingUsage(ctx context.Context, p *PendingUsage) context.Context {
	return context.WithValue(ctx, pendingUsageKey{}, p)
}

func (p *PendingUsage) add(r aiusage.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = append(p.items, r)
}

// Drain returns every collected record and clears the collector.
func (p *PendingUsage) Drain() []aiusage.Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	items := p.items
	p.items = nil
	return items
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
	if p, ok := ctx.Value(pendingUsageKey{}).(*PendingUsage); ok && p != nil {
		p.add(rec)
		return
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
	// Checked for the WHOLE window, before anything else — not only when it turns out to have zero usable text.
	// A window with a text message AND an audio message whose transcript/description is not done yet still has
	// pending media: finalizing it now (ok or empty, either is a terminal receipt) would permanently omit that
	// audio even once its analysis finishes, since nothing ever reopens a completed window. Returning an error
	// makes the runner retry/back off instead, giving the media pipeline time to finish (Codex review round 5 —
	// round 4's fix only covered the all-media, zero-usable-text case).
	if pending, perr := s.analysis.HasPendingMediaAnalysis(ctx, w.TenantID, ids, s.mediaPipelines); perr != nil {
		return perr
	} else if pending {
		return fmt.Errorf("context window analyzer: media analysis still pending for this window's messages")
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

// boundedTitle cuts s to at most maxRunes runes (never a byte-boundary split) and trims the cut edge — the
// summary/fact text a valid model response can return is bounded far looser (contextdelta allows up to 2,000
// characters) than domain.MaxTitleRunes accepts for a topic title (Codex review round 3).
func boundedTitle(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:maxRunes]))
}

func (s *WindowAnalyzerService) createTopicForNewFact(ctx context.Context, tenantID, conversationID uuid.UUID, summary, factText string) (uuid.UUID, error) {
	title := boundedTitle(domain.SanitizeDerivedText(summary), domain.MaxTitleRunes)
	if title == "" {
		title = boundedTitle(domain.SanitizeDerivedText(factText), domain.MaxTitleRunes)
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
