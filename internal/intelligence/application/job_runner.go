package application

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// PipelineVersion identifies the processing recipe. A new version creates new jobs for new messages without touching
// the old ones (the job key includes it).
const PipelineVersion = "v1"

// ErrPermanent marks an error no retry can fix (the message is gone, the input is invalid): the job goes straight to dead.
var ErrPermanent = errors.New("intelligence: permanent pipeline error")

// Pipeline is what runs for one message. It must be idempotent: a job can be executed more than once (a crash after the
// work, before Complete), and the routing, links and summaries it creates must not duplicate.
type Pipeline interface {
	Process(ctx context.Context, job ports.Job) error
}

// TenantSession runs fn inside a system session scoped to the tenant (the tenant is read from stored state).
type TenantSession func(ctx context.Context, tenantID uuid.UUID, fn func(ctx context.Context) error) error

// JobMetrics is the minimal counter surface for the pipeline.
type JobMetrics interface {
	Job(state string)
}

type noJobMetrics struct{}

func (noJobMetrics) Job(string) {}

// RoutingPipeline is pipeline v1: route the message (flag-gated inside the routing service).
type RoutingPipeline struct{ Routing *RoutingService }

func (p RoutingPipeline) Process(ctx context.Context, job ports.Job) error {
	_, err := p.Routing.Route(ctx, job.Ref, RouteOptions{})
	switch {
	case err == nil, errors.Is(err, ErrNotRoutable):
		return nil // an outbound message has nothing to route
	case errors.Is(err, domain.ErrReferenceNotFound), errors.Is(err, domain.ErrInvalidTopic):
		return ErrPermanent
	}
	return err
}

type JobRunnerConfig struct {
	BatchSize   int
	Lease       time.Duration
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

func DefaultJobRunnerConfig() JobRunnerConfig {
	return JobRunnerConfig{BatchSize: 5, Lease: 2 * time.Minute, MaxAttempts: 8, BaseBackoff: 5 * time.Second, MaxBackoff: 10 * time.Minute}
}

// JobRunner claims due jobs and runs the pipeline for each inside its tenant session. Any number of runners may run at
// once (claims use SKIP LOCKED); a crashed one is recovered when its lease expires.
type JobRunner struct {
	jobs     ports.JobStore
	pipeline Pipeline
	session  TenantSession
	cfg      JobRunnerConfig
	metrics  JobMetrics
}

func NewJobRunner(jobs ports.JobStore, pipeline Pipeline, session TenantSession, cfg JobRunnerConfig, m JobMetrics) *JobRunner {
	if m == nil {
		m = noJobMetrics{}
	}
	return &JobRunner{jobs: jobs, pipeline: pipeline, session: session, cfg: cfg, metrics: m}
}

func (r *JobRunner) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := r.ProcessOnce(ctx); err != nil {
				log.Printf("intelligence: claim failed: %v", err)
			} else if n > 0 {
				log.Printf("intelligence: processed %d job(s)", n)
			}
		}
	}
}

// ProcessOnce claims a batch and handles each job; it returns how many were claimed.
func (r *JobRunner) ProcessOnce(ctx context.Context) (int, error) {
	jobs, err := r.jobs.Claim(ctx, r.cfg.BatchSize, r.cfg.Lease)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j ports.Job) {
			defer wg.Done()
			r.handle(ctx, j)
		}(j)
	}
	wg.Wait()
	return len(jobs), nil
}

func (r *JobRunner) handle(ctx context.Context, j ports.Job) {
	var err error
	func() {
		defer func() {
			if rec := recover(); rec != nil { // a panic on one message must not take the worker down
				log.Printf("intelligence: panic on job %s: %v", j.ID, rec)
				err = ErrPermanent
			}
		}()
		err = r.session(ctx, j.TenantID, func(scoped context.Context) error { return r.pipeline.Process(scoped, j) })
	}()
	switch {
	case err == nil:
		if ok, e := r.jobs.Complete(ctx, j); e != nil {
			log.Printf("intelligence: cannot complete %s: %v", j.ID, e)
		} else if ok {
			r.metrics.Job("completed")
		}
	case errors.Is(err, ErrPermanent):
		r.metrics.Job("dead")
		if _, e := r.jobs.Dead(ctx, j, "permanent"); e != nil {
			log.Printf("intelligence: cannot mark %s dead: %v", j.ID, e)
		}
	case j.Attempt >= r.cfg.MaxAttempts:
		r.metrics.Job("dead")
		if _, e := r.jobs.Dead(ctx, j, "retries_exhausted"); e != nil {
			log.Printf("intelligence: cannot mark %s dead: %v", j.ID, e)
		}
	default:
		r.metrics.Job("failed")
		log.Printf("intelligence: job %s attempt %d failed: %v", j.ID, j.Attempt, err)
		if _, e := r.jobs.Retry(ctx, j, "transient", r.backoff(j.Attempt)); e != nil {
			log.Printf("intelligence: cannot reschedule %s: %v", j.ID, e)
		}
	}
}

func (r *JobRunner) backoff(attempt int) time.Duration {
	d := r.cfg.BaseBackoff
	for i := 1; i < attempt && d < r.cfg.MaxBackoff; i++ {
		d *= 2
	}
	if d > r.cfg.MaxBackoff {
		d = r.cfg.MaxBackoff
	}
	return d
}
