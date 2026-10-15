package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

var (
	_ ports.CaseRepository    = (*PostgresRepository)(nil)
	_ ports.TaskRepository    = (*PostgresRepository)(nil)
	_ ports.ContextRepository = (*PostgresRepository)(nil)
)

const caseColumns = `id, tenant_id, case_number, case_sequence, title, description, customer_id, category_id, priority, status, assigned_team_id, assigned_agent_id,
	origin_session_id, resolution, cancel_reason, created_by_user_id, opened_at, resolved_at, closed_at, cancelled_at, reopen_count,
	version, created_at, updated_at`

func scanCase(row pgx.Row) (*domain.Case, error) {
	c := &domain.Case{}
	var priority, status string
	if err := row.Scan(&c.ID, &c.TenantID, &c.CaseNumber, &c.CaseSequence, &c.Title, &c.Description, &c.CustomerID, &c.CategoryID, &priority, &status, &c.AssignedTeamID,
		&c.AssignedAgentID, &c.OriginSessionID, &c.Resolution, &c.CancelReason, &c.CreatedBy, &c.OpenedAt, &c.ResolvedAt, &c.ClosedAt, &c.CancelledAt, &c.ReopenCount,
		&c.Version, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Priority, c.Status = domain.Priority(priority), domain.CaseStatus(status)
	return c, nil
}

func (r *PostgresRepository) oneCase(ctx context.Context, where string, lock bool, args ...any) (*domain.Case, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	sql := `SELECT ` + caseColumns + ` FROM service_cases WHERE tenant_id = $1 AND ` + where
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanCase(r.q(ctx).QueryRow(ctx, sql, append([]any{tenant}, args...)...))
}

func (r *PostgresRepository) LockCase(ctx context.Context, id uuid.UUID) (*domain.Case, error) {
	c, err := r.oneCase(ctx, `id = $2`, true, id)
	return c, mapErr(err)
}

func (r *PostgresRepository) GetCase(ctx context.Context, id uuid.UUID) (*domain.Case, error) {
	c, err := r.oneCase(ctx, `id = $2`, false, id)
	return c, mapErr(err)
}

func (r *PostgresRepository) InsertCase(ctx context.Context, c *domain.Case) error {
	_, err := r.q(ctx).Exec(ctx, `
		INSERT INTO service_cases (id, tenant_id, case_number, case_sequence, title, description, customer_id, category_id, priority, status, assigned_team_id, assigned_agent_id,
			origin_session_id, created_by_user_id, opened_at, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		c.ID, c.TenantID, c.CaseNumber, c.CaseSequence, c.Title, c.Description, c.CustomerID, c.CategoryID, string(c.Priority), string(c.Status), c.AssignedTeamID, c.AssignedAgentID,
		c.OriginSessionID, c.CreatedBy, c.OpenedAt, c.Version, c.CreatedAt, c.UpdatedAt)
	return mapErr(err)
}

func (r *PostgresRepository) UpdateCase(ctx context.Context, c *domain.Case, expectedVersion int) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE service_cases SET title=$3, description=$4, customer_id=$5, category_id=$6, priority=$7, status=$8, assigned_team_id=$9, assigned_agent_id=$10,
			resolution=$11, cancel_reason=$12, resolved_at=$13, closed_at=$14, cancelled_at=$15, reopen_count=$16
		WHERE tenant_id=$1 AND id=$2 AND version=$17
		RETURNING version, updated_at`,
		tenant, c.ID, c.Title, c.Description, c.CustomerID, c.CategoryID, string(c.Priority), string(c.Status), c.AssignedTeamID, c.AssignedAgentID,
		c.Resolution, c.CancelReason, c.ResolvedAt, c.ClosedAt, c.CancelledAt, c.ReopenCount, expectedVersion)
	if err := row.Scan(&c.Version, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var one int
			if e := r.q(ctx).QueryRow(ctx, `SELECT 1 FROM service_cases WHERE tenant_id=$1 AND id=$2`, tenant, c.ID).Scan(&one); e != nil {
				return domain.ErrNotFound
			}
			return domain.ErrConflict
		}
		return mapErr(err)
	}
	return nil
}

func (r *PostgresRepository) LinkCaseSession(ctx context.Context, caseID, sessionID uuid.UUID, relation string, by *uuid.UUID, at time.Time) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	tag, err := r.q(ctx).Exec(ctx, `
		INSERT INTO service_case_sessions (tenant_id, case_id, session_id, relation, linked_by_user_id, linked_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, case_id, session_id) DO NOTHING`, tenant, caseID, sessionID, relation, by, at)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *PostgresRepository) SessionsOfCase(ctx context.Context, caseID uuid.UUID) ([]domain.Session, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	cols := make([]string, 0, 28)
	for _, c := range strings.Split(sessionColumns, ",") {
		cols = append(cols, "s."+strings.TrimSpace(c))
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM service_case_sessions l
		JOIN service_sessions s ON s.tenant_id = l.tenant_id AND s.id = l.session_id
		WHERE l.tenant_id = $1 AND l.case_id = $2 ORDER BY s.opened_at DESC, s.id DESC`, tenant, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) IsActiveMember(ctx context.Context, userID uuid.UUID) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	err = r.q(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE tenant_id=$1 AND user_id=$2 AND status='active')`, tenant, userID).Scan(&ok)
	return ok, err
}

