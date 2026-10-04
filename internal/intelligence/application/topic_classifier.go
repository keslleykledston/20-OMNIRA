package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/omnira/omnira/internal/aiusage"
	"log"
	"time"

	"github.com/google/uuid"

	aiports "github.com/omnira/omnira/internal/ai/ports"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ClassifierPromptVersion is stored with every proposal so a change of recipe is traceable.
const ClassifierPromptVersion = "topic-classify-v1"

const maxClassifyCandidates = 8

// ClassifierMetrics is the counter surface of the AI shadow step.
type ClassifierMetrics interface {
	Shadow(outcome string)
}

type noClassifierMetrics struct{}

func (noClassifierMetrics) Shadow(string) {}

// TopicClassifier is the AI shadow classifier (ADR-0017 Wave 6). It PROPOSES a topic for a message and records the proposal
// (decision_source=ai, applied=false). It never links a message, never creates a topic or an ambiguity, and a provider
// failure only means "no proposal": the deterministic router and the people stay in charge.
type TopicClassifier struct {
	routing   ports.RoutingRepository
	data      ports.ContextRepository
	summaries ports.SummaryRepository
	models    *ModelRouter
	flags     Flags
	metrics   ClassifierMetrics
	ledger    aiusage.Ledger // nil = calls are not accounted
	now       func() time.Time
}

// WithLedger makes every model call of the classifier (successful or not) leave a row in the AI usage ledger.
func (c *TopicClassifier) WithLedger(l aiusage.Ledger) *TopicClassifier {
	c.ledger = l
	return c
}

func (c *TopicClassifier) account(ctx context.Context, tenantID uuid.UUID, route ModelRoute, resp aiports.GenerateResponse, ref uuid.UUID, ok bool, reason string) {
	if c.ledger == nil {
		return
	}
	rec := aiusage.Record{TenantID: tenantID, Provider: route.Provider, Model: route.Model, Task: string(TaskTopicClassify), InputTokens: resp.InputTokens,
		OutputTokens: resp.OutputTokens, NoCost: true, Success: ok, Reason: reason, Ref: ref}
	if rec.Provider == "" {
		rec.Provider = "unknown"
	}
	if err := c.ledger.Record(ctx, rec); err != nil {
		log.Printf("intelligence: cannot record classification usage: %v", err)
	}
}

func NewTopicClassifier(routing ports.RoutingRepository, data ports.ContextRepository, summaries ports.SummaryRepository, models *ModelRouter, flags Flags, m ClassifierMetrics) *TopicClassifier {
	if m == nil {
		m = noClassifierMetrics{}
	}
	return &TopicClassifier{routing: routing, data: data, summaries: summaries, models: models, flags: flags, metrics: m, now: time.Now}
}

