package adapters

import (
	"context"
	"time"

	"github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var _ ports.AnalysisRepository = (*PostgresRepository)(nil)

// ClaimAnalysis leases due jobs whose input file is cleared and still on disk.
func (r *PostgresRepository) ClaimAnalysis(ctx context.Context, kind string, limit int, lease time.Duration) ([]ports.AnalysisWork, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []ports.AnalysisWork
	err := r.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			WITH due AS (
			  SELECT a.id FROM message_media_analysis a
			  WHERE a.status = 'pending' AND a.kind = $1 AND a.next_attempt_at <= now()
			  ORDER BY a.next_attempt_at, a.created_at
			  LIMIT $2
			  FOR UPDATE OF a SKIP LOCKED
			), claimed AS (
			  UPDATE message_media_analysis a
			  SET attempts = a.attempts + 1, next_attempt_at = now() + make_interval(secs => $3::float8), updated_at = now()
			  FROM due WHERE a.id = due.id
			  RETURNING a.id, a.tenant_id, a.message_id, a.kind, a.attempts
			)
			SELECT c.id, c.tenant_id, c.message_id, mm.id, c.kind, c.attempts, mm.mime
			FROM claimed c
			JOIN message_media mm ON mm.tenant_id = c.tenant_id AND mm.message_id = c.message_id`,
			kind, limit, lease.Seconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w ports.AnalysisWork
			if err := rows.Scan(&w.ID, &w.TenantID, &w.MessageID, &w.MediaID, &w.Kind, &w.Attempts, &w.Mime); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (r *PostgresRepository) SaveAnalysis(ctx context.Context, w ports.AnalysisWork, a ports.Analysis) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			UPDATE message_media_analysis
			SET status='done', body=$3, language=$4, model=$5, suspicious=$6, reason=$7, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, a.Text, a.Language, a.Model, a.Suspicious, a.Reason)
		return requireOne(tag.RowsAffected(), err)
	})
}

func (r *PostgresRepository) SaveAnalysisEmpty(ctx context.Context, w ports.AnalysisWork, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `UPDATE message_media_analysis SET status='empty', reason=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, reason)
		return requireOne(tag.RowsAffected(), err)
	})
}

func (r *PostgresRepository) FailAnalysis(ctx context.Context, w ports.AnalysisWork, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `UPDATE message_media_analysis SET status='failed', reason=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, reason)
		return requireOne(tag.RowsAffected(), err)
	})
}

func (r *PostgresRepository) RetryAnalysis(ctx context.Context, w ports.AnalysisWork, delay time.Duration, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		_, err := db.Exec(ctx, `UPDATE message_media_analysis SET next_attempt_at = now() + make_interval(secs => $3::float8), reason=$4, updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, delay.Seconds(), reason)
		return err
	})
}
