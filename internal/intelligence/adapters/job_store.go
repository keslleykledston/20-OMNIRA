package adapters

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresJobStore runs in system sessions (the worker has no human actor); the table's write policies admit only those.
type PostgresJobStore struct{ pool *pgxpool.Pool }

var _ ports.JobStore = (*PostgresJobStore)(nil)

func NewPostgresJobStore(pool *pgxpool.Pool) *PostgresJobStore { return &PostgresJobStore{pool: pool} }

func (s *PostgresJobStore) system(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(sctx context.Context) error {
		return fn(sctx, platformdb.QuerierFromContext(sctx, s.pool))
	})
}

func (s *PostgresJobStore) EnsureFromEvent(ctx context.Context, ref ports.MessageRef, version string) (bool, error) {
	source, c := "messages", "message_id"
	if ref.Kind == ports.KindGroup {
		source, c = "wa_group_messages", "group_message_id"
	}
	var created bool
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		// the tenant comes from the stored row, never from the event payload
		tag, err := q.Exec(ctx, fmt.Sprintf(`
			INSERT INTO intelligence_jobs (tenant_id, %[1]s, pipeline_version)
			SELECT m.tenant_id, m.id, $2 FROM %[2]s m WHERE m.id = $1
			ON CONFLICT (tenant_id, %[1]s, pipeline_version) WHERE %[1]s IS NOT NULL DO NOTHING`, c, source), ref.ID, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			created = true
			return nil
		}
		var exists bool
		if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)`, source), ref.ID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrReferenceNotFound
		}
		return nil
	})
	return created, err
}

func (s *PostgresJobStore) Claim(ctx context.Context, limit int, lease time.Duration) ([]ports.Job, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []ports.Job
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			WITH due AS (
			  SELECT j.id FROM intelligence_jobs j
			  JOIN tenants t ON t.id = j.tenant_id AND t.status = 'active'
			  WHERE j.next_attempt_at <= now() AND (j.state = 'pending' OR (j.state = 'running' AND j.locked_until < now()))
			  ORDER BY j.next_attempt_at, j.created_at
			  LIMIT $1
			  FOR UPDATE OF j SKIP LOCKED
			  FOR SHARE OF t SKIP LOCKED
			)
			UPDATE intelligence_jobs j
			SET state = 'running', attempts = j.attempts + 1, started_at = now(), locked_until = now() + make_interval(secs => $2::float8), updated_at = now()
			FROM due WHERE j.id = due.id
			RETURNING j.id, j.tenant_id, j.message_id, j.group_message_id, j.pipeline_version, j.attempts`, limit, lease.Seconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j ports.Job
			var msg, grp *uuid.UUID
			if err := rows.Scan(&j.ID, &j.TenantID, &msg, &grp, &j.PipelineVersion, &j.Attempt); err != nil {
				return err
			}
			if grp != nil {
				j.Ref = ports.MessageRef{Kind: ports.KindGroup, ID: *grp}
			} else if msg != nil {
				j.Ref = ports.MessageRef{Kind: ports.KindConversation, ID: *msg}
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

// move changes the state only for the holder of the current claim.
func (s *PostgresJobStore) move(ctx context.Context, j ports.Job, sql string, args ...any) (bool, error) {
	var ok bool
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		tag, err := q.Exec(ctx, sql, append([]any{j.ID, j.Attempt}, args...)...)
		if err != nil {
			return err
		}
		ok = tag.RowsAffected() == 1
		return nil
	})
	return ok, err
}

func (s *PostgresJobStore) Complete(ctx context.Context, j ports.Job) (bool, error) {
	return s.move(ctx, j, `UPDATE intelligence_jobs SET state='completed', completed_at=now(), locked_until=NULL, last_error_class=NULL, updated_at=now()
		WHERE id=$1 AND attempts=$2 AND state='running'`)
}

func (s *PostgresJobStore) Retry(ctx context.Context, j ports.Job, class string, delay time.Duration) (bool, error) {
	return s.move(ctx, j, `UPDATE intelligence_jobs SET state='pending', last_error_class=$3, next_attempt_at=now()+make_interval(secs => $4::float8), locked_until=NULL, updated_at=now()
		WHERE id=$1 AND attempts=$2 AND state='running'`, class, delay.Seconds())
}

func (s *PostgresJobStore) Dead(ctx context.Context, j ports.Job, class string) (bool, error) {
	return s.move(ctx, j, `UPDATE intelligence_jobs SET state='dead', last_error_class=$3, locked_until=NULL, completed_at=now(), updated_at=now()
		WHERE id=$1 AND attempts=$2 AND state='running'`, class)
}
