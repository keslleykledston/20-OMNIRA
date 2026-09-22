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
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx,
		`SELECT assigned_to_user_id FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
		tenantID, conversationID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("routing: lock conversation: %w", err)
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
		   AND qm.active AND qm.available AND (SELECT count(*) FROM conversations active WHERE active.tenant_id=qm.tenant_id AND active.queue_id=qm.queue_id AND active.assigned_to_user_id=qm.user_id AND active.status='open') < qm.capacity
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
