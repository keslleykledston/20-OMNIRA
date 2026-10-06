package adapters

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/flowstest"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// platformdbSession runs fn in a fresh tenant session of user A and returns fn's error (unlike Env.AsUser, which fails the test).
func platformdbSession(ctx context.Context, env *flowstest.Env, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return platformdb.WithTenantSession(ctx, env.App, env.UserA, false, fn)
}

func withTenant(ctx context.Context, tenant, user uuid.UUID) context.Context {
	tc, _ := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
	return tenancydomain.WithTenantContext(ctx, tc)
}
