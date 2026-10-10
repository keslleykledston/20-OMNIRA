package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PostgresPermissionChecker resolves role permissions of the actor's active
// membership in the TenantContext tenant (existing RBAC tables).
type PostgresPermissionChecker struct{ pool *pgxpool.Pool }

var _ ports.PermissionChecker = (*PostgresPermissionChecker)(nil)

func NewPostgresPermissionChecker(pool *pgxpool.Pool) *PostgresPermissionChecker {
	return &PostgresPermissionChecker{pool: pool}
}

func (c *PostgresPermissionChecker) HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return false, errors.New("channel: tenant context required")
	}
	q := db.QuerierFromContext(ctx, c.pool)
	// Management delegated by the Hub (ADR-0038 phase 3): the answer comes from the contract's scope and the manager's role/grant,
	// asked of the database NOW, in the request's own session. The person's tenant role means nothing here, and a tenant-role
	// permission other than the two management ones is never granted this way.
	if tc.Source == tenancydomain.AccessSourceHubManage {
		if userID != tc.ActorID || tc.HubID == nil {
			return false, nil
		}
		var scope string
		switch permission {
		case application.PermissionChannelManage:
			scope = "channels"
		case application.PermissionIntegrationManage:
			scope = "integrations"
		default:
			return false, nil
		}
		var ok bool
		if err := q.QueryRow(ctx, `SELECT has_hub_manage_access($1, $2, $3, $4)`, tc.TenantID, userID, scope, *tc.HubID).Scan(&ok); err != nil {
			return false, fmt.Errorf("channel: check hub management: %w", err)
		}
		return ok, nil
	}
	// A Hub agent attending the instance (ADR-0040): only the keys the grant AND the contract's ceiling hold, asked of the database now. The person's
	// tenant role (if they even have one) means nothing here, and a key that is not delegable is simply never held.
	if tc.Source == tenancydomain.AccessSourceHubServe {
		if userID != tc.ActorID {
			return false, nil
		}
		var ok bool
		if err := q.QueryRow(ctx, `SELECT actor_has_permission($1, $2, $3)`, tc.TenantID, userID, permission).Scan(&ok); err != nil {
			return false, fmt.Errorf("channel: check delegated permission: %w", err)
		}
		return ok, nil
	}
	// Members: managing an ERP/CRM connection is the same role permission as managing a channel (nothing changed for them).
	if permission == application.PermissionIntegrationManage {
		permission = application.PermissionChannelManage
	}
	var ok bool
	err = q.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, userID, permission).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("channel: check permission: %w", err)
	}
	return ok, nil
}

// ChannelAuditRecorder writes channel management events through the existing
// audit repository, in the request transaction.
type ChannelAuditRecorder struct {
	repo auditports.AuditEventRepository
}

var _ ports.ChannelAudit = (*ChannelAuditRecorder)(nil)

func NewChannelAuditRecorder(repo auditports.AuditEventRepository) *ChannelAuditRecorder {
	return &ChannelAuditRecorder{repo: repo}
}

func (a *ChannelAuditRecorder) Record(ctx context.Context, action, resourceType string, resourceID uuid.UUID, meta map[string]any) error {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return errors.New("channel: tenant context required")
	}
	if tc.Source == tenancydomain.AccessSourceHubManage && tc.HubID != nil {
		// who acted and through which Hub is part of the trail (the actor is a Hub person, not a member of this company)
		merged := make(map[string]any, len(meta)+2)
		for k, v := range meta {
			merged[k] = v
		}
		merged["via"] = "hub"
		merged["hub_id"] = tc.HubID.String()
		meta = merged
	}
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tc.TenantID, ActorID: tc.ActorID,
		Action: auditdomain.AuditAction(action), ResourceType: auditdomain.ResourceType(resourceType), ResourceID: resourceID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: meta, CreatedAt: time.Now().UTC(),
	})
}
