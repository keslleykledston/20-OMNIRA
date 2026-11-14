package adapters

import (
	"context"
	"time"

	mediaports "github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var _ mediaports.AnalysisRepository = (*GroupMediaRepository)(nil)

// ClaimAnalysis mirrors internal/media/adapters.PostgresRepository.ClaimAnalysis, against
// wa_group_message_analysis/wa_group_message_media instead of message_media_analysis/message_media.
func (r *GroupMediaRepository) ClaimAnalysis(ctx context.Context, kind string, limit int, lease time.Duration) ([]mediaports.AnalysisWork, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []mediaports.AnalysisWork
	err := r.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			WITH due AS (
			  SELECT a.id FROM wa_group_message_analysis a
			  JOIN tenants t ON t.id = a.tenant_id AND t.status = 'active'
			  WHERE a.status = 'pending' AND a.kind = $1 AND a.next_attempt_at <= now()
			  ORDER BY a.next_attempt_at, a.created_at
			  LIMIT $2
			  FOR UPDATE OF a SKIP LOCKED
			  FOR SHARE OF t SKIP LOCKED
			), claimed AS (
			  UPDATE wa_group_message_analysis a
			  SET attempts = a.attempts + 1, next_attempt_at = now() + make_interval(secs => $3::float8), updated_at = now()
			  FROM due WHERE a.id = due.id
			  RETURNING a.id, a.tenant_id, a.group_message_id, a.kind, a.attempts
			)
			SELECT c.id, c.tenant_id, c.group_message_id, mm.id, c.kind, c.attempts, mm.mime
			FROM claimed c
			JOIN wa_group_message_media mm ON mm.tenant_id = c.tenant_id AND mm.group_message_id = c.group_message_id`,
			kind, limit, lease.Seconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w mediaports.AnalysisWork
			if err := rows.Scan(&w.ID, &w.TenantID, &w.MessageID, &w.MediaID, &w.Kind, &w.Attempts, &w.Mime); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (r *GroupMediaRepository) SaveAnalysis(ctx context.Context, w mediaports.AnalysisWork, a mediaports.Analysis) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			UPDATE wa_group_message_analysis
			SET status='done', body=$3, language=$4, model=$5, suspicious=$6, reason=$7, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, a.Text, a.Language, a.Model, a.Suspicious, a.Reason)
		return requireOneGroupMedia(tag.RowsAffected(), err)
	})
}

func (r *GroupMediaRepository) SaveAnalysisEmpty(ctx context.Context, w mediaports.AnalysisWork, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `UPDATE wa_group_message_analysis SET status='empty', reason=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, reason)
		return requireOneGroupMedia(tag.RowsAffected(), err)
	})
}

func (r *GroupMediaRepository) FailAnalysis(ctx context.Context, w mediaports.AnalysisWork, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `UPDATE wa_group_message_analysis SET status='failed', reason=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, reason)
		return requireOneGroupMedia(tag.RowsAffected(), err)
	})
}

func (r *GroupMediaRepository) RetryAnalysis(ctx context.Context, w mediaports.AnalysisWork, delay time.Duration, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		_, err := db.Exec(ctx, `UPDATE wa_group_message_analysis SET next_attempt_at = now() + make_interval(secs => $3::float8), reason=$4, updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, delay.Seconds(), reason)
		return err
	})
}

var _ mediaports.VisionRepository = (*GroupMediaRepository)(nil)

// EnqueueVision mirrors internal/media/adapters.PostgresRepository.EnqueueVision: ONLY for tenants that
// opted into the external AI AND explicitly also opted group images into it (tenant_ai_integrations.enabled
// AND .group_media_enabled — the SAME key/budget/consent 1:1 vision already uses, but its own decision, never
// implied by enabling 1:1 alone), only cleared group images/PDFs still on disk and recent enough to matter.
func (r *GroupMediaRepository) EnqueueVision(ctx context.Context, newerThan time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}
	var n int
	err := r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `
			INSERT INTO wa_group_message_analysis (tenant_id, group_message_id, kind, engine)
			SELECT mm.tenant_id, mm.group_message_id,
			       CASE WHEN mm.mime = 'application/pdf' THEN 'document_text' ELSE 'description' END, 'gemini'
			FROM wa_group_message_media mm
			JOIN tenant_ai_integrations ti ON ti.tenant_id = mm.tenant_id AND ti.provider = 'gemini' AND ti.enabled AND ti.group_media_enabled
			JOIN tenants tn ON tn.id = mm.tenant_id AND tn.status = 'active'
			WHERE mm.status = 'clean' AND mm.file_purged_at IS NULL AND mm.created_at >= $1
			  AND mm.mime IN ('image/jpeg','image/png','image/webp','application/pdf')
			  AND NOT EXISTS (SELECT 1 FROM wa_group_message_analysis a WHERE a.tenant_id = mm.tenant_id AND a.group_message_id = mm.group_message_id
			                  AND a.kind IN ('description','document_text'))
			ORDER BY mm.created_at
			LIMIT $2
			FOR SHARE OF tn SKIP LOCKED
			ON CONFLICT (tenant_id, group_message_id, kind) DO NOTHING`, newerThan, limit)
		n = int(tag.RowsAffected())
		return err
	})
	return n, err
}

func (r *GroupMediaRepository) SkipAnalysis(ctx context.Context, w mediaports.AnalysisWork, reason string) error {
	return r.system(ctx, func(ctx context.Context, db platformdb.Querier) error {
		tag, err := db.Exec(ctx, `UPDATE wa_group_message_analysis SET status='skipped', reason=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
			w.TenantID, w.ID, reason)
		return requireOneGroupMedia(tag.RowsAffected(), err)
	})
}
