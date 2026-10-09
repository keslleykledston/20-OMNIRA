package application

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/omnira/omnira/internal/media/domain"
	"github.com/omnira/omnira/internal/media/ports"
)

// TranscriptionProcessor turns cleared audio into text with the local engine (ADR-0016 M2). It only ever reads
// files from the clean area, handles one item at a time (the GPU is shared with other services), and treats the
// engine as unreliable: unreachable means retry with backoff, never "failed" on the first error.
type TranscriptionProcessor struct {
	repo    ports.AnalysisRepository
	files   ports.CleanFiles
	engine  ports.Transcriber
	metrics Metrics
	// MaxAttempts bounds retries while the engine is down (about a day with the backoff below).
	MaxAttempts int
}

func NewTranscriptionProcessor(repo ports.AnalysisRepository, files ports.CleanFiles, engine ports.Transcriber, m Metrics) (*TranscriptionProcessor, error) {
	if repo == nil || files == nil || engine == nil {
		return nil, errors.New("transcription: repository, files and engine are required")
	}
	if m == nil {
		m = noMetrics{}
	}
	return &TranscriptionProcessor{repo: repo, files: files, engine: engine, metrics: m, MaxAttempts: 30}, nil
}

func (p *TranscriptionProcessor) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := p.ProcessOnce(ctx); err != nil {
				log.Printf("transcription: claim failed: %v", err)
			} else if n > 0 {
				log.Printf("transcription: processed %d item(s)", n)
			}
		}
	}
}

// ProcessOnce claims one job (serial on purpose) and handles it.
func (p *TranscriptionProcessor) ProcessOnce(ctx context.Context) (int, error) {
	items, err := p.repo.ClaimAnalysis(ctx, "transcript", 1, 6*time.Minute)
	if err != nil {
		return 0, err
	}
	for _, w := range items {
		p.handle(ctx, w)
	}
	return len(items), nil
}

func (p *TranscriptionProcessor) handle(ctx context.Context, w ports.AnalysisWork) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("transcription: panic on %s: %v", w.ID, r)
			_ = p.repo.FailAnalysis(ctx, w, "internal_error")
		}
	}()
	if !serving(ctx, p.repo, w.TenantID) { // ADR-0038: a suspended company's audio is not transcribed
		p.metrics.Inc("transcribe", "company_suspended")
		return
	}
	data, err := p.files.ReadClean(w.TenantID, w.MediaID, domain.MaxBytes)
	if err != nil {
		// The file is gone (retention, manual cleanup) or never was: no amount of retrying changes that.
		p.metrics.Inc("transcribe", "file_missing")
		_ = p.repo.FailAnalysis(ctx, w, "file_missing")
		return
	}
	a, err := p.engine.Transcribe(ctx, data, w.Mime)
	switch {
	case errors.Is(err, ports.ErrNoSpeech):
		p.metrics.Inc("transcribe", "empty")
		if e := p.repo.SaveAnalysisEmpty(ctx, w, "no_speech"); e != nil {
			log.Printf("transcription: cannot record empty for %s: %v", w.ID, e)
		}
	case err != nil:
		p.metrics.Inc("transcribe", "engine_error")
		log.Printf("transcription: engine error for %s (attempt %d): %v", w.ID, w.Attempts, err)
		if w.Attempts >= p.MaxAttempts {
			_ = p.repo.FailAnalysis(ctx, w, "engine_unavailable")
			return
		}
		if e := p.repo.RetryAnalysis(ctx, w, backoff(w.Attempts, 10*time.Second, 30*time.Minute), "engine_unavailable"); e != nil {
			log.Printf("transcription: cannot reschedule %s: %v", w.ID, e)
		}
	default:
		// Defence in depth: whatever the engine returned is cleaned again before it is stored.
		a.Text = domain.SanitizeDerivedText(a.Text)
		if a.Text == "" {
			p.metrics.Inc("transcribe", "empty")
			_ = p.repo.SaveAnalysisEmpty(ctx, w, "no_speech")
			return
		}
		a.Suspicious = a.Suspicious || domain.LooksLikeInstruction(a.Text)
		if !serving(ctx, p.repo, w.TenantID) { // suspended while the engine was running: the result is not stored
			p.metrics.Inc("transcribe", "company_suspended")
			return
		}
		if e := p.repo.SaveAnalysis(ctx, w, a); e != nil {
			log.Printf("transcription: cannot save %s: %v", w.ID, e)
			return
		}
		p.metrics.Inc("transcribe", "done")
		if a.Suspicious {
			p.metrics.Inc("transcribe", "suspicious")
		}
	}
}
