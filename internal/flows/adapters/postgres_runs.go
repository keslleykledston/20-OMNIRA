package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

var _ ports.RunRepository = (*PostgresFlowRepository)(nil)
var _ ports.VersionReader = (*PostgresFlowRepository)(nil)

// LoadConversation reads the conversation and takes a row lock on it, held until the surrounding transaction ends. Every
// event of one conversation is serialized by that lock; there is no global lock.
func (r *PostgresFlowRepository) LoadConversation(ctx context.Context, id uuid.UUID) (*ports.ConversationFacts, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var f ports.ConversationFacts
	var mode string
	err = r.q(ctx).QueryRow(ctx, `
		SELECT c.id, c.tenant_id, c.conversation_kind, c.status, c.automation_mode, c.assigned_to_user_id, c.queue_id,
		       c.channel_connection_id, COALESCE(cc.provider, ''),
		       EXISTS (SELECT 1 FROM identity_resolution_conflicts ic WHERE ic.tenant_id = c.tenant_id AND ic.contact_id = c.contact_id AND ic.status = 'open'), c.contact_id,
		       COALESCE(NULLIF(ct.alias, ''), NULLIF(ct.whatsapp_name, ''), ct.display_name, ''), COALESCE(ct.phone_e164, ''),
		       COALESCE(ct.kind, ''),
		       (SELECT r.active_customer_account_id FROM flow_runs r WHERE r.tenant_id = c.tenant_id AND r.conversation_id = c.id AND r.active_customer_account_id IS NOT NULL ORDER BY r.started_at DESC LIMIT 1)
		FROM conversations c
		LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id
		LEFT JOIN contacts ct ON ct.tenant_id = c.tenant_id AND ct.id = c.contact_id
		WHERE c.tenant_id = $1 AND c.id = $2
		FOR UPDATE OF c`, tenantID, id).
		Scan(&f.ID, &f.TenantID, &f.Kind, &f.Status, &mode, &f.AssignedTo, &f.QueueID, &f.ConnectionID, &f.Provider,
			&f.IdentityConflict, &f.ContactID, &f.ContactName, &f.ContactPhone, &f.ContactKind, &f.ActiveCustomerAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	f.AutomationMode = domain.AutomationMode(mode)
	return &f, err
}

func (r *PostgresFlowRepository) LoadInboundMessage(ctx context.Context, conversationID, messageID uuid.UUID) (*ports.InboundMessage, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var m ports.InboundMessage
	err = r.q(ctx).QueryRow(ctx, `SELECT id, body, created_at FROM messages WHERE tenant_id=$1 AND conversation_id=$2 AND id=$3 AND direction='inbound'`,
		tenantID, conversationID, messageID).Scan(&m.ID, &m.Text, &m.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return &m, err
}

const runColumns = `id, tenant_id, flow_id, flow_version_id, conversation_id, contact_id, active_customer_account_id, status,
	COALESCE(current_node_id, ''), variables, call_stack, wait_until, trigger_event_id, COALESCE(last_event_id, ''), node_exec_count,
	COALESCE(error, ''), started_at, updated_at, completed_at`

func scanRun(row pgx.Row) (*domain.FlowRun, error) {
	var r domain.FlowRun
	var vars, stack []byte
	err := row.Scan(&r.ID, &r.TenantID, &r.FlowID, &r.FlowVersionID, &r.ConversationID, &r.ContactID, &r.ActiveCustomerAccountID, &r.Status,
		&r.CurrentNodeID, &vars, &stack, &r.WaitUntil, &r.TriggerEventID, &r.LastEventID, &r.NodeExecCount, &r.Error, &r.StartedAt, &r.UpdatedAt, &r.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Variables = map[string]any{}
	if len(vars) > 0 {
		if err := json.Unmarshal(vars, &r.Variables); err != nil {
			return nil, fmt.Errorf("flows: corrupt run variables: %w", err)
		}
	}
	if len(stack) > 0 {
		if err := json.Unmarshal(stack, &r.CallStack); err != nil {
			return nil, fmt.Errorf("flows: corrupt call stack: %w", err)
		}
	}
	return &r, nil
}

func (r *PostgresFlowRepository) RunByEvent(ctx context.Context, eventID string) (*domain.FlowRun, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return scanRun(r.q(ctx).QueryRow(ctx, `SELECT `+runColumns+` FROM flow_runs WHERE tenant_id=$1 AND (trigger_event_id=$2 OR last_event_id=$2) LIMIT 1`, tenantID, eventID))
}

func (r *PostgresFlowRepository) ActiveRun(ctx context.Context, conversationID uuid.UUID) (*domain.FlowRun, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return scanRun(r.q(ctx).QueryRow(ctx, `SELECT `+runColumns+` FROM flow_runs WHERE tenant_id=$1 AND conversation_id=$2 AND status IN ('running','waiting_input','waiting_human') FOR UPDATE`, tenantID, conversationID))
}

func (r *PostgresFlowRepository) GetRun(ctx context.Context, id uuid.UUID) (*domain.FlowRun, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return scanRun(r.q(ctx).QueryRow(ctx, `SELECT `+runColumns+` FROM flow_runs WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

// CandidateFlows lists the flows that may start a run, in resolution order: specific flows by priority (lower first),
// then the default flow of the type. Channel filtering is applied by the engine (it is data-light and testable).
func (r *PostgresFlowRepository) CandidateFlows(ctx context.Context) ([]*domain.Flow, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+flowColumns+` FROM flows WHERE tenant_id=$1 AND status='published' AND flow_type='INBOUND' AND active_version_id IS NOT NULL
		ORDER BY is_default ASC, priority ASC, id ASC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Flow
	for rows.Next() {
		f, err := scanFlow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func mapRunErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "flow_runs_tenant_id_trigger_event_id_key":
			return domain.ErrDuplicateEvent
		case "flow_runs_one_active_per_conversation_uq":
			return domain.ErrConversationBusy
		}
	}
	return err
}

func (r *PostgresFlowRepository) CreateRun(ctx context.Context, run *domain.FlowRun) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if run.TenantID != tenantID {
		return errors.New("flows: run tenant differs from the session tenant")
	}
	vars, _ := json.Marshal(run.Variables)
	stack, _ := json.Marshal(run.CallStack)
	if len(run.CallStack) == 0 {
		stack = []byte("[]")
	}
	// A unique violation would abort the surrounding transaction; the savepoint keeps it usable for the caller's decision.
	q := r.q(ctx)
	if tx, ok := q.(pgx.Tx); ok {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = sp.Exec(ctx, createRunSQL, run.ID, run.TenantID, run.FlowID, run.FlowVersionID, run.ConversationID, run.ContactID, run.ActiveCustomerAccountID,
			run.Status, nullIfEmpty(run.CurrentNodeID), vars, stack, run.WaitUntil, run.TriggerEventID, nullIfEmpty(run.LastEventID), run.NodeExecCount, run.StartedAt, run.UpdatedAt)
		if err != nil {
			_ = sp.Rollback(ctx)
			return mapRunErr(err)
		}
		return sp.Commit(ctx)
	}
	_, err = q.Exec(ctx, createRunSQL, run.ID, run.TenantID, run.FlowID, run.FlowVersionID, run.ConversationID, run.ContactID, run.ActiveCustomerAccountID,
		run.Status, nullIfEmpty(run.CurrentNodeID), vars, stack, run.WaitUntil, run.TriggerEventID, nullIfEmpty(run.LastEventID), run.NodeExecCount, run.StartedAt, run.UpdatedAt)
	return mapRunErr(err)
}

const createRunSQL = `INSERT INTO flow_runs (id, tenant_id, flow_id, flow_version_id, conversation_id, contact_id, active_customer_account_id, status,
	current_node_id, variables, call_stack, wait_until, trigger_event_id, last_event_id, node_exec_count, started_at, updated_at)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *PostgresFlowRepository) SaveRun(ctx context.Context, run *domain.FlowRun) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	vars, _ := json.Marshal(run.Variables)
	stack, _ := json.Marshal(run.CallStack)
	if len(run.CallStack) == 0 {
		stack = []byte("[]")
	}
	tag, err := r.q(ctx).Exec(ctx, `UPDATE flow_runs SET status=$3, current_node_id=$4, variables=$5, call_stack=$6, wait_until=$7, last_event_id=$8,
		node_exec_count=$9, error=$10, updated_at=$11, completed_at=$12, active_customer_account_id=$13 WHERE tenant_id=$1 AND id=$2`,
		tenantID, run.ID, run.Status, nullIfEmpty(run.CurrentNodeID), vars, stack, run.WaitUntil, nullIfEmpty(run.LastEventID), run.NodeExecCount,
		nullIfEmpty(run.Error), run.UpdatedAt, run.CompletedAt, run.ActiveCustomerAccountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *PostgresFlowRepository) AppendExecution(ctx context.Context, e *domain.NodeExecution) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if e.TenantID != tenantID {
		return errors.New("flows: execution tenant differs from the session tenant")
	}
	_, err = r.q(ctx).Exec(ctx, `INSERT INTO flow_node_executions (id, tenant_id, flow_run_id, seq, flow_version_id, node_id, node_type, status, port, input, output, error, started_at, completed_at, duration_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		e.ID, e.TenantID, e.FlowRunID, e.Seq, e.FlowVersionID, e.NodeID, e.NodeType, e.Status, nullIfEmpty(e.Port), []byte(e.Input), []byte(e.Output), nullIfEmpty(e.Error),
		e.StartedAt, e.CompletedAt, e.DurationMs)
	return err
}

func (r *PostgresFlowRepository) ListExecutions(ctx context.Context, runID uuid.UUID) ([]*domain.NodeExecution, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT id, tenant_id, flow_run_id, seq, flow_version_id, node_id, node_type, status, COALESCE(port,''), input, output, COALESCE(error,''), started_at, completed_at, duration_ms
		FROM flow_node_executions WHERE tenant_id=$1 AND flow_run_id=$2 ORDER BY seq`, tenantID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.NodeExecution
	for rows.Next() {
		var e domain.NodeExecution
		if err := rows.Scan(&e.ID, &e.TenantID, &e.FlowRunID, &e.Seq, &e.FlowVersionID, &e.NodeID, &e.NodeType, &e.Status, &e.Port, &e.Input, &e.Output, &e.Error, &e.StartedAt, &e.CompletedAt, &e.DurationMs); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (r *PostgresFlowRepository) SetAutomationMode(ctx context.Context, conversationID uuid.UUID, mode domain.AutomationMode) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.q(ctx).Exec(ctx, `UPDATE conversations SET automation_mode=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, conversationID, string(mode))
	return err
}

// DueRun identifies a waiting run whose timeout passed. Listing is system-wide (a system-admin session); each run is then
// processed in its own tenant session.
type DueRun struct {
	TenantID uuid.UUID
	RunID    uuid.UUID
}

func (r *PostgresFlowRepository) DueRuns(ctx context.Context, now time.Time, limit int) ([]DueRun, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT r.tenant_id, r.id FROM flow_runs r JOIN tenants t ON t.id = r.tenant_id AND t.status = 'active'
		WHERE r.status='waiting_input' AND r.wait_until IS NOT NULL AND r.wait_until <= $1 ORDER BY r.wait_until LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueRun
	for rows.Next() {
		var d DueRun
		if err := rows.Scan(&d.TenantID, &d.RunID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// StrandedConversation is a conversation the bot holds although no run exists for it.
type StrandedConversation struct {
	TenantID       uuid.UUID
	ConversationID uuid.UUID
}

// StrandedConversations lists bot-held conversations without an active run that were last touched before cutoff
// (system-admin session; each is then released in its own tenant session).
func (r *PostgresFlowRepository) StrandedConversations(ctx context.Context, cutoff time.Time, limit int) ([]StrandedConversation, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT c.tenant_id, c.id FROM conversations c JOIN tenants t ON t.id = c.tenant_id AND t.status = 'active'
		WHERE c.automation_mode = 'bot' AND c.updated_at < $1
		  AND NOT EXISTS (SELECT 1 FROM flow_runs r WHERE r.tenant_id = c.tenant_id AND r.conversation_id = c.id AND r.status IN ('running','waiting_input','waiting_human'))
		ORDER BY c.updated_at LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrandedConversation
	for rows.Next() {
		var s StrandedConversation
		if err := rows.Scan(&s.TenantID, &s.ConversationID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CancelRunsOfClosedConversations frees the one-active-run slot of conversations closed while a run waited.
func (r *PostgresFlowRepository) CancelRunsOfClosedConversations(ctx context.Context) (int64, error) {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE flow_runs r SET status = 'cancelled', error = 'conversation closed', completed_at = now(), updated_at = now(), wait_until = NULL
		FROM conversations c
		WHERE c.tenant_id = r.tenant_id AND c.id = r.conversation_id AND c.status = 'closed'
		  AND r.status IN ('running','waiting_input','waiting_human')`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
