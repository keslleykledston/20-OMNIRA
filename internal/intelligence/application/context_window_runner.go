package application

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/omnira/omnira/internal/aiusage"
	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// ErrWindowPermanent marks a window analysis error no retry can fix (the conversation is gone, the window is
// otherwise unusable): the window goes straight to dead instead of being retried.
var ErrWindowPermanent = errors.New("context intelligence: permanent window analysis error")

// WindowAnalyzer is CONTEXT.2B's extension point: turning one closed window's messages into structured context
// (topics, summaries, proposals). CONTEXT.2A ships only the scheduling around this interface — NoopWindowAnalyzer
// below exercises the whole claim/complete/retry/dead lifecycle without yet analyzing anything.
type WindowAnalyzer interface {
	Analyze(ctx context.Context, w ports.ContextWindow) error
}

// NoopWindowAnalyzer completes every window without reading it. CONTEXT.2A wiring only: never enable alongside a
// real CONTEXT.2B analyzer, and never in production — it exists so the storage/scheduling plumbing (consumer ->
// Touch -> Sweep -> Claim -> Complete, concurrency, leases, fencing) has something end-to-end to run and be tested
// against before CONTEXT.2B exists.
type NoopWindowAnalyzer struct{}

func (NoopWindowAnalyzer) Analyze(context.Context, ports.ContextWindow) error { return nil }

type ContextWindowRunnerConfig struct {
	Policy      contextwindow.Policy
	SweepLimit  int
	ClaimLimit  int
	Lease       time.Duration
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

func DefaultContextWindowRunnerConfig() ContextWindowRunnerConfig {
	return ContextWindowRunnerConfig{
		Policy: contextwindow.DefaultPolicy(), SweepLimit: 50, ClaimLimit: 5, Lease: 2 * time.Minute,
		MaxAttempts: 8, BaseBackoff: 5 * time.Second, MaxBackoff: 10 * time.Minute,
	}
}

// ContextWindowRunner sweeps idle/max-age windows to 'due', then claims and analyzes due windows. Any number of
// runners may run at once (both steps use SKIP LOCKED); a crashed one is recovered when its lease expires.
type ContextWindowRunner struct {
	store      ports.ContextWindowStore
	analyzer   WindowAnalyzer
	session    TenantSession
	cfg        ContextWindowRunnerConfig
	metrics    JobMetrics
	usageFlush func(ctx context.Context, items []aiusage.Record) error
}

func NewContextWindowRunner(store ports.ContextWindowStore, analyzer WindowAnalyzer, session TenantSession, cfg ContextWindowRunnerConfig, m JobMetrics) *ContextWindowRunner {
	if m == nil {
		m = noJobMetrics{}
	}
	return &ContextWindowRunner{store: store, analyzer: analyzer, session: session, cfg: cfg, metrics: m}
}

// WithUsageFlush makes the runner collect WindowAnalyzerService's AI-usage ledger writes on ctx (via
// WithPendingUsage) instead of letting them open their own nested session per analysis, and perform flush ITSELF
// right after releasing the window's own session — a fresh, independently acquired connection, never nested
// inside another one (Codex review round 5). Optional: a runner without this still works exactly as before
// (WindowAnalyzerService.WithLedger's direct-write path), just without the pool-exhaustion protection.
func (r *ContextWindowRunner) WithUsageFlush(flush func(ctx context.Context, items []aiusage.Record) error) *ContextWindowRunner {
	r.usageFlush = flush
	return r
}

func (r *ContextWindowRunner) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := r.ProcessOnce(ctx); err != nil {
				log.Printf("context window: process failed: %v", err)
			} else if n > 0 {
				log.Printf("context window: processed %d window(s)", n)
			}
		}
	}
}

// ProcessOnce sweeps due windows, then claims and analyzes a batch; it returns how many were claimed.
func (r *ContextWindowRunner) ProcessOnce(ctx context.Context) (int, error) {
	if _, err := r.store.Sweep(ctx, time.Now().UTC(), r.cfg.SweepLimit); err != nil {
		return 0, err
	}
	windows, err := r.store.Claim(ctx, r.cfg.ClaimLimit, r.cfg.Lease)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, w := range windows {
		wg.Add(1)
		go func(w ports.ContextWindow) {
			defer wg.Done()
			r.handle(ctx, w)
		}(w)
	}
	wg.Wait()
	return len(windows), nil
}

func (r *ContextWindowRunner) handle(ctx context.Context, w ports.ContextWindow) {
	var err error
	sessionCtx := ctx
	var pending *PendingUsage
	if r.usageFlush != nil {
		pending = &PendingUsage{}
		sessionCtx = WithPendingUsage(ctx, pending)
	}
	func() {
		defer func() {
			if rec := recover(); rec != nil { // a panic on one window must not take the worker down
				log.Printf("context window: panic on %s: %v", w.ID, rec)
				err = ErrWindowPermanent
			}
		}()
		err = r.session(sessionCtx, w.TenantID, func(scoped context.Context) error { return r.analyzer.Analyze(scoped, w) })
	}()
	// Outside r.session now: its connection is released. Only here is it safe to let WindowAnalyzerService's
	// collected ledger writes open their OWN connection without risking the nested-acquisition deadlock.
	if pending != nil {
		if items := pending.Drain(); len(items) > 0 {
			if ferr := r.usageFlush(ctx, items); ferr != nil {
				log.Printf("context window: cannot record AI usage for %s after releasing its session: %v", w.ID, ferr)
			}
		}
	}
	switch {
	case err == nil:
		if ok, e := r.store.Complete(ctx, w); e != nil {
			log.Printf("context window: cannot complete %s: %v", w.ID, e)
		} else if ok {
			r.metrics.Job("completed")
		}
	case errors.Is(err, ErrWindowPermanent):
		r.metrics.Job("dead")
		if _, e := r.store.Dead(ctx, w, "permanent"); e != nil {
			log.Printf("context window: cannot mark %s dead: %v", w.ID, e)
		}
	case w.Attempt >= r.cfg.MaxAttempts:
		r.metrics.Job("dead")
		if _, e := r.store.Dead(ctx, w, "retries_exhausted"); e != nil {
			log.Printf("context window: cannot mark %s dead: %v", w.ID, e)
		}
	default:
		r.metrics.Job("failed")
		log.Printf("context window: %s attempt %d failed: %v", w.ID, w.Attempt, err)
		if _, e := r.store.Retry(ctx, w, "transient", r.backoff(w.Attempt)); e != nil {
			log.Printf("context window: cannot reschedule %s: %v", w.ID, e)
		}
	}
}

func (r *ContextWindowRunner) backoff(attempt int) time.Duration {
	d := r.cfg.BaseBackoff
	for i := 1; i < attempt && d < r.cfg.MaxBackoff; i++ {
		d *= 2
	}
	if d > r.cfg.MaxBackoff {
		d = r.cfg.MaxBackoff
	}
	return d
}
