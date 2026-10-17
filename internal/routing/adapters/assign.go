package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PostgresConversationAssigner runs inside the request's tenant session
// (SET LOCAL app.current_user_id + RLS); it never opens its own transaction.
type PostgresConversationAssigner struct{ pool *pgxpool.Pool }

var _ ports.ConversationAssigner = (*PostgresConversationAssigner)(nil)

func NewPostgresConversationAssigner(pool *pgxpool.Pool) *PostgresConversationAssigner {
	return &PostgresConversationAssigner{pool: pool}
}

func tenantOf(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("routing: tenant context required")
	}
	return tc.TenantID, nil
}

func (r *PostgresConversationAssigner) LockAssignee(ctx context.Context, conversationID uuid.UUID) (*uuid.UUID, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, false, err
	}
	var current *uuid.UUID
	var held bool
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx,
		`SELECT assigned_to_user_id, COALESCE(control_kind IN ('crm_application','crm_agent_verified'), false) FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
		tenantID, conversationID).Scan(&current, &held)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("routing: lock conversation: %w", err)
	}
	if held {
		return nil, true, ports.ErrHeldByExternal
	}
	return current, true, nil
}

func (r *PostgresConversationAssigner) SetAssignee(ctx context.Context, c ports.AssignmentChange) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	q := platformdb.QuerierFromContext(ctx, r.pool)
	tag, err := q.Exec(ctx, `
		UPDATE conversations
		SET assigned_to_user_id=$3, assigned_at=CASE WHEN $3::uuid IS NULL THEN NULL ELSE now() END, updated_at=now()
		WHERE tenant_id=$1 AND id=$2`, tenantID, c.ConversationID, c.To)
	if err != nil {
		return fmt.Errorf("routing: update assignee: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("routing: conversation not updated")
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason)
		VALUES($1,$2,$3,$4,$5,$6)`, tenantID, c.ConversationID, c.From, c.To, c.Actor, c.Reason); err != nil {
		return fmt.Errorf("routing: record assignment history: %w", err)
	}
	return nil
}

// HasPermission resolves the user's active membership role in the context
// tenant against role_permissions (the existing RBAC tables).
func (r *PostgresConversationAssigner) HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tenantID, userID, permission).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("routing: check permission: %w", err)
	}
	return ok, nil
}

// IsActiveAgent: active membership plus active agent profile (self-claim; no queue availability/capacity rule).
func (r *PostgresConversationAssigner) IsActiveAgent(ctx context.Context, userID uuid.UUID) (bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM memberships m
		 JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id AND ap.status='active'
		 WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active')`, tenantID, userID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("routing: check active agent: %w", err)
	}
	return ok, nil
}

