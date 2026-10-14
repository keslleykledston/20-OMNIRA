// Package headless runs the background work of the external API (ADR-0046 H4): numbering the journal, fanning facts out to webhook subscriptions, delivering
// them, expiring old facts and reporting how far behind each step is. Everything is idempotent and safe to run in several workers.
package headless

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/external/delivery"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
)

var (
	sequencerBacklog = opsmetrics.NewGauge("omnira_domain_events_unsequenced", "Facts of the external journal still waiting for a sequence number")
	sequencerLag     = opsmetrics.NewGauge("omnira_domain_events_oldest_unsequenced_seconds", "Age of the oldest fact still waiting for a sequence number (a long write transaction holds the horizon back)")
	webhookPending   = opsmetrics.NewGauge("omnira_external_webhook_pending", "Webhook deliveries waiting to be sent or retried")
	webhookDead      = opsmetrics.NewGauge("omnira_external_webhook_dead", "Webhook deliveries in the dead-letter list")
	webhookLag       = opsmetrics.NewGauge("omnira_external_webhook_oldest_due_seconds", "Age of the oldest webhook delivery that is due")
)

// Runner owns the loops.
type Runner struct {
	pool      *pgxpool.Pool
	delivery  *delivery.Worker
	retention time.Duration
}

func New(pool *pgxpool.Pool, d *delivery.Worker, retentionDays int) *Runner {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	return &Runner{pool: pool, delivery: d, retention: time.Duration(retentionDays) * 24 * time.Hour}
}

// SequenceOnce numbers what can be numbered now and returns how many facts it numbered.
func (r *Runner) SequenceOnce(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT domain_events_sequence(500)`).Scan(&n)
	return n, err
}

// PurgeOnce expires the facts older than the retention.
func (r *Runner) PurgeOnce(ctx context.Context) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx, `SELECT domain_events_purge(make_interval(secs => $1))`, r.retention.Seconds()).Scan(&n)
	return n, err
}

// ReportOnce refreshes the gauges.
func (r *Runner) ReportOnce(ctx context.Context) error {
	var waiting, pending, dead int64
	var oldest, due float64
	if err := r.pool.QueryRow(ctx, `SELECT waiting, oldest_seconds FROM domain_events_backlog()`).Scan(&waiting, &oldest); err != nil {
		return err
	}
	if err := r.pool.QueryRow(ctx, `SELECT pending, dead, oldest_due_seconds FROM webhook_backlog()`).Scan(&pending, &dead, &due); err != nil {
		return err
	}
	sequencerBacklog.Set(float64(waiting))
	sequencerLag.Set(oldest)
	webhookPending.Set(float64(pending))
	webhookDead.Set(float64(dead))
	webhookLag.Set(due)
	return nil
}

// Run blocks until the context ends.
func (r *Runner) Run(ctx context.Context) {
	go func() { // the sequencer: short interval, drains a backlog without sleeping
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			for {
				n, err := r.SequenceOnce(ctx)
				if err != nil || n < 500 {
					if err != nil && ctx.Err() == nil {
						log.Printf("headless: sequencer: %v", err)
					}
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if n, err := r.PurgeOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("headless: purge: %v", err)
			} else if n > 0 {
				log.Printf("headless: purged %d expired journal facts", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			_ = r.ReportOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	if r.delivery != nil {
		r.delivery.Run(ctx, time.Second)
		return
	}
	<-ctx.Done()
}
