// Package application drives the inbound media pipeline (ADR-0016 M1):
//
//	pending -> fetch -> type check -> quarantine -> antivirus -> clean
//
// Every step fails closed: a file that was not positively cleared is never served.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/omnira/omnira/internal/media/domain"
	"github.com/omnira/omnira/internal/media/ports"
)

// Metrics is the minimal counter surface; the worker adapts it to Prometheus.
type Metrics interface {
	Inc(stage, outcome string)
}

type noMetrics struct{}

func (noMetrics) Inc(string, string) {}

type Config struct {
	BatchSize int
	// Lease keeps a claimed row away from other workers while it is processed.
	Lease time.Duration
	// MaxFetchAttempts bounds retries for a source that keeps failing (WAHA deletes its copy in minutes,
	// so a long tail of retries cannot help).
	MaxFetchAttempts int
	// MaxScanAttempts bounds retries while the antivirus is unavailable (about a day with the backoff below).
	MaxScanAttempts int
	// SourceGoneAfter: a 404 younger than this is retried (the file may still be landing), older is final.
	SourceGoneAfter time.Duration
	// Retention is how long a file is kept; the extracted text stays on the message (ADR-0016 decision 4).
	Retention time.Duration
}

func DefaultConfig() Config {
	return Config{BatchSize: 5, Lease: 90 * time.Second, MaxFetchAttempts: 8, MaxScanAttempts: 60, SourceGoneAfter: 45 * time.Second, Retention: 60 * 24 * time.Hour}
}

type Processor struct {
	repo    ports.Repository
	store   ports.Store
	fetcher ports.Fetcher
	scanner ports.Scanner
	cfg     Config
	metrics Metrics
	now     func() time.Time
}

func NewProcessor(repo ports.Repository, store ports.Store, fetcher ports.Fetcher, scanner ports.Scanner, cfg Config, m Metrics) (*Processor, error) {
	if repo == nil || store == nil || fetcher == nil || scanner == nil {
		return nil, errors.New("media processor: repository, store, fetcher and scanner are required")
	}
	if m == nil {
		m = noMetrics{}
	}
	return &Processor{repo: repo, store: store, fetcher: fetcher, scanner: scanner, cfg: cfg, metrics: m, now: time.Now}, nil
}

// Run polls until ctx is done. Polling (not a message queue) because WAHA's copy of the file lives only a few
// minutes: the row is created with the message, so there is no outbox hop to wait for.
func (p *Processor) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	sweep := time.NewTicker(time.Hour)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := p.ProcessOnce(ctx); err != nil {
				log.Printf("media: claim failed: %v", err)
			} else if n > 0 {
				log.Printf("media: processed %d item(s)", n)
			}
		case <-sweep.C:
			if n, err := p.PurgeExpired(ctx); err != nil {
				log.Printf("media: retention sweep failed: %v", err)
			} else if n > 0 {
				log.Printf("media: retention removed %d file(s)", n)
			}
		}
	}
}

// ProcessOnce claims a batch and handles each item; it returns how many were claimed.
func (p *Processor) ProcessOnce(ctx context.Context) (int, error) {
	items, err := p.repo.Claim(ctx, p.cfg.BatchSize, p.cfg.Lease)
	if err != nil {
		return 0, err
	}
	for _, w := range items {
		if ctx.Err() != nil {
			break
		}
		p.handle(ctx, w)
	}
	return len(items), nil
}

func (p *Processor) handle(ctx context.Context, w ports.Work) {
	// A panic on one hostile file must not take the worker (and every other tenant's media) down.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("media: panic on %s: %v", w.ID, r)
			p.terminal(ctx, w, ports.StatusFailed, "internal_error")
		}
	}()
	switch w.Status {
	case ports.StatusPending:
		p.fetchAndQuarantine(ctx, w)
	case ports.StatusQuarantined:
		p.scan(ctx, w)
	}
}