// ClassifyShadow runs once per message (idempotent): a replayed job never calls the provider again for a message that
// already has an AI proposal. It returns the stored decision id, or uuid.Nil when nothing was recorded.
func (c *TopicClassifier) ClassifyShadow(ctx context.Context, ref ports.MessageRef) (uuid.UUID, error) {
	if !c.flags.TopicAIRoutingEnabled {
		return uuid.Nil, nil
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("intelligence: tenant context required")
	}
	route, err := c.models.Route(TaskTopicClassify)
	if err != nil {
		c.metrics.Shadow("no_model")
		return uuid.Nil, nil
	}
	msg, err := c.routing.LoadRoutable(ctx, tc.TenantID, ref)
	if err != nil {
		return uuid.Nil, err
	}
	if !msg.Inbound || msg.Text == "" {
		return uuid.Nil, nil
	}
	if done, err := c.routing.ProposalExists(ctx, tc.TenantID, ref, domain.DecisionAI); err != nil {
		return uuid.Nil, err
	} else if done {
		c.metrics.Shadow("replay")
		return uuid.Nil, nil
	}
	briefs, err := c.routing.OpenTopics(ctx, tc.TenantID, ref, msg.ContainerID, maxClassifyCandidates)
	if err != nil {
		return uuid.Nil, err
	}
	cands := make([]domain.ClassifyCandidate, 0, len(briefs))
	byAlias := map[string]uuid.UUID{}
	for i, b := range briefs {
		alias := fmt.Sprintf("T%d", i+1)
		byAlias[alias] = b.ID
		cand := domain.ClassifyCandidate{Alias: alias, Title: b.Title}
		if ents, err := c.data.TopicEntities(ctx, tc.TenantID, b.ID); err == nil {
			for _, e := range ents {
				cand.Entities = append(cand.Entities, string(e.Type)+" "+e.Key)
			}
		}
		if s, err := c.summaries.LatestConfirmed(ctx, tc.TenantID, b.ID); err == nil && s != nil {
			cand.Summary = s.SummaryText
		} else if s, err := c.summaries.LatestInferred(ctx, tc.TenantID, b.ID); err == nil && s != nil {
			cand.Summary = s.SummaryText
		}
		cands = append(cands, cand)
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return uuid.Nil, err
	}
	input, aliases := domain.BuildClassificationInput(hex.EncodeToString(nonce), msg.Text, cands)

	callCtx := ctx
	if route.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, route.Timeout)
		defer cancel()
	}
	started := c.now()
	resp, err := route.Generator.Generate(callCtx, aiports.GenerateRequest{Instructions: domain.ClassificationInstructions, Input: input, MaxOutputTokens: route.MaxOutputTokens})
	latency := int(c.now().Sub(started) / time.Millisecond)
	if err != nil {
		c.account(ctx, tc.TenantID, route, resp, ref.ID, false, "provider_error")
		c.metrics.Shadow("provider_error")
		log.Printf("intelligence: topic classification skipped (%v)", err)
		return uuid.Nil, nil
	}
	cl, err := domain.ParseClassification(resp.OutputText, aliases)
	if err != nil {
		c.account(ctx, tc.TenantID, route, resp, ref.ID, false, "invalid_output")
		c.metrics.Shadow("invalid_output")
		log.Printf("intelligence: topic classification discarded (%v)", err)
		return uuid.Nil, nil
	}

	rec := ports.DecisionRecord{Ref: ref, Source: domain.DecisionAI, Applied: false, Confidence: &cl.Confidence,
		ModelProvider: strPtr(route.Provider), ModelName: strPtr(route.Model), PromptVersion: strPtr(ClassifierPromptVersion), LatencyMS: &latency, InputTokens: intPtr(resp.InputTokens), OutputTokens: intPtr(resp.OutputTokens)}
	switch cl.Verdict {
	case domain.VerdictExisting:
		id := byAlias[cl.TopicAlias]
		rec.Status, rec.Selected = domain.RoutingAssigned, &id
	case domain.VerdictNew:
		rec.Status = domain.RoutingNewTopic
	default:
		rec.Status = domain.RoutingUnassigned
	}
	rec.Signals, _ = json.Marshal(map[string]any{"verdict": cl.Verdict, "reason": cl.Reason, "candidates": len(cands), "mode": "shadow"})
	id, _, err := c.routing.SaveDecision(ctx, tc.TenantID, rec)
	if err != nil {
		return uuid.Nil, err
	}
	c.account(ctx, tc.TenantID, route, resp, id, true, "")
	c.metrics.Shadow(string(cl.Verdict))
	return id, nil
}

func intPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ShadowPipeline runs the AI shadow classification after the deterministic pipeline, as a best-effort extra step: a
// problem here never fails, delays or reverts the routing of the message.
type ShadowPipeline struct {
	Next       Pipeline
	Classifier *TopicClassifier
}

func (p ShadowPipeline) Process(ctx context.Context, job ports.Job) error {
	if err := p.Next.Process(ctx, job); err != nil {
		return err
	}
	if _, err := p.Classifier.ClassifyShadow(ctx, job.Ref); err != nil {
		log.Printf("intelligence: ai shadow step failed (%v)", err)
	}
	return nil
}
