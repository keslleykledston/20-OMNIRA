package adapters_test

import (
	"context"
	"time"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
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
	svc, cat, h := adapters.Build(s.env.App, auditadapters.NewPostgresAuditEventRepository(s.env.App))
	svc.WithReopenWindow(time.Nanosecond)
	return svc, cat, h
}
