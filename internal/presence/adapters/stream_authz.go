package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// StreamAuthorizer authorizes a supervisor for tenant-wide presence
// visibility (agent.read) in a short transaction, never holding a connection
// for the SSE stream's lifetime — same discipline as the inbox realtime
// stream's sessionStreamAuthorizer (internal/platform/httpserver/server.go).
type StreamAuthorizer struct {
	pool  *pgxpool.Pool
	authz *tenancyapplication.AuthorizationService
}

func NewStreamAuthorizer(pool *pgxpool.Pool, authz *tenancyapplication.AuthorizationService) *StreamAuthorizer {
	return &StreamAuthorizer{pool: pool, authz: authz}
}

func (a *StreamAuthorizer) Authorize(ctx context.Context, userID, tenantID uuid.UUID) (*tenancydomain.TenantContext, error) {
	var tc *tenancydomain.TenantContext
	var allowed bool
	err := platformdb.WithTenantSession(ctx, a.pool, userID, false, func(scoped context.Context) error {
		var authErr error
		tc, authErr = a.authz.AuthorizeAccessToTenant(scoped, tenantID, userID)
		if authErr != nil {
			return authErr
		}
		return platformdb.QuerierFromContext(scoped, a.pool).QueryRow(scoped, `
			SELECT EXISTS(
			  SELECT 1 FROM memberships m
			  JOIN role_permissions rp ON rp.role_id = m.role_id
			  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key='agent.read')`,
			tenantID, userID).Scan(&allowed)
	})
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errors.New("presence: agent.read required")
	}
	return tc, nil
}
