package adapters

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	mediaports "github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// GroupMediaRepository is the worker's view of wa_group_message_media/wa_group_messages (ADR-0015 addendum
// 2): the SAME fetch/antivirus state machine 1:1 media already uses (internal/media/application.Processor,
// internal/media/ports.Repository), run a second time against a table of its own, never touching
// message_media. Every call runs in a system session (is_system_admin): the table's UPDATE/DELETE policies
// admit only system sessions, same as message_media's.
type GroupMediaRepository struct{ pool *pgxpool.Pool }

var _ mediaports.Repository = (*GroupMediaRepository)(nil)

func NewGroupMediaRepository(pool *pgxpool.Pool) *GroupMediaRepository {
	return &GroupMediaRepository{pool: pool}
}

func (r *GroupMediaRepository) system(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return fn(sctx, platformdb.QuerierFromContext(sctx, r.pool))
	})
}

// WhileActive implements mediaports.TenantGate, identical in shape to internal/media/adapters.PostgresRepository's.
func (r *GroupMediaRepository) WhileActive(ctx context.Context, tenant uuid.UUID, fn func(ctx context.Context) error) (bool, error) {
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

// Claim mirrors internal/media/adapters.PostgresRepository.Claim exactly, against wa_group_message_media
// joined to wa_group_messages (never messages) for media_ref/mime_type/channel_connection_id.
func (r *GroupMediaRepository) Claim(ctx context.Context, limit int, lease time.Duration) ([]mediaports.Work, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []mediaports.Work
	err := r.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			WITH due AS (
			  SELECT mm.id FROM wa_group_message_media mm
			  JOIN tenants t ON t.id = mm.tenant_id AND t.status = 'active'
			  WHERE mm.status IN ('pending','quarantined') AND mm.next_attempt_at <= now()
			  ORDER BY mm.next_attempt_at, mm.created_at
			  LIMIT $1
			  FOR UPDATE OF mm SKIP LOCKED
			  FOR SHARE OF t SKIP LOCKED
			), claimed AS (
			  UPDATE wa_group_message_media mm
			  SET attempts = mm.attempts + 1,
			      next_attempt_at = now() + make_interval(secs => $2::float8),
			      updated_at = now()
			  FROM due WHERE mm.id = due.id
			  RETURNING mm.id, mm.tenant_id, mm.group_message_id, mm.status, mm.attempts, mm.created_at
			)
			SELECT c.id, c.tenant_id, c.group_message_id, c.status, c.attempts, c.created_at, m.media_ref, m.mime_type, g.channel_connection_id
			FROM claimed c
			JOIN wa_group_messages m ON m.tenant_id = c.tenant_id AND m.id = c.group_message_id
			JOIN wa_groups g ON g.tenant_id = m.tenant_id AND g.id = m.group_id`, limit, lease.Seconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w mediaports.Work
			var status string
			if err := rows.Scan(&w.ID, &w.TenantID, &w.MessageID, &status, &w.Attempts, &w.CreatedAt, &w.MediaRef, &w.DeclaredMime, &w.ConnectionID); err != nil {
				return err
			}
			w.Status = mediaports.Status(status)
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (r *GroupMediaRepository) MarkQuarantined(ctx context.Context, w mediaports.Work, q mediaports.Quarantined) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			UPDATE wa_group_message_media
			SET status='quarantined', kind=$3, mime=$4, size_bytes=$5, sha256=$6, fetched_at=now(), reason='', updated_at=now(),
			    next_attempt_at = now()
			WHERE tenant_id=$1 AND id=$2 AND status IN ('pending','quarantined')`, w.TenantID, w.ID, q.Kind, q.Mime, q.SizeBytes, q.SHA256)
		return requireOneGroupMedia(tag.RowsAffected(), err)
	})
}

func (r *GroupMediaRepository) MarkClean(ctx context.Context, w mediaports.Work) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			UPDATE wa_group_message_media SET status='clean', scanned_at=now(), reason='', updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status='quarantined'`, w.TenantID, w.ID)
		return requireOneGroupMedia(tag.RowsAffected(), err)
	})
}

// MarkTerminal writes the final status and its audit event in one transaction, same shape as 1:1's
// (resource_type names this table specifically, so the two pipelines are never confused in the trail).
func (r *GroupMediaRepository) MarkTerminal(ctx context.Context, w mediaports.Work, status mediaports.Status, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			UPDATE wa_group_message_media
			SET status=$3, reason=$4, scanned_at = CASE WHEN $3 IN ('infected','failed') THEN now() ELSE scanned_at END, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status IN ('pending','quarantined')`, w.TenantID, w.ID, string(status), reason)
		if err := requireOneGroupMedia(tag.RowsAffected(), err); err != nil {
			return err
		}
		_, err = db.Exec(ctx, `
			INSERT INTO audit_events (tenant_id, actor_id, action, resource_type, resource_id, outcome, metadata)
			VALUES ($1, NULL, $2, 'wa_group_message_media', $3, $4, jsonb_build_object('group_message_id', $5::text, 'reason', $6::text))`,
			w.TenantID, "group_media."+string(status), w.ID.String(), groupMediaAuditOutcome(status), w.MessageID.String(), reason)
		return err
	})
}

func groupMediaAuditOutcome(s mediaports.Status) string {
	if s == mediaports.StatusInfected || s == mediaports.StatusRejected || s == mediaports.StatusFailed {
		return "failure"
	}
	return "success"
}

func (r *GroupMediaRepository) Retry(ctx context.Context, w mediaports.Work, delay time.Duration, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		_, err := db.Exec(ctx, `
			UPDATE wa_group_message_media SET next_attempt_at = now() + make_interval(secs => $3::float8), reason=$4, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status IN ('pending','quarantined')`, w.TenantID, w.ID, delay.Seconds(), reason)
		return err
	})
}

func (r *GroupMediaRepository) ExpiredFiles(ctx context.Context, olderThan time.Time, limit int) ([]mediaports.Work, error) {
	var out []mediaports.Work
	err := r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		rows, err := db.Query(ctx, `
			SELECT id, tenant_id, group_message_id, status FROM wa_group_message_media
			WHERE file_purged_at IS NULL AND status IN ('clean','quarantined','failed') AND created_at < $1
			ORDER BY created_at LIMIT $2`, olderThan, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w mediaports.Work
			var s string
			if err := rows.Scan(&w.ID, &w.TenantID, &w.MessageID, &s); err != nil {
				return err
			}
			w.Status = mediaports.Status(s)
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (r *GroupMediaRepository) MarkPurged(ctx context.Context, w mediaports.Work) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		_, err := db.Exec(ctx, `UPDATE wa_group_message_media SET file_purged_at=now(), updated_at=now() WHERE tenant_id=$1 AND id=$2`, w.TenantID, w.ID)
		return err
	})
}

func requireOneGroupMedia(affected int64, err error) error {
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("group media: expected to update one row, updated %d", affected)
	}
	return nil
}
