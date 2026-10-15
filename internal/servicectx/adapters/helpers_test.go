package adapters_test

import (
	"context"
	"time"

	"github.com/google/uuid"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/application"
)

// execInSession runs a statement on the request's tenant-scoped transaction (the one the session middleware would hand a repository).
func execInSession(ctx context.Context, sql string, args ...any) error {
	_, err := platformdb.QuerierFromContext(ctx, nil).Exec(ctx, sql, args...)
	return err
}

// newTight builds a service whose reopen window is a nanosecond, to test the window.
func newTight(s *stack) (*application.Service, *application.Catalog, *adapters.Handler) {
	svc, cat, h := adapters.Build(s.env.App)
	svc.WithReopenWindow(time.Nanosecond)
	return svc, cat, h
}

// platformQuerier is the request transaction a repository would use.
func platformQuerier(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, nil)
}

// trySystem runs fn the way the worker does (a system session scoped to one company) and returns its error; the transaction is rolled back when fn fails.
func (s *stack) trySystem(tenant uuid.UUID, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return platformdb.WithSystemTenantSession(ctx, s.env.App, tenant, fn)
}
