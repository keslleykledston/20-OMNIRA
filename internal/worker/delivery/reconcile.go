package delivery

import (
	"context"
	"log"
	"time"
)

// ReconciliationStore is the persistence boundary for stranded-send
// reconciliation (PILOT.4D3-B). It never publishes to NATS and never calls a
// provider — it only recreates durable Postgres intent for the ordinary
// outbox publisher to pick up through its existing, unmodified pickup query.
type ReconciliationStore interface {
	// ReconcileStrandedQueuedSends finds up to batchSize outbound messages
	// that are still status='queued', whose most-recently-published
	// job.channel.send_text.v1 outbox intent is older than maxAge+grace, and
	// that have no currently-unpublished send intent — then inserts exactly
	// one new outbox_events row per candidate, atomically with the
	// eligibility re-check, using the message row as the locking/
	// serialization authority (FOR UPDATE SKIP LOCKED). Returns how many new
	// intents were created. Never touches messages.reserved_provider_message_id,
	// never modifies an existing outbox_events row.
	ReconcileStrandedQueuedSends(ctx context.Context, maxAge, grace time.Duration, batchSize int) (int, error)
}

// ReconcileInterval is how often the reconciler ticks while enabled. It only
// affects how quickly a stranded message is noticed AFTER it has already
// exceeded maxAge+grace — correctness never depends on a fast poll, since
// nothing is ever considered stranded before that boundary regardless of
// tick timing. 10 minutes is operationally modest (comparable in spirit to
// the routing liveness sweep's 60s, scaled up because this sweep's own
// eligibility window is measured in hours/days, not seconds).
const ReconcileInterval = 10 * time.Minute

// ReconcileBatchSize bounds one reconciliation pass, same convention as
// internal/worker/routing.SweepBatchSize.
const ReconcileBatchSize = 200

// Reconciler is the periodic driver, structurally parallel to
// internal/worker/routing.Sweep. It is deliberately inert whenever maxAge<=0
// — PILOT.4D3-B2 ships this mechanism disabled, because OMNIRA_JOBS' live
// MaxAge is still 0 (unbounded): a message can never legitimately be
// "stranded by retention" when nothing evicts it. PILOT.4D3-C is the slice
// that raises jobsstream.MaxAge above zero and, in the same change, makes
// this reconciler start doing real work.
type Reconciler struct {
	store ReconciliationStore
	// MaxAge/Grace/Interval/BatchSize are exported so tests can shrink them;
	// production wiring leaves MaxAge/Grace at the jobsstream package
	// constants and Interval/BatchSize at the package defaults above.
	MaxAge    time.Duration
	Grace     time.Duration
	Interval  time.Duration
	BatchSize int
}

func NewReconciler(store ReconciliationStore, maxAge, grace time.Duration) *Reconciler {
	return &Reconciler{store: store, MaxAge: maxAge, Grace: grace, Interval: ReconcileInterval, BatchSize: ReconcileBatchSize}
}

// Run starts the periodic loop. Callers should not invoke Run at all when
// MaxAge<=0 (see ShouldRun) — Run itself also refuses to do any per-tick
// work in that case, as defense in depth, but logs only once at start
// rather than every tick.
func (r *Reconciler) Run(ctx context.Context) {
	if !r.ShouldRun() {
		log.Printf("channel-send reconciliation: disabled (OMNIRA_JOBS MaxAge<=0, nothing to reconcile against)")
		return
	}
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Tick(ctx)
		}
	}
}

// ShouldRun reports whether reconciliation can safely do anything: a
// zero/negative MaxAge means OMNIRA_JOBS never expires a message, so no
// queued send can ever be legitimately "stranded by retention" — treating
// staleness as loss in that world would be exactly the flawed heuristic
// PILOT.4D3-B1 rejected.
func (r *Reconciler) ShouldRun() bool {
	return r.MaxAge > 0
}

// Tick drains every due batch before returning (same shape as
// internal/worker/routing.Sweep.Tick), so a burst of stranded messages does
// not wait a full Interval to catch up.
func (r *Reconciler) Tick(ctx context.Context) {
	if !r.ShouldRun() {
		return
	}
	for {
		n, err := r.store.ReconcileStrandedQueuedSends(ctx, r.MaxAge, r.Grace, r.BatchSize)
		if err != nil {
			log.Printf("channel-send reconciliation: batch failed: %v", err)
			return
		}
		if n > 0 {
			log.Printf("channel-send reconciliation: recreated %d stranded send intent(s)", n)
		}
		if n < r.BatchSize {
			return
		}
	}
}
