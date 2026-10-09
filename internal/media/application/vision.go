package application

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/omnira/omnira/internal/media/domain"
	"github.com/omnira/omnira/internal/media/ports"
)

// VisionProcessor reads cleared images and PDFs with the tenant's own Gemini key (ADR-0016, Wave 9). It exists to
// extend the same pipeline audio already uses: the same analysis table, the same cleared-files-only rule, the same
// untrusted-text handling. What is different is that the data leaves the server, so every gate is explicit:
//   - the tenant must have opted in (an enabled integration, which requires a key and a recorded consent);
//   - only cleared files of an allow-listed mime type are sent;
//   - the monthly budget is checked BEFORE each call, against the worst case of that call;
//   - every call is recorded in the usage ledger, success or not.
type VisionProcessor struct {
	repo     ports.AnalysisRepository
	vision   ports.VisionRepository
	files    ports.CleanFiles
	tenants  ports.TenantAIResolver
	analyzer ports.VisionAnalyzer
	ledger   ports.UsageLedger
	metrics  Metrics
	now      func() time.Time

	MaxAttempts int
	// Lookback bounds how old a cleared file may be when its tenant opts in (retention keeps files for a while).
	Lookback time.Duration
	// EnqueueBatch is how many new jobs one tick may create.
	EnqueueBatch int
}

func NewVisionProcessor(repo ports.AnalysisRepository, vision ports.VisionRepository, files ports.CleanFiles, tenants ports.TenantAIResolver,
	analyzer ports.VisionAnalyzer, ledger ports.UsageLedger, m Metrics) (*VisionProcessor, error) {
	if repo == nil || vision == nil || files == nil || tenants == nil || analyzer == nil || ledger == nil {
		return nil, errors.New("vision: repository, files, tenant resolver, analyzer and usage ledger are required")
	}
	if m == nil {
		m = noMetrics{}
	}
	return &VisionProcessor{repo: repo, vision: vision, files: files, tenants: tenants, analyzer: analyzer, ledger: ledger, metrics: m, now: time.Now,
		MaxAttempts: 8, Lookback: 7 * 24 * time.Hour, EnqueueBatch: 20}, nil
}

func (p *VisionProcessor) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := p.ProcessOnce(ctx); err != nil {
				log.Printf("vision: tick failed: %v", err)
			} else if n > 0 {
				log.Printf("vision: processed %d item(s)", n)
			}
		}
	}
}

// ProcessOnce enqueues what became eligible, then handles at most one job per kind (serial on purpose: cost and rate).
func (p *VisionProcessor) ProcessOnce(ctx context.Context) (int, error) {
	if _, err := p.vision.EnqueueVision(ctx, p.now().Add(-p.Lookback), p.EnqueueBatch); err != nil {
		return 0, err
	}
	total := 0
	for _, kind := range []string{"description", "document_text"} {
		items, err := p.repo.ClaimAnalysis(ctx, kind, 1, 3*time.Minute)
		if err != nil {
			return total, err
		}
		for _, w := range items {
			p.handle(ctx, w)
		}
		total += len(items)
	}
	return total, nil
}

func (p *VisionProcessor) skip(ctx context.Context, w ports.AnalysisWork, reason string) {
	p.metrics.Inc("vision", reason)
	if err := p.vision.SkipAnalysis(ctx, w, reason); err != nil {
		log.Printf("vision: cannot record skip for %s: %v", w.ID, err)
	}
}