func (p *Processor) fetchAndQuarantine(ctx context.Context, w ports.Work) {
	data, declared, err := p.fetcher.Fetch(ctx, w.MediaRef)
	if err != nil {
		p.fetchFailed(ctx, w, err)
		return
	}
	res, err := domain.Classify(data, declared)
	if err != nil {
		if reason, ok := domain.IsRejection(err); ok {
			p.metrics.Inc("classify", "rejected")
			p.terminal(ctx, w, ports.StatusRejected, reason)
			return
		}
		p.terminal(ctx, w, ports.StatusFailed, "classify_error")
		return
	}
	sum := sha256.Sum256(data)
	if err := p.store.PutQuarantine(w, data); err != nil {
		log.Printf("media: cannot store %s in quarantine: %v", w.ID, err)
		p.retry(ctx, w, p.cfg.MaxFetchAttempts, "store_error")
		return
	}
	q := ports.Quarantined{Kind: string(res.Kind), Mime: res.Mime, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	if err := p.repo.MarkQuarantined(ctx, w, q); err != nil {
		_ = p.store.Remove(w)
		log.Printf("media: cannot record quarantine for %s: %v", w.ID, err)
		return
	}
	p.metrics.Inc("fetch", "quarantined")
	// Scan right away: the antivirus is usually up, and the file is already safe on disk if it is not.
	w.Status = ports.StatusQuarantined
	p.scanData(ctx, w, data)
}

func (p *Processor) fetchFailed(ctx context.Context, w ports.Work, err error) {
	if errors.Is(err, ports.ErrSourceGone) {
		// A very recent message may be hitting WAHA before its file lands: give it a few quick tries.
		if p.now().Sub(w.CreatedAt) < p.cfg.SourceGoneAfter && w.Attempts < p.cfg.MaxFetchAttempts {
			p.retry(ctx, w, p.cfg.MaxFetchAttempts, "source_not_ready")
			return
		}
		p.metrics.Inc("fetch", "source_gone")
		p.terminal(ctx, w, ports.StatusSourceGone, "provider_no_longer_has_the_file")
		return
	}
	log.Printf("media: fetch failed for %s (attempt %d): %v", w.ID, w.Attempts, err)
	p.retry(ctx, w, p.cfg.MaxFetchAttempts, "fetch_error")
}

func (p *Processor) scan(ctx context.Context, w ports.Work) {
	data, err := p.store.GetQuarantine(w)
	if err != nil {
		log.Printf("media: quarantined file for %s is unreadable: %v", w.ID, err)
		p.terminal(ctx, w, ports.StatusFailed, "quarantine_file_missing")
		return
	}
	p.scanData(ctx, w, data)
}

func (p *Processor) scanData(ctx context.Context, w ports.Work, data []byte) {
	verdict, err := p.scanner.Scan(ctx, data)
	if err != nil {
		// Antivirus down or confused: the file stays quarantined and is retried. It is never served unscanned.
		p.metrics.Inc("scan", "unavailable")
		log.Printf("media: antivirus unavailable for %s (attempt %d): %v", w.ID, w.Attempts, err)
		if w.Attempts >= p.cfg.MaxScanAttempts {
			p.terminal(ctx, w, ports.StatusFailed, "antivirus_unavailable")
			return
		}
		if rerr := p.repo.Retry(ctx, w, backoff(w.Attempts, 10*time.Second, 15*time.Minute), "antivirus_unavailable"); rerr != nil {
			log.Printf("media: cannot reschedule %s: %v", w.ID, rerr)
		}
		return
	}
	if verdict.Infected {
		p.metrics.Inc("scan", "infected")
		log.Printf("media: INFECTED %s tenant=%s signature=%s", w.ID, w.TenantID, verdict.Signature)
		p.terminal(ctx, w, ports.StatusInfected, verdict.Signature)
		return
	}
	if err := p.store.Promote(w); err != nil {
		log.Printf("media: cannot publish %s after a clean scan: %v", w.ID, err)
		p.retry(ctx, w, p.cfg.MaxFetchAttempts, "promote_error")
		return
	}
	if err := p.repo.MarkClean(ctx, w); err != nil {
		_ = p.store.Remove(w)
		log.Printf("media: cannot record clean verdict for %s: %v", w.ID, err)
		return
	}
	p.metrics.Inc("scan", "clean")
}

// terminal records a final status, audits it, and removes the bytes of anything that must not be kept.
func (p *Processor) terminal(ctx context.Context, w ports.Work, status ports.Status, reason string) {
	keepBytes := status == ports.StatusFailed && w.Status == ports.StatusQuarantined
	if !keepBytes {
		_ = p.store.Remove(w)
	}
	if err := p.repo.MarkTerminal(ctx, w, status, reason); err != nil {
		log.Printf("media: cannot record %s for %s: %v", status, w.ID, err)
	}
}

func (p *Processor) retry(ctx context.Context, w ports.Work, max int, reason string) {
	if w.Attempts >= max {
		p.terminal(ctx, w, ports.StatusFailed, reason)
		return
	}
	if err := p.repo.Retry(ctx, w, backoff(w.Attempts, 2*time.Second, time.Minute), reason); err != nil {
		log.Printf("media: cannot reschedule %s: %v", w.ID, err)
	}
}

// PurgeExpired removes files older than the retention window. The derived text stays with the message.
func (p *Processor) PurgeExpired(ctx context.Context) (int, error) {
	items, err := p.repo.ExpiredFiles(ctx, p.now().Add(-p.cfg.Retention), 200)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, w := range items {
		if err := p.store.Remove(w); err != nil {
			return n, fmt.Errorf("remove %s: %w", w.ID, err)
		}
		if err := p.repo.MarkPurged(ctx, w); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}
