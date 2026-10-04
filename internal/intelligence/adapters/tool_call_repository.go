package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type PostgresToolCallRepository struct{ pool *pgxpool.Pool }

var _ ports.ToolCallRepository = (*PostgresToolCallRepository)(nil)

func NewPostgresToolCallRepository(pool *pgxpool.Pool) *PostgresToolCallRepository {
	return &PostgresToolCallRepository{pool: pool}
}

func (r *PostgresToolCallRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

const toolCallColumns = `id, tenant_id, topic_thread_id, tool, risk, source, status, args, result, error, idempotency_key, requested_by, decided_by`

func scanToolCall(row pgx.Row) (*domain.ToolCall, error) {
	var c domain.ToolCall
	var risk, source, status string
	var args, result []byte
	if err := row.Scan(&c.ID, &c.TenantID, &c.TopicThreadID, &c.Tool, &risk, &source, &status, &args, &result, &c.Error, &c.IdempotencyKey, &c.RequestedBy, &c.DecidedBy); err != nil {
		return nil, err
	}
	c.Risk, c.Source, c.Status = domain.ToolRisk(risk), domain.ToolSource(source), domain.ToolCallStatus(status)
	c.Args, c.Result = args, result
	return &c, nil
}

func (r *PostgresToolCallRepository) Begin(ctx context.Context, c *domain.ToolCall) (*domain.ToolCall, bool, error) {
	args := c.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	stored, err := scanToolCall(r.q(ctx).QueryRow(ctx, `
		INSERT INTO ai_tool_calls (tenant_id, topic_thread_id, tool, risk, source, status, args, error, idempotency_key, requested_by, decided_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10, CASE WHEN $6 = 'denied' THEN now() END)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
		RETURNING `+toolCallColumns, c.TenantID, c.TopicThreadID, c.Tool, string(c.Risk), string(c.Source), string(c.Status), string(args), c.Error, c.IdempotencyKey, c.RequestedBy))
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, mapError(err)
	}
	existing, err := scanToolCall(r.q(ctx).QueryRow(ctx, `SELECT `+toolCallColumns+` FROM ai_tool_calls WHERE tenant_id=$1 AND idempotency_key=$2`, c.TenantID, c.IdempotencyKey))
	if err != nil {
		return nil, false, err
	}
	if existing.Tool != c.Tool || existing.TopicThreadID != c.TopicThreadID || !sameJSON(existing.Args, args) {
		return nil, false, ports.ErrIdempotencyMismatch
	}
	return existing, false, nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb)
}

func (r *PostgresToolCallRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.ToolCall, error) {
	c, err := scanToolCall(r.q(ctx).QueryRow(ctx, `SELECT `+toolCallColumns+` FROM ai_tool_calls WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrReferenceNotFound
	}
	return c, err
}

func (r *PostgresToolCallRepository) List(ctx context.Context, tenantID, topicID uuid.UUID, limit int) ([]domain.ToolCall, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+toolCallColumns+` FROM ai_tool_calls WHERE tenant_id=$1 AND topic_thread_id=$2 ORDER BY created_at DESC, id DESC LIMIT $3`, tenantID, topicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ToolCall{}
	for rows.Next() {
		c, err := scanToolCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *PostgresToolCallRepository) Transition(ctx context.Context, tenantID, id uuid.UUID, from, to domain.ToolCallStatus, result json.RawMessage, errText string, decidedBy *uuid.UUID) (bool, error) {
	var res any
	if len(result) > 0 {
		res = string(result)
	}
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE ai_tool_calls SET status=$4, result=$5::jsonb, error=$6, decided_by=COALESCE($7, decided_by),
		       decided_at = CASE WHEN $4 IN ('rejected','running') AND $3 = 'pending_approval' THEN now() ELSE decided_at END,
		       executed_at = CASE WHEN $4 = 'executed' THEN now() ELSE executed_at END
		WHERE tenant_id=$1 AND id=$2 AND status=$3`, tenantID, id, string(from), string(to), res, errText, decidedBy)
	if err != nil {
		return false, mapError(err)
	}
	return tag.RowsAffected() == 1, nil
}
