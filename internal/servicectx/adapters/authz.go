package adapters

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Authorizer asks the database function that answers for a member (from the role matrix) AND for a Hub agent (from the live grant), never both.
type Authorizer struct{ pool *pgxpool.Pool }

func NewAuthorizer(pool *pgxpool.Pool) *Authorizer { return &Authorizer{pool: pool} }

func (a *Authorizer) Has(ctx context.Context, userID uuid.UUID, permission string) (bool, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return false, err
	}
	return tenancyadapters.ActorHasPermission(ctx, platformdb.QuerierFromContext(ctx, a.pool), tc.TenantID, userID, permission)
}
