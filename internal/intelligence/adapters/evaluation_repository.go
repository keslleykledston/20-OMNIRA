package adapters

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type PostgresEvaluationRepository struct{ pool *pgxpool.Pool }

var _ ports.EvaluationRepository = (*PostgresEvaluationRepository)(nil)

func NewPostgresEvaluationRepository(pool *pgxpool.Pool) *PostgresEvaluationRepository {
	return &PostgresEvaluationRepository{pool: pool}
}

func rate(num, den int) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func (r *PostgresEvaluationRepository) countMap(ctx context.Context, sql string, args ...any) (map[string]int, error) {
	rows, err := platformdb.QuerierFromContext(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// Report runs inside the caller's tenant session (RLS applies); every query is also filtered by tenant_id explicitly.
func (r *PostgresEvaluationRepository) Report(ctx context.Context, tenantID uuid.UUID, days int) (*ports.Evaluation, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	q := platformdb.QuerierFromContext(ctx, r.pool)
	ev := &ports.Evaluation{Days: days}
	var err error

	// router: every decision of the window (applied or only proposed), and how many people overrode
	if err = q.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE applied AND decision_source <> 'ai'), count(*) FILTER (WHERE applied AND overridden_at IS NOT NULL)
		FROM routing_decisions WHERE tenant_id=$1 AND decision_source <> 'ai' AND created_at >= now() - make_interval(days => $2)`, tenantID, days).
		Scan(&ev.Router.Decisions, &ev.Router.Applied, &ev.Router.Overridden); err != nil {
		return nil, err
	}
	ev.Router.OverrideRate = rate(ev.Router.Overridden, ev.Router.Applied)
	if ev.Router.ByStatus, err = r.countMap(ctx, `SELECT status, count(*) FROM routing_decisions WHERE tenant_id=$1 AND decision_source <> 'ai' AND created_at >= now() - make_interval(days => $2) GROUP BY 1`, tenantID, days); err != nil {
		return nil, err
	}
	if ev.Router.BySource, err = r.countMap(ctx, `SELECT decision_source, count(*) FROM routing_decisions WHERE tenant_id=$1 AND decision_source <> 'ai' AND created_at >= now() - make_interval(days => $2) GROUP BY 1`, tenantID, days); err != nil {
		return nil, err
	}

	// AI shadow: a proposal "agreed" when the topic it chose is one the message really ended up in. Only proposals that
	// pointed at an existing topic and whose message has a placement are comparable.
	if err = q.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(input_tokens),0)::bigint, COALESCE(sum(output_tokens),0)::bigint
		FROM routing_decisions WHERE tenant_id=$1 AND decision_source='ai' AND NOT applied AND created_at >= now() - make_interval(days => $2)`, tenantID, days).
		Scan(&ev.AIShadow.Proposals, &ev.AIShadow.TokensIn, &ev.AIShadow.TokensOut); err != nil {
		return nil, err
	}
	if ev.AIShadow.ByStatus, err = r.countMap(ctx, `SELECT status, count(*) FROM routing_decisions WHERE tenant_id=$1 AND decision_source='ai' AND NOT applied AND created_at >= now() - make_interval(days => $2) GROUP BY 1`, tenantID, days); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		WITH cmp AS (
		  SELECT d.confidence::float8 AS conf,
		         EXISTS (SELECT 1 FROM message_topic_links l WHERE l.tenant_id=d.tenant_id AND l.message_id=d.message_id) AS placed,
		         EXISTS (SELECT 1 FROM message_topic_links l WHERE l.tenant_id=d.tenant_id AND l.message_id=d.message_id AND l.topic_thread_id=d.selected_topic_thread_id) AS agreed
		  FROM routing_decisions d
		  WHERE d.tenant_id=$1 AND d.decision_source='ai' AND NOT d.applied AND d.status='assigned' AND d.selected_topic_thread_id IS NOT NULL
		    AND d.message_id IS NOT NULL AND d.created_at >= now() - make_interval(days => $2)
		)
		SELECT CASE WHEN conf < 0.5 THEN '0.00-0.49' WHEN conf < 0.8 THEN '0.50-0.79' ELSE '0.80-1.00' END AS bucket,
		       count(*) FILTER (WHERE placed), count(*) FILTER (WHERE placed AND agreed)
		FROM cmp GROUP BY 1 ORDER BY 1`, tenantID, days)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b ports.ConfBucket
		if err := rows.Scan(&b.Range, &b.WithFinalPlacement, &b.Agreed); err != nil {
			rows.Close()
			return nil, err
		}
		b.AgreementRate = rate(b.Agreed, b.WithFinalPlacement)
		ev.AIShadow.WithFinalPlacement += b.WithFinalPlacement
		ev.AIShadow.Agreed += b.Agreed
		ev.AIShadow.ByConfidence = append(ev.AIShadow.ByConfidence, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if ev.AIShadow.ByConfidence == nil {
		ev.AIShadow.ByConfidence = []ports.ConfBucket{}
	}
	ev.AIShadow.AgreementRate = rate(ev.AIShadow.Agreed, ev.AIShadow.WithFinalPlacement)

	// ambiguities
	if err = q.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status='resolved' AND resolution_source='agent'), count(*) FILTER (WHERE status='resolved' AND resolution_source='customer'), count(*) FILTER (WHERE status='open')
		FROM ambiguity_cases WHERE tenant_id=$1 AND created_at >= now() - make_interval(days => $2)`, tenantID, days).
		Scan(&ev.Ambiguities.Opened, &ev.Ambiguities.ResolvedByAgent, &ev.Ambiguities.ResolvedByCust, &ev.Ambiguities.StillOpen); err != nil {
		return nil, fmt.Errorf("ambiguities: %w", err)
	}
	// summaries
	if err = q.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE model_provider IS NOT NULL), count(*) FILTER (WHERE status IN ('agent_confirmed','customer_confirmed')), count(*) FILTER (WHERE status='corrected')
		FROM topic_summaries WHERE tenant_id=$1 AND created_at >= now() - make_interval(days => $2)`, tenantID, days).
		Scan(&ev.Summaries.Versions, &ev.Summaries.AIInferred, &ev.Summaries.Confirmed, &ev.Summaries.Corrected); err != nil {
		return nil, err
	}
	ev.Summaries.CorrectionRate = rate(ev.Summaries.Corrected, ev.Summaries.Confirmed+ev.Summaries.Corrected)
	if ev.ToolCalls, err = r.countMap(ctx, `SELECT status, count(*) FROM ai_tool_calls WHERE tenant_id=$1 AND created_at >= now() - make_interval(days => $2) GROUP BY 1`, tenantID, days); err != nil {
		return nil, err
	}
	if ev.Handoffs, err = r.countMap(ctx, `
		SELECT CASE WHEN status='pending' AND expires_at <= now() THEN 'expired' ELSE status END, count(*)
		FROM topic_handoffs WHERE tenant_id=$1 AND created_at >= now() - make_interval(days => $2) GROUP BY 1`, tenantID, days); err != nil {
		return nil, err
	}
	return ev, nil
}