func (r *PostgresRepository) ListCases(ctx context.Context, f ports.CaseFilter) ([]domain.Case, string, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, "", err
	}
	where := []string{"c.tenant_id = $1"}
	args := []any{tenant}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Status != "" {
		add("c.status = ?", string(f.Status))
	}
	if f.OnlyWorking {
		where = append(where, "c.status IN ('open','investigating','waiting')")
	}
	if f.CustomerID != nil {
		add("c.customer_id = ?", *f.CustomerID)
	}
	if f.AssignedAgentID != nil {
		add("c.assigned_agent_id = ?", *f.AssignedAgentID)
	}
	if f.AssignedTeamID != nil {
		add("c.assigned_team_id = ?", *f.AssignedTeamID)
	}
	if f.Priority != "" {
		add("c.priority = ?", string(f.Priority))
	}
	if f.CaseNumber != "" {
		add("c.case_number = ?", strings.ToUpper(strings.TrimSpace(f.CaseNumber)))
	}
	if f.SessionID != nil {
		add("EXISTS (SELECT 1 FROM service_case_sessions l WHERE l.tenant_id = c.tenant_id AND l.case_id = c.id AND l.session_id = ?)", *f.SessionID)
	}
	if f.Cursor != "" {
		t, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, t, id)
		where = append(where, fmt.Sprintf("(c.opened_at, c.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	cols := make([]string, 0, 26)
	for _, c := range strings.Split(caseColumns, ",") {
		cols = append(cols, "c."+strings.TrimSpace(c))
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM service_cases c WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY c.opened_at DESC, c.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.Case
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.OpenedAt, last.ID)
	}
	return out, next, nil
}

// ---- tasks ----

const taskColumns = `id, tenant_id, title, description, status, priority, assigned_user_id, assigned_team_id, due_at, completed_at, completed_by_user_id, cancelled_at,
	cancel_reason, origin_type, session_id, case_id, contact_id, flow_run_id, flow_seq, created_by_user_id, reopen_count, version,
	created_at, updated_at`

