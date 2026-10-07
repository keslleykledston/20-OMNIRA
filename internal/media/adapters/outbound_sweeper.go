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
//   - an upload that was never attached to a message and expired (24 h) or was taken out of the composer: the row is deleted;
//   - a sent file older than the retention window (60 days, like inbound media): the row is marked purged and stays as the record.
//
// The ROW changes first, in one statement that takes the row lock the send path also takes (so a send and a sweep of the same upload
// cannot both win), and the FILE is removed afterwards. A crash in between leaves an orphan file, which the orphan pass collects.
// Messages still queued are never purged: their file has not been delivered yet.
type OutboundSweeper struct {
	pool      *pgxpool.Pool
	files     *OutboundFiles
	retention time.Duration
	orphanAge time.Duration
}

func NewOutboundSweeper(pool *pgxpool.Pool, files *OutboundFiles, retention time.Duration) *OutboundSweeper {
	return &OutboundSweeper{pool: pool, files: files, retention: retention, orphanAge: 48 * time.Hour}
}

type outboundRef struct{ tenantID, id uuid.UUID }

func (s *OutboundSweeper) system(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(sctx context.Context) error {
		return fn(sctx, platformdb.QuerierFromContext(sctx, s.pool))
	})
}

// returning runs one statement that ends in RETURNING tenant_id, id and commits it before returning the references.
func (s *OutboundSweeper) returning(ctx context.Context, query string, args ...any) ([]outboundRef, error) {
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
	unsent, err := s.returning(ctx, `
		WITH d AS (
		  SELECT id FROM message_outbound_media
		  WHERE message_id IS NULL AND file_purged_at IS NULL AND expires_at < now()
		  ORDER BY expires_at LIMIT 200 FOR UPDATE SKIP LOCKED)
		DELETE FROM message_outbound_media m USING d WHERE m.id = d.id AND m.message_id IS NULL
		RETURNING m.tenant_id, m.id`)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range unsent {
		if err := s.files.Remove(r.tenantID, r.id); err != nil {
			return deleted, purged, err
		}
		deleted++
	}
	sent, err := s.returning(ctx, `
		WITH d AS (
		  SELECT om.id FROM message_outbound_media om
		  JOIN messages m ON m.tenant_id = om.tenant_id AND m.id = om.message_id
		  WHERE om.message_id IS NOT NULL AND om.file_purged_at IS NULL AND m.status <> 'queued'
		    AND om.created_at < now() - make_interval(secs => $1::float8)
		  ORDER BY om.created_at LIMIT 200 FOR UPDATE OF om SKIP LOCKED)
		UPDATE message_outbound_media o SET file_purged_at = now() FROM d WHERE o.id = d.id
		RETURNING o.tenant_id, o.id`, s.retention.Seconds())
	if err != nil {
		return deleted, 0, err
	}
	for _, r := range sent {
		if err := s.files.Remove(r.tenantID, r.id); err != nil {
			return deleted, purged, err
		}
		purged++
	}
	return deleted, purged, nil
}

// Orphans removes files that no record owns (a crash between writing the file and inserting its row) and stale ".part" temporaries, once
// they are old enough that no upload can still be in flight.
func (s *OutboundSweeper) Orphans(ctx context.Context) (int, error) {
	return s.files.Orphans(s.orphanAge, 200, func(tenantID, id uuid.UUID) (bool, error) {
		var exists bool
		err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
			return q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM message_outbound_media WHERE tenant_id=$1 AND id=$2)`, tenantID, id).Scan(&exists)
		})
		return exists, err
	})
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
		if n, err := s.Orphans(ctx); err != nil {
			log.Printf("outbound media: orphan pass failed: %v", err)
		} else if n > 0 {
			log.Printf("outbound media: removed %d orphan file(s)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
