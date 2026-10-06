package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

var _ ports.RunReader = (*PostgresFlowRepository)(nil)

const runSummarySQL = `
	SELECT r.id, r.flow_id, f.slug, f.name, v.version, r.conversation_id, r.status, COALESCE(r.current_node_id, ''), r.node_exec_count,
	       COALESCE(r.error, ''), r.started_at, r.completed_at,
	       CASE WHEN r.completed_at IS NULL THEN NULL ELSE (extract(epoch FROM (r.completed_at - r.started_at)) * 1000)::bigint END
	FROM flow_runs r
	JOIN flows f ON f.tenant_id = r.tenant_id AND f.id = r.flow_id
	JOIN flow_versions v ON v.tenant_id = r.tenant_id AND v.id = r.flow_version_id`

func scanSummary(row pgx.Row) (*ports.RunSummary, error) {
	var s ports.RunSummary
	err := row.Scan(&s.ID, &s.FlowID, &s.FlowSlug, &s.FlowName, &s.FlowVersion, &s.ConversationID, &s.Status, &s.CurrentNodeID, &s.NodeExecCount, &s.Error, &s.StartedAt, &s.CompletedAt, &s.DurationMs)
	return &s, err
}

func (r *PostgresFlowRepository) ListRuns(ctx context.Context, f ports.RunFilter) ([]ports.RunSummary, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	rows, err := r.q(ctx).Query(ctx, runSummarySQL+`
		WHERE r.tenant_id = $1 AND ($2::uuid IS NULL OR r.flow_id = $2) AND ($3::uuid IS NULL OR r.conversation_id = $3) AND ($4 = '' OR r.status = $4)
		ORDER BY r.started_at DESC, r.id DESC LIMIT $5 OFFSET $6`, tenantID, f.FlowID, f.ConversationID, f.Status, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ports.RunSummary{}
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *PostgresFlowRepository) GetRunDetail(ctx context.Context, id uuid.UUID) (*ports.RunDetail, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	s, err := scanSummary(r.q(ctx).QueryRow(ctx, runSummarySQL+` WHERE r.tenant_id = $1 AND r.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &ports.RunDetail{RunSummary: *s, Variables: map[string]any{}, Executions: []ports.ExecutionView{}}
	var vars []byte
	if err := r.q(ctx).QueryRow(ctx, `SELECT variables FROM flow_runs WHERE tenant_id = $1 AND id = $2`, tenantID, id).Scan(&vars); err != nil {
		return nil, err
	}
	var all map[string]any
	_ = json.Unmarshal(vars, &all)
	if h, ok := all["_handoff"].(map[string]any); ok {
		// the summary interpolates what the contact typed: it is redacted like every other value that leaves the run
		if red, ok := domain.Redact(h).(map[string]any); ok {
			d.Handoff = red
		}
	}
	for k, v := range all {
		if len(k) > 0 && k[0] == '_' { // engine-private state ("_state", "_candidates", "_handoff")
			continue
		}
		d.Variables[k] = v
	}
	if red, ok := domain.Redact(d.Variables).(map[string]any); ok {
		d.Variables = red
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT seq, node_id, node_type, status, COALESCE(port, ''), input, output, COALESCE(error, ''), started_at, duration_ms
		FROM flow_node_executions WHERE tenant_id = $1 AND flow_run_id = $2 ORDER BY seq`, tenantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e ports.ExecutionView
		if err := rows.Scan(&e.Seq, &e.NodeID, &e.NodeType, &e.Status, &e.Port, &e.Input, &e.Output, &e.Error, &e.StartedAt, &e.DurationMs); err != nil {
			return nil, err
		}
		d.Executions = append(d.Executions, e)
	}
	return d, rows.Err()
}

func (r *PostgresFlowRepository) FlowAnalytics(ctx context.Context, flowID uuid.UUID, days int) (*ports.FlowAnalytics, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	var exists bool
	if err := r.q(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM flows WHERE tenant_id=$1 AND id=$2)`, tenantID, flowID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, domain.ErrNotFound
	}
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	a := &ports.FlowAnalytics{Since: since, Days: days, ByStatus: map[string]int{}, NodeErrors: []ports.NodeCount{}, DropOff: []ports.NodeCount{}}
	rows, err := r.q(ctx).Query(ctx, `SELECT status, count(*) FROM flow_runs WHERE tenant_id=$1 AND flow_id=$2 AND started_at >= $3 GROUP BY status`, tenantID, flowID, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		a.ByStatus[st] = n
		a.Runs += n
	}
	rows.Close()
	a.Completed, a.Failed = a.ByStatus["completed"], a.ByStatus["failed"]
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(DISTINCT r.id) FROM flow_runs r JOIN flow_node_executions e ON e.tenant_id=r.tenant_id AND e.flow_run_id=r.id AND e.node_type='human_handoff'
		WHERE r.tenant_id=$1 AND r.flow_id=$2 AND r.started_at >= $3`, tenantID, flowID, since).Scan(&a.HumanHandoff); err != nil {
		return nil, err
	}
	var avg *float64
	if err := r.q(ctx).QueryRow(ctx, `SELECT avg(extract(epoch FROM (completed_at - started_at)) * 1000) FROM flow_runs
		WHERE tenant_id=$1 AND flow_id=$2 AND started_at >= $3 AND status='completed' AND completed_at IS NOT NULL`, tenantID, flowID, since).Scan(&avg); err != nil {
		return nil, err
	}
	if avg != nil {
		v := int64(*avg)
		a.AvgDuration = &v
	}
	if rows, err = r.q(ctx).Query(ctx, `SELECT e.node_id, e.node_type, count(*) FROM flow_node_executions e JOIN flow_runs r ON r.tenant_id=e.tenant_id AND r.id=e.flow_run_id
		WHERE r.tenant_id=$1 AND r.flow_id=$2 AND r.started_at >= $3 AND e.status='failed' GROUP BY e.node_id, e.node_type ORDER BY count(*) DESC, e.node_id LIMIT 10`, tenantID, flowID, since); err != nil {
		return nil, err
	}
	for rows.Next() {
		var c ports.NodeCount
		if err := rows.Scan(&c.NodeID, &c.NodeType, &c.Count); err != nil {
			rows.Close()
			return nil, err
		}
		a.NodeErrors = append(a.NodeErrors, c)
	}
	rows.Close()
	if rows, err = r.q(ctx).Query(ctx, `SELECT COALESCE(current_node_id, '(start)'), count(*) FROM flow_runs
		WHERE tenant_id=$1 AND flow_id=$2 AND started_at >= $3 AND status IN ('failed','cancelled','expired') GROUP BY 1 ORDER BY count(*) DESC, 1 LIMIT 10`, tenantID, flowID, since); err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c ports.NodeCount
		if err := rows.Scan(&c.NodeID, &c.Count); err != nil {
			return nil, err
		}
		a.DropOff = append(a.DropOff, c)
	}
	return a, rows.Err()
}
