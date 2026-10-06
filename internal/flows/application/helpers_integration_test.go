package application_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/flowstest"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// platformdbSystem is one worker transaction: a system session scoped to one tenant derived from trusted state.
func platformdbSystem(ctx context.Context, env *flowstest.Env, tenant uuid.UUID, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return platformdb.WithSystemTenantSession(ctx, env.App, tenant, fn)
}

// platformdbSystemAdmin is the cross-tenant scan session the sweeper uses (system admin, no tenant).
func platformdbSystemAdmin(ctx context.Context, env *flowstest.Env, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return platformdb.WithTenantSession(ctx, env.App, uuid.Nil, true, fn)
}