func (p *VisionProcessor) handle(ctx context.Context, w ports.AnalysisWork) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("vision: panic on %s: %v", w.ID, r)
			_ = p.repo.FailAnalysis(ctx, w, "internal_error")
		}
	}()
	if !serving(ctx, p.repo, w.TenantID) { // ADR-0038: nothing leaves the server for a suspended company
		p.metrics.Inc("vision", "company_suspended")
		return
	}
	// 1. the tenant must still be opted in (it may have switched the integration off since the job was created)
	cfg, err := p.tenants.Resolve(ctx, w.TenantID)
	if err != nil {
		p.retry(ctx, w, "config_unavailable")
		return
	}
	if cfg == nil {
		p.skip(ctx, w, "ai_not_enabled")
		return
	}
	// 2. only allow-listed, cleared files leave the server
	if !domain.VisionMimeAllowed(w.Mime) || domain.VisionKindFor(w.Mime) != w.Kind {
		p.skip(ctx, w, "mime_not_allowed")
		return
	}
	// 3. the budget, against the worst case of THIS call
	spent, err := p.ledger.SpentThisMonth(ctx, w.TenantID, p.now())
	if err != nil {
		p.retry(ctx, w, "ledger_unavailable")
		return
	}
	if spent+domain.WorstCaseCostUSD(cfg.Model, 4000) > cfg.BudgetUSD {
		p.skip(ctx, w, "budget_exceeded")
		return
	}
	data, err := p.files.ReadClean(w.TenantID, w.MediaID, domain.MaxBytes)
	if err != nil {
		p.metrics.Inc("vision", "file_missing")
		_ = p.repo.FailAnalysis(ctx, w, "file_missing")
		return
	}
	a, usage, err := p.analyzer.Analyze(ctx, cfg.APIKey, cfg.Model, ports.VisionInput{Data: data, Mime: w.Mime, Kind: w.Kind})
	record := ports.UsageRecord{TenantID: w.TenantID, Provider: "gemini", Model: cfg.Model, Task: w.Kind, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CostUSD: domain.EstimateCostUSD(cfg.Model, usage.InputTokens, usage.OutputTokens), Ref: w.ID}
	switch {
	case errors.Is(err, ports.ErrNothingReadable):
		record.Success = true
		p.account(ctx, record)
		p.metrics.Inc("vision", "empty")
		_ = p.repo.SaveAnalysisEmpty(ctx, w, "nothing_readable")
	case errors.Is(err, ports.ErrProviderRejected):
		record.Reason = "rejected"
		p.account(ctx, record)
		p.metrics.Inc("vision", "rejected")
		_ = p.repo.FailAnalysis(ctx, w, "provider_rejected")
	case err != nil:
		record.Reason = "transient"
		p.account(ctx, record)
		p.metrics.Inc("vision", "provider_error")
		p.retry(ctx, w, "provider_unavailable")
	default:
		record.Success = true
		p.account(ctx, record)
		a.Text = domain.SanitizeDerivedText(a.Text)
		if a.Text == "" {
			p.metrics.Inc("vision", "empty")
			_ = p.repo.SaveAnalysisEmpty(ctx, w, "nothing_readable")
			return
		}
		// text read from an image is exactly where a hidden instruction would sit: flag it, never obey it
		a.Suspicious = a.Suspicious || domain.LooksLikeInstruction(a.Text)
		if !serving(ctx, p.repo, w.TenantID) { // suspended while the provider was answering: the result is not stored
			p.metrics.Inc("vision", "company_suspended")
			return
		}
		if e := p.repo.SaveAnalysis(ctx, w, a); e != nil {
			log.Printf("vision: cannot save %s: %v", w.ID, e)
			return
		}
		p.metrics.Inc("vision", "done")
		if a.Suspicious {
			p.metrics.Inc("vision", "suspicious")
		}
	}
}

func (p *VisionProcessor) account(ctx context.Context, u ports.UsageRecord) {
	if err := p.ledger.Record(ctx, u); err != nil {
		log.Printf("vision: cannot record usage for %s: %v", u.Ref, err)
	}
}

func (p *VisionProcessor) retry(ctx context.Context, w ports.AnalysisWork, reason string) {
	if w.Attempts >= p.MaxAttempts {
		_ = p.repo.FailAnalysis(ctx, w, reason)
		return
	}
	if e := p.repo.RetryAnalysis(ctx, w, backoff(w.Attempts, 30*time.Second, 2*time.Hour), reason); e != nil {
		log.Printf("vision: cannot reschedule %s: %v", w.ID, e)
	}
}
