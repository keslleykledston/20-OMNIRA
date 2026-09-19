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
	var ok bool
	err = db.QuerierFromContext(ctx, c.pool).QueryRow(ctx, `
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
	return a.repo.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tc.TenantID, ActorID: tc.ActorID,
		Action: auditdomain.AuditAction(action), ResourceType: auditdomain.ResourceType(resourceType), ResourceID: resourceID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: meta, CreatedAt: time.Now().UTC(),
	})
}
