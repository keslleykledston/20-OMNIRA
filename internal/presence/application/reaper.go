package application

import (
	"context"
	"log"
	"time"

	presencedomain "github.com/omnira/omnira/internal/presence/domain"
	"github.com/omnira/omnira/internal/presence/ports"
)

// Reaper is the deterministic expiry process ADR-0010 requires: presence
// offline transitions must not depend on Valkey/Redis keyspace-notification
// delivery, which is best-effort, can be disabled at the server config level,
// and is silently lost on redeploy. Polling a bounded, score-sorted expiry
// index (Store.ExpireBatch) is the primary mechanism. Keyspace notifications,
// if ever added later, would only be an optimization layered on top of this
// loop — never a replacement for it.
type Reaper struct {
	store     ports.Store
	publisher ports.TransitionPublisher
	lastSeen  ports.LastSeenWriter
	Interval  time.Duration
	BatchSize int
	now       func() time.Time
}

func NewReaper(store ports.Store, publisher ports.TransitionPublisher, lastSeen ports.LastSeenWriter) *Reaper {
	return &Reaper{
		store:     store,
		publisher: publisher,
		lastSeen:  lastSeen,
		Interval:  30 * time.Second,
		BatchSize: 500,
		now:       time.Now,
	}
}

// Run polls until ctx is cancelled. Interval is coherent with the 120s TTL:
// a session can be seen as expired up to one Interval late, never more.
func (r *Reaper) Run(ctx context.Context) {
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

// Tick processes one or more bounded batches, draining fully before the next
// ticker fire so a burst of expirations does not queue up for a whole
// Interval. Exported for tests and for a manual/administrative sweep.
func (r *Reaper) Tick(ctx context.Context) {
	for {
		offline, processed, err := r.store.ExpireBatch(ctx, r.now(), r.BatchSize)
		if err != nil {
			log.Printf("presence reaper: expire batch failed: %v", err)
			return
		}
		for _, ref := range offline {
			if r.lastSeen != nil {
				r.lastSeen.MarkSeen(ref.TenantID, ref.AgentProfileID, r.now())
			}
			if r.publisher != nil {
				if err := r.publisher.PublishTransition(ctx, ref.TenantID, ref.AgentProfileID, presencedomain.StatusOffline); err != nil {
					log.Printf("presence reaper: publish offline failed: %v", err)
				}
			}
		}
		if processed < r.BatchSize {
			return
		}
	}
}
