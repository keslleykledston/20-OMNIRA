package adapters

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/aiusage"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresLedger writes and reads ai_usage. Writes use a system session (the worker has no user); the monthly summary is
// read inside the caller's tenant session so the administrator-only RLS policy applies.
type PostgresLedger struct{ pool *pgxpool.Pool }

var _ aiusage.Ledger = (*PostgresLedger)(nil)

func NewPostgresLedger(pool *pgxpool.Pool) *PostgresLedger { return &PostgresLedger{pool: pool} }

func (l *PostgresLedger) system(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, l.pool, uuid.Nil, true, func(sctx context.Context) error {
		return fn(sctx, platformdb.QuerierFromContext(sctx, l.pool))
	})
}

func (l *PostgresLedger) Record(ctx context.Context, r aiusage.Record) error {
	var cost *float64
	if !r.NoCost {
		c := r.CostUSD
		cost = &c
	}
	var ref *uuid.UUID
	if r.Ref != uuid.Nil {
		ref = &r.Ref
	}
	reason := r.Reason
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return l.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		_, err := q.Exec(ctx, `
			INSERT INTO ai_usage (tenant_id, provider, model, task, input_tokens, output_tokens, cost_usd, success, reason, ref_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			r.TenantID, r.Provider, r.Model, r.Task, nonNeg(r.InputTokens), nonNeg(r.OutputTokens), cost, r.Success, reason, ref)
		return err
	})
}

func nonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// SpentThisMonth sums the estimated cost of the budgeted provider for the tenant in now's month (UTC). Failed calls count:
// a rejected or timed-out request may still have been billed.
func (l *PostgresLedger) SpentThisMonth(ctx context.Context, tenantID uuid.UUID, now time.Time) (float64, error) {
	var spent float64
	err := l.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		return q.QueryRow(ctx, `SELECT COALESCE(sum(cost_usd), 0)::float8 FROM ai_usage WHERE tenant_id=$1 AND provider=$2 AND created_at >= $3 AND created_at < $4`,
			tenantID, aiusage.BudgetProvider, aiusage.MonthStart(now), aiusage.MonthStart(now).AddDate(0, 1, 0)).Scan(&spent)
	})
	return spent, err
}

// Summarize groups the month by provider, model and task. It runs in the caller's session (an administrator's).
func (l *PostgresLedger) Summarize(ctx context.Context, tenantID uuid.UUID, month time.Time) (aiusage.Summary, error) {
	from := aiusage.MonthStart(month)
	s := aiusage.Summary{From: from, To: from.AddDate(0, 1, 0)}
	rows, err := platformdb.QuerierFromContext(ctx, l.pool).Query(ctx, `
		SELECT provider, model, task, count(*), count(*) FILTER (WHERE NOT success),
		       COALESCE(sum(input_tokens),0)::bigint, COALESCE(sum(output_tokens),0)::bigint, COALESCE(sum(cost_usd),0)::float8
		FROM ai_usage WHERE tenant_id=$1 AND created_at >= $2 AND created_at < $3
		GROUP BY provider, model, task ORDER BY provider, model, task`, tenantID, s.From, s.To)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var ln aiusage.Line
		if err := rows.Scan(&ln.Provider, &ln.Model, &ln.Task, &ln.Calls, &ln.Failures, &ln.InputTokens, &ln.OutputTokens, &ln.CostUSD); err != nil {
			return s, err
		}
		s.Lines = append(s.Lines, ln)
	}
	return s, rows.Err()
}
