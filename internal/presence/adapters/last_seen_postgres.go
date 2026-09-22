package adapters

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/presence/ports"
)

type lastSeenEntry struct {
	tenantID uuid.UUID
	at       time.Time
}

// CoalescedLastSeenWriter batches last_seen_at updates so a heartbeat never
// causes a Postgres write (ADR-0010 §9-10). MarkSeen only records intent in
// memory; FlushNow — driven by a periodic loop, and also called once on every
// offline transition for a final accurate value — performs at most one
// UPDATE per dirty agent per flush.
type CoalescedLastSeenWriter struct {
	pool *pgxpool.Pool

	mu    sync.Mutex
	dirty map[uuid.UUID]lastSeenEntry
}

func NewCoalescedLastSeenWriter(pool *pgxpool.Pool) *CoalescedLastSeenWriter {
	return &CoalescedLastSeenWriter{pool: pool, dirty: make(map[uuid.UUID]lastSeenEntry)}
}

var _ ports.LastSeenWriter = (*CoalescedLastSeenWriter)(nil)

func (w *CoalescedLastSeenWriter) MarkSeen(tenantID, agentProfileID uuid.UUID, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if existing, ok := w.dirty[agentProfileID]; !ok || at.After(existing.at) {
		w.dirty[agentProfileID] = lastSeenEntry{tenantID: tenantID, at: at}
	}
}

// FlushNow writes every pending entry in one system-actor transaction (same
// uuid.Nil/is_system_admin=true pattern the routing worker already uses to
// write tenant-owned tables from a background process, see
// internal/worker/routing/postgres.go), then clears the dirty set.
func (w *CoalescedLastSeenWriter) FlushNow(ctx context.Context) error {
	w.mu.Lock()
	batch := w.dirty
	w.dirty = make(map[uuid.UUID]lastSeenEntry)
	w.mu.Unlock()

	if len(batch) == 0 || w.pool == nil {
		return nil
	}
	return platformdb.WithTenantSession(ctx, w.pool, uuid.Nil, true, func(sctx context.Context) error {
		q := platformdb.QuerierFromContext(sctx, w.pool)
		for agentID, e := range batch {
			if _, err := q.Exec(sctx, `
				UPDATE agent_profiles SET last_seen_at=$1
				WHERE tenant_id=$2 AND id=$3 AND (last_seen_at IS NULL OR last_seen_at < $1)`,
				e.at, e.tenantID, agentID); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunFlushLoop coalesces MarkSeen calls into periodic Postgres writes.
// interval is the real, documented cadence (ADR-0010 target: ~1 write per
// agent per few minutes) — 2 minutes by default when the caller passes 0.
func (w *CoalescedLastSeenWriter) RunFlushLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = w.FlushNow(context.Background())
			return
		case <-ticker.C:
			_ = w.FlushNow(ctx)
		}
	}
}
