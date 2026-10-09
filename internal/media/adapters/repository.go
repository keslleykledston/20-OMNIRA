package adapters

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresRepository is the worker's view of message_media. Every call runs in a system session (is_system_admin):
// workers have no human actor, and the table's UPDATE/DELETE policies admit only system sessions, so an operator
// session can never change a verdict.
type PostgresRepository struct{ pool *pgxpool.Pool }

var _ ports.Repository = (*PostgresRepository)(nil)

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) system(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return fn(sctx, platformdb.QuerierFromContext(sctx, r.pool))
	})
}

// WhileActive implements ports.TenantGate. fn gets the CALLER's context (not the lock transaction's): its own repository calls
// open their own sessions; this one only holds the share lock on the company's row until fn returns.
func (r *PostgresRepository) WhileActive(ctx context.Context, tenant uuid.UUID, fn func(ctx context.Context) error) (bool, error) {
	ran := false
	err := platformdb.WithSystemTenantSession(ctx, r.pool, tenant, func(c context.Context) error {
		active, err := platformdb.LockTenantActive(c, platformdb.QuerierFromContext(c, r.pool), tenant)
		if err != nil || !active {
			return err
		}
		ran = true
		return fn(ctx)
	})
	return ran, err
}

func (r *PostgresRepository) Claim(ctx context.Context, limit int, lease time.Duration) ([]ports.Work, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []ports.Work
	err := r.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			WITH due AS (
			  SELECT mm.id FROM message_media mm
			  JOIN tenants t ON t.id = mm.tenant_id AND t.status = 'active'
			  WHERE mm.status IN ('pending','quarantined') AND mm.next_attempt_at <= now()
			  ORDER BY mm.next_attempt_at, mm.created_at
			  LIMIT $1
			  FOR UPDATE OF mm SKIP LOCKED
			  FOR SHARE OF t SKIP LOCKED
			), claimed AS (
			  UPDATE message_media mm
			  SET attempts = mm.attempts + 1,
			      next_attempt_at = now() + make_interval(secs => $2::float8),
			      updated_at = now()
			  FROM due WHERE mm.id = due.id
			  RETURNING mm.id, mm.tenant_id, mm.message_id, mm.status, mm.attempts, mm.created_at
			)
			SELECT c.id, c.tenant_id, c.message_id, c.status, c.attempts, c.created_at, m.media_ref, m.mime_type, COALESCE(m.channel_connection_id, '00000000-0000-0000-0000-000000000000'::uuid)
			FROM claimed c
			JOIN messages m ON m.tenant_id = c.tenant_id AND m.id = c.message_id`, limit, lease.Seconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w ports.Work
			var status string
			if err := rows.Scan(&w.ID, &w.TenantID, &w.MessageID, &status, &w.Attempts, &w.CreatedAt, &w.MediaRef, &w.DeclaredMime, &w.ConnectionID); err != nil {
				return err
			}
			w.Status = ports.Status(status)
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (r *PostgresRepository) MarkQuarantined(ctx context.Context, w ports.Work, q ports.Quarantined) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			UPDATE message_media
			SET status='quarantined', kind=$3, mime=$4, size_bytes=$5, sha256=$6, fetched_at=now(), reason='', updated_at=now(),
			    next_attempt_at = now()
			WHERE tenant_id=$1 AND id=$2 AND status IN ('pending','quarantined')`, w.TenantID, w.ID, q.Kind, q.Mime, q.SizeBytes, q.SHA256)
		return requireOne(tag.RowsAffected(), err)
	})
}

func (r *PostgresRepository) MarkClean(ctx context.Context, w ports.Work) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			UPDATE message_media SET status='clean', scanned_at=now(), reason='', updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status='quarantined'`, w.TenantID, w.ID)
		return requireOne(tag.RowsAffected(), err)
	})
}

// MarkTerminal writes the final status and its audit event in one transaction.
func (r *PostgresRepository) MarkTerminal(ctx context.Context, w ports.Work, status ports.Status, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			UPDATE message_media
			SET status=$3, reason=$4, scanned_at = CASE WHEN $3 IN ('infected','failed') THEN now() ELSE scanned_at END, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status IN ('pending','quarantined')`, w.TenantID, w.ID, string(status), reason)
		if err := requireOne(tag.RowsAffected(), err); err != nil {
			return err
		}
		_, err = db.Exec(ctx, `
			INSERT INTO audit_events (tenant_id, actor_id, action, resource_type, resource_id, outcome, metadata)
			VALUES ($1, NULL, $2, 'message_media', $3, $4, jsonb_build_object('message_id', $5::text, 'reason', $6::text))`,
			w.TenantID, "media."+string(status), w.ID.String(), auditOutcome(status), w.MessageID.String(), reason)
		return err
	})
}

func auditOutcome(s ports.Status) string {
	if s == ports.StatusInfected || s == ports.StatusRejected || s == ports.StatusFailed {
		return "failure"
	}
	return "success"
}

func (r *PostgresRepository) Retry(ctx context.Context, w ports.Work, delay time.Duration, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		_, err := db.Exec(ctx, `
			UPDATE message_media SET next_attempt_at = now() + make_interval(secs => $3::float8), reason=$4, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status IN ('pending','quarantined')`, w.TenantID, w.ID, delay.Seconds(), reason)
		return err
	})
}

func (r *PostgresRepository) ExpiredFiles(ctx context.Context, olderThan time.Time, limit int) ([]ports.Work, error) {
	var out []ports.Work
	err := r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		rows, err := db.Query(ctx, `
			SELECT id, tenant_id, message_id, status FROM message_media
			WHERE file_purged_at IS NULL AND status IN ('clean','quarantined','failed') AND created_at < $1
			ORDER BY created_at LIMIT $2`, olderThan, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w ports.Work
			var s string
			if err := rows.Scan(&w.ID, &w.TenantID, &w.MessageID, &s); err != nil {
				return err
			}
			w.Status = ports.Status(s)
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (r *PostgresRepository) MarkPurged(ctx context.Context, w ports.Work) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		_, err := db.Exec(ctx, `UPDATE message_media SET file_purged_at=now(), updated_at=now() WHERE tenant_id=$1 AND id=$2`, w.TenantID, w.ID)
		return err
	})
}

// requireOne turns "no row matched" into an error: a verdict that did not land must not look like success.
func requireOne(affected int64, err error) error {
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("media: expected to update one row, updated %d", affected)
	}
	return nil
}