func scanTask(row pgx.Row) (*domain.Task, error) {
	t := &domain.Task{}
	var status, priority string
	if err := row.Scan(&t.ID, &t.TenantID, &t.Title, &t.Description, &status, &priority, &t.AssignedUserID, &t.AssignedTeamID, &t.DueAt, &t.CompletedAt, &t.CompletedBy,
		&t.CancelledAt, &t.CancelReason, &t.OriginType, &t.SessionID, &t.CaseID, &t.ContactID, &t.FlowRunID, &t.FlowSeq, &t.CreatedBy,
		&t.ReopenCount, &t.Version, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Status, t.Priority = domain.TaskStatus(status), domain.Priority(priority)
	return t, nil
}

func (r *PostgresRepository) oneTask(ctx context.Context, where string, lock bool, args ...any) (*domain.Task, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	sql := `SELECT ` + taskColumns + ` FROM service_tasks WHERE tenant_id = $1 AND ` + where
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanTask(r.q(ctx).QueryRow(ctx, sql, append([]any{tenant}, args...)...))
}

func (r *PostgresRepository) LockTask(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	t, err := r.oneTask(ctx, `id = $2`, true, id)
	return t, mapErr(err)
}

func (r *PostgresRepository) GetTask(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	t, err := r.oneTask(ctx, `id = $2`, false, id)
	return t, mapErr(err)
}

func (r *PostgresRepository) TaskByFlowEffect(ctx context.Context, runID uuid.UUID, seq int) (*domain.Task, error) {
	t, err := r.oneTask(ctx, `flow_run_id = $2 AND flow_seq = $3`, false, runID, seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (r *PostgresRepository) InsertTask(ctx context.Context, t *domain.Task) error {
	_, err := r.q(ctx).Exec(ctx, `
		INSERT INTO service_tasks (id, tenant_id, title, description, status, priority, assigned_user_id, assigned_team_id, due_at, origin_type, session_id, case_id, contact_id,
			flow_run_id, flow_seq, created_by_user_id, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		t.ID, t.TenantID, t.Title, t.Description, string(t.Status), string(t.Priority), t.AssignedUserID, t.AssignedTeamID, t.DueAt, t.OriginType, t.SessionID, t.CaseID, t.ContactID,
		t.FlowRunID, t.FlowSeq, t.CreatedBy, t.Version, t.CreatedAt, t.UpdatedAt)
	return mapErr(err)
}

func (r *PostgresRepository) UpdateTask(ctx context.Context, t *domain.Task, expectedVersion int) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE service_tasks SET title=$3, description=$4, status=$5, priority=$6, assigned_user_id=$7, assigned_team_id=$8, due_at=$9, completed_at=$10,
			completed_by_user_id=$11, cancelled_at=$12, cancel_reason=$13, reopen_count=$14
		WHERE tenant_id=$1 AND id=$2 AND version=$15
		RETURNING version, updated_at`,
		tenant, t.ID, t.Title, t.Description, string(t.Status), string(t.Priority), t.AssignedUserID, t.AssignedTeamID, t.DueAt, t.CompletedAt,
		t.CompletedBy, t.CancelledAt, t.CancelReason, t.ReopenCount, expectedVersion)
	if err := row.Scan(&t.Version, &t.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var one int
			if e := r.q(ctx).QueryRow(ctx, `SELECT 1 FROM service_tasks WHERE tenant_id=$1 AND id=$2`, tenant, t.ID).Scan(&one); e != nil {
				return domain.ErrNotFound
			}
			return domain.ErrConflict
		}
		return mapErr(err)
	}
	return nil
}

func (r *PostgresRepository) ListTasks(ctx context.Context, f ports.TaskFilter, now time.Time) ([]domain.Task, string, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, "", err
	}
	where := []string{"tenant_id = $1"}
	args := []any{tenant}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Status != "" {
		add("status = ?", string(f.Status))
	}
	if f.PendingOnly || f.OverdueOnly {
		where = append(where, "status IN ('open','in_progress')")
	}
	if f.OverdueOnly {
		add("due_at < ?", now)
	}
	if f.AssignedUserID != nil {
		add("assigned_user_id = ?", *f.AssignedUserID)
	}
	if f.AssignedTeamID != nil {
		add("assigned_team_id = ?", *f.AssignedTeamID)
	}
	if f.SessionID != nil {
		add("session_id = ?", *f.SessionID)
	}
	if f.CaseID != nil {
		add("case_id = ?", *f.CaseID)
	}
	if f.ContactID != nil {
		add("contact_id = ?", *f.ContactID)
	}
	if f.Cursor != "" {
		t, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, t, id)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.q(ctx).Query(ctx, `SELECT `+taskColumns+` FROM service_tasks WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// ---- context ----

func (r *PostgresRepository) OpenCasesForContext(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID, needsSelection bool, limit int) ([]domain.Case, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	cols := make([]string, 0, 26)
	for _, c := range strings.Split(caseColumns, ",") {
		cols = append(cols, "c."+strings.TrimSpace(c))
	}
	// a case is relevant through the contact's sessions, or directly through the selected customer; it never shows under a customer that was not chosen
	rows, err := r.q(ctx).Query(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM service_cases c
		WHERE c.tenant_id = $1 AND c.status IN ('open','investigating','waiting')
		  AND (
		    EXISTS (SELECT 1 FROM service_case_sessions l JOIN service_sessions s ON s.tenant_id = l.tenant_id AND s.id = l.session_id
		            WHERE l.tenant_id = c.tenant_id AND l.case_id = c.id AND s.contact_id = $2)
		    OR ($3::uuid IS NOT NULL AND c.customer_id = $3)
		  )
		  AND (c.customer_id IS NULL OR (NOT $4 AND ($3::uuid IS NULL OR c.customer_id = $3)))
		ORDER BY c.opened_at DESC, c.id DESC LIMIT $5`, tenant, contactID, customerID, needsSelection, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Case
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) PendingTasksForContext(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID, needsSelection bool, limit int) ([]domain.Task, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	cols := make([]string, 0, 26)
	for _, c := range strings.Split(taskColumns, ",") {
		cols = append(cols, "t."+strings.TrimSpace(c))
	}
	// the customer of a task is the one of its case, else of its session; a task with none is shown to everybody who sees the contact
	rows, err := r.q(ctx).Query(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM service_tasks t
		LEFT JOIN service_cases c ON c.tenant_id = t.tenant_id AND c.id = t.case_id
		LEFT JOIN service_sessions s ON s.tenant_id = t.tenant_id AND s.id = t.session_id
		WHERE t.tenant_id = $1 AND t.status IN ('open','in_progress')
		  AND (t.contact_id = $2 OR s.contact_id = $2)
		  AND (COALESCE(c.customer_id, s.customer_id) IS NULL OR (NOT $4 AND ($3::uuid IS NULL OR COALESCE(c.customer_id, s.customer_id) = $3)))
		ORDER BY t.due_at NULLS LAST, t.created_at DESC, t.id DESC LIMIT $5`, tenant, contactID, customerID, needsSelection, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

var _ ports.LinkCounter = (*PostgresRepository)(nil)

// CountLinks says how many operational links a session gathered: it refines the result of a plain finalization (service order > case > task).
func (r *PostgresRepository) CountLinks(ctx context.Context, sessionID uuid.UUID) (externalOrders, cases, tasks int, err error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	err = r.q(ctx).QueryRow(ctx, `SELECT
		(SELECT count(*) FROM service_case_sessions WHERE tenant_id = $1 AND session_id = $2),
		(SELECT count(*) FROM service_tasks WHERE tenant_id = $1 AND session_id = $2)`, tenant, sessionID).Scan(&cases, &tasks)
	return 0, cases, tasks, err
}