// IsEligibleForConversation aligns manual routing with round-robin: active
// membership/profile plus queue-local availability and existing capacity.
func (r *PostgresConversationAssigner) IsEligibleForConversation(ctx context.Context, conversationID, userID uuid.UUID) (bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM conversations c
		 JOIN memberships m ON m.tenant_id=c.tenant_id AND m.user_id=$3 AND m.status='active'
		 JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id AND ap.status='active'
		 WHERE c.tenant_id=$1 AND c.id=$2 AND (c.queue_id IS NULL OR EXISTS (
		   SELECT 1 FROM queue_members qm WHERE qm.tenant_id=c.tenant_id AND qm.queue_id=c.queue_id AND qm.user_id=$3
		   AND qm.active AND qm.available AND (qm.capacity = 0 OR (SELECT count(*) FROM conversations active WHERE active.tenant_id=qm.tenant_id AND active.queue_id=qm.queue_id AND active.assigned_to_user_id=qm.user_id AND active.status='open') < qm.capacity)
		 )))`, tenantID, conversationID, userID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("routing: check operational eligibility: %w", err)
	}
	return ok, nil
}

// AuditRecorder writes conversation.assigned / conversation.unassigned
// through the existing audit repository, in the same transaction.
type AuditRecorder struct {
	repo auditports.AuditEventRepository
}

var _ ports.AuditRecorder = (*AuditRecorder)(nil)

func NewAuditRecorder(repo auditports.AuditEventRepository) *AuditRecorder {
	return &AuditRecorder{repo: repo}
}

func (a *AuditRecorder) ConversationAssignmentChanged(ctx context.Context, c ports.AssignmentChange) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	action := auditdomain.ActionConversationUnassigned
	if c.To != nil {
		action = auditdomain.ActionConversationAssigned
	}
	meta := map[string]interface{}{"reason": c.Reason}
	if c.From != nil {
		meta["previous_assignee"] = c.From.String()
	}
	if c.To != nil {
		meta["new_assignee"] = c.To.String()
	}
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tenantID, ActorID: c.Actor, Action: action,
		ResourceType: auditdomain.ResourceConversation, ResourceID: c.ConversationID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: meta, CreatedAt: time.Now().UTC(),
	})
}

func (a *AuditRecorder) ParticipantInvited(ctx context.Context, conversationID, actor, targetUser uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tenantID, ActorID: actor, Action: auditdomain.ActionParticipantInvited,
		ResourceType: auditdomain.ResourceConversation, ResourceID: conversationID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: map[string]interface{}{"target_user": targetUser.String()}, CreatedAt: time.Now().UTC(),
	})
}

func (a *AuditRecorder) ParticipantAccepted(ctx context.Context, conversationID, actor uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tenantID, ActorID: actor, Action: auditdomain.ActionParticipantAccepted,
		ResourceType: auditdomain.ResourceConversation, ResourceID: conversationID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: map[string]interface{}{}, CreatedAt: time.Now().UTC(),
	})
}

func (a *AuditRecorder) ParticipantRejected(ctx context.Context, conversationID, actor uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tenantID, ActorID: actor, Action: auditdomain.ActionParticipantRejected,
		ResourceType: auditdomain.ResourceConversation, ResourceID: conversationID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: map[string]interface{}{}, CreatedAt: time.Now().UTC(),
	})
}

func (a *AuditRecorder) ParticipantLeft(ctx context.Context, conversationID, actor uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tenantID, ActorID: actor, Action: auditdomain.ActionParticipantLeft,
		ResourceType: auditdomain.ResourceConversation, ResourceID: conversationID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: map[string]interface{}{}, CreatedAt: time.Now().UTC(),
	})
}

func (a *AuditRecorder) ConversationTransferred(ctx context.Context, conversationID, actor uuid.UUID, from, to *uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	meta := map[string]interface{}{}
	if from != nil {
		meta["from_participant"] = from.String()
	}
	if to != nil {
		meta["to_participant"] = to.String()
	}
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tenantID, ActorID: actor, Action: auditdomain.ActionConversationTransferred,
		ResourceType: auditdomain.ResourceConversation, ResourceID: conversationID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: meta, CreatedAt: time.Now().UTC(),
	})
}

var _ ports.QueueRouter = (*PostgresConversationAssigner)(nil)

// MoveToQueue sends the conversation to another queue (the person's decision). Under the row lock: refuses what is held by an external application, closed or with
// staff; the queue must exist in this company and its sector must be on; a holder is released with history; the queue is written and the routing job is queued so the
// engine distributes it inside the new queue. Moving to the queue it is already in, with nobody holding it, changes nothing.
func (r *PostgresConversationAssigner) MoveToQueue(ctx context.Context, conversationID, queueID, actor uuid.UUID) (ports.QueueMove, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ports.QueueMove{}, false, err
	}
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var fromQueue, holder, staff *uuid.UUID
	var status string
	var held bool
	err = q.QueryRow(ctx, `SELECT queue_id, assigned_to_user_id, internal_user_id, status, COALESCE(control_kind IN ('crm_application','crm_agent_verified'), false)
		FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, conversationID).Scan(&fromQueue, &holder, &staff, &status, &held)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.QueueMove{}, false, nil
	}
	if err != nil {
		return ports.QueueMove{}, false, fmt.Errorf("routing: lock conversation: %w", err)
	}
	if held {
		return ports.QueueMove{}, true, ports.ErrHeldByExternal
	}
	if status != "open" || staff != nil {
		return ports.QueueMove{}, true, ports.ErrNotRoutable
	}
	var mode string
	var sectorOn bool
	err = q.QueryRow(ctx, `SELECT qq.mode, COALESCE(sc.active, true) FROM queues qq LEFT JOIN sectors sc ON sc.tenant_id=qq.tenant_id AND sc.id=qq.sector_id
		WHERE qq.tenant_id=$1 AND qq.id=$2`, tenantID, queueID).Scan(&mode, &sectorOn)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !sectorOn) {
		return ports.QueueMove{}, true, ports.ErrQueueUnavailable
	}
	if err != nil {
		return ports.QueueMove{}, true, fmt.Errorf("routing: look up queue: %w", err)
	}
	move := ports.QueueMove{FromQueue: fromQueue, ToQueue: queueID, PreviousAssignee: holder}
	if holder == nil && fromQueue != nil && *fromQueue == queueID {
		return move, true, nil
	}
	move.Changed = true
	if holder != nil {
		if _, err := q.Exec(ctx, `UPDATE conversations SET assigned_to_user_id=NULL, assigned_at=NULL, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, conversationID); err != nil {
			return ports.QueueMove{}, true, fmt.Errorf("routing: release holder: %w", err)
		}
		if _, err := q.Exec(ctx, `INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason) VALUES($1,$2,$3,NULL,$4,'queue_transfer')`,
			tenantID, conversationID, holder, actor); err != nil {
			return ports.QueueMove{}, true, fmt.Errorf("routing: record assignment history: %w", err)
		}
	}
	if _, err := q.Exec(ctx, `UPDATE conversations SET queue_id=$3, updated_at=now(),
		routing_retry_at = CASE WHEN $4 IN ('round_robin','least_loaded') THEN now() + interval '90 seconds' ELSE NULL END
		WHERE tenant_id=$1 AND id=$2`, tenantID, conversationID, queueID, mode); err != nil {
		return ports.QueueMove{}, true, fmt.Errorf("routing: set queue: %w", err)
	}
	// the same job a new conversation gets: the engine (or the legacy distributor) places it inside the new queue
	if _, err := q.Exec(ctx, `INSERT INTO outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
		VALUES (uuid_generate_v4(), $1, 'job.routing.assign.v1', 'conversation', $2::text, uuid_generate_v4(), '{}'::jsonb)`, tenantID, conversationID); err != nil {
		return ports.QueueMove{}, true, fmt.Errorf("routing: queue the routing job: %w", err)
	}
	return move, true, nil
}

var _ ports.QueueAuditRecorder = (*AuditRecorder)(nil)

func (a *AuditRecorder) ConversationQueueChanged(ctx context.Context, conversationID, actor uuid.UUID, from *uuid.UUID, to uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	meta := map[string]interface{}{"new_queue": to.String()}
	if from != nil {
		meta["previous_queue"] = from.String()
	}
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tenantID, ActorID: actor, Action: auditdomain.ActionConversationQueueChanged,
		ResourceType: auditdomain.ResourceConversation, ResourceID: conversationID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: meta, CreatedAt: time.Now().UTC(),
	})
}
