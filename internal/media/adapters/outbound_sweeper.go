package adapters

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// OutboundSweeper (ADR-0024) removes files nobody will send any more and applies the retention of the ones that were sent:
//   - an upload that was never attached to a message and expired (24 h) or was taken out of the composer: file and row are deleted;
//   - a sent file older than the retention window (60 days, like inbound media): the file is removed, the row stays as the record.
type OutboundSweeper struct {
	pool      *pgxpool.Pool
	files     *OutboundFiles
	retention time.Duration
	now       func() time.Time
}

func NewOutboundSweeper(pool *pgxpool.Pool, files *OutboundFiles, retention time.Duration) *OutboundSweeper {
	return &OutboundSweeper{pool: pool, files: files, retention: retention, now: time.Now}
}

type outboundRef struct{ tenantID, id uuid.UUID }

func (s *OutboundSweeper) system(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(sctx context.Context) error {
		return fn(sctx, platformdb.QuerierFromContext(sctx, s.pool))
	})
}

func (s *OutboundSweeper) refs(ctx context.Context, query string, args ...any) ([]outboundRef, error) {
	var out []outboundRef
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r outboundRef
			if err := rows.Scan(&r.tenantID, &r.id); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// Once runs one sweep and returns how many unsent uploads were deleted and how many sent files were purged.
func (s *OutboundSweeper) Once(ctx context.Context) (deleted, purged int, err error) {
	now := s.now()
	unsent, err := s.refs(ctx, `
		SELECT tenant_id, id FROM message_outbound_media
		WHERE message_id IS NULL AND file_purged_at IS NULL AND expires_at < $1 ORDER BY expires_at LIMIT 200`, now)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range unsent {
		if err := s.files.Remove(r.tenantID, r.id); err != nil {
			return deleted, purged, err
		}
		if err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
			// the file is gone; a row that raced into a message in the meantime is kept (message_id IS NULL guard)
			_, err := q.Exec(ctx, `DELETE FROM message_outbound_media WHERE tenant_id=$1 AND id=$2 AND message_id IS NULL`, r.tenantID, r.id)
			return err
		}); err != nil {
			return deleted, purged, err
		}
		deleted++
	}
	sent, err := s.refs(ctx, `
		SELECT tenant_id, id FROM message_outbound_media
		WHERE message_id IS NOT NULL AND file_purged_at IS NULL AND created_at < $1 ORDER BY created_at LIMIT 200`, now.Add(-s.retention))
	if err != nil {
		return deleted, 0, err
	}
	for _, r := range sent {
		if err := s.files.Remove(r.tenantID, r.id); err != nil {
			return deleted, purged, err
		}
		if err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
			_, err := q.Exec(ctx, `UPDATE message_outbound_media SET file_purged_at=now() WHERE tenant_id=$1 AND id=$2`, r.tenantID, r.id)
			return err
		}); err != nil {
			return deleted, purged, err
		}
		purged++
	}
	return deleted, purged, nil
}

// Run sweeps every interval until ctx ends. Failures are logged and retried at the next tick.
func (s *OutboundSweeper) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if d, p, err := s.Once(ctx); err != nil {
			log.Printf("outbound media: sweep failed: %v", err)
		} else if d+p > 0 {
			log.Printf("outbound media: swept unsent=%d purged=%d", d, p)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
