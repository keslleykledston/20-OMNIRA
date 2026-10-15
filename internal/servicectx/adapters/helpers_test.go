package adapters_test

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/gatewaytest"
)

// execInSession runs a statement on the request's tenant-scoped transaction (the one the session middleware would hand a repository).
func execInSession(ctx context.Context, sql string, args ...any) error {
	_, err := platformdb.QuerierFromContext(ctx, nil).Exec(ctx, sql, args...)
	return err
}

// newTight builds a service whose reopen window is a nanosecond, to test the window.
func newTight(s *stack) (*application.Service, *application.Catalog, *adapters.Handler) {
	svc, cat, h := adapters.Build(s.env.App, adapters.Options{ExternalOrders: true})
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

// adapters_BuildBare builds the module with NO gateway adapter (the default of a deployment that has none).
func adapters_BuildBare(s *stack) (*application.Service, *application.Catalog, *adapters.Handler) {
	return adapters.Build(s.env.App, adapters.Options{})
}

// newOffMux is the HTTP surface with the orders switch off.
func newOffMux(s *stack) *http.ServeMux {
	_, _, h := adapters.Build(s.env.App, adapters.Options{ExternalOrders: false, Gateways: gatewaytest.Resolver{F: s.gw}})
	return muxFor(s, h)
}

// ageRequest makes a request look d older. created_at is identity (a trigger refuses to change it), so the trigger is switched off for this one test transaction.
func (s *stack) ageRequest(id uuid.UUID, d time.Duration) {
	s.t.Helper()
	ctx := context.Background()
	tx, err := s.env.Seed.Begin(ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		s.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE external_record_requests SET created_at = created_at - make_interval(secs => $2) WHERE id=$1`, id, d.Seconds()); err != nil {
		s.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		s.t.Fatal(err)
	}
}

// noTriggers runs one statement as the owner with the triggers switched off, to craft a state the application can never reach.
func (s *stack) noTriggers(sql string, args ...any) {
	s.t.Helper()
	ctx := context.Background()
	tx, err := s.env.Seed.Begin(ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		s.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		s.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		s.t.Fatal(err)
	}
}

// forceRequestState crafts a request state the application would never go back to.
func (s *stack) forceRequestState(id uuid.UUID, state string) {
	s.t.Helper()
	s.noTriggers(`UPDATE external_record_requests SET state=$2, failure_class=CASE WHEN $2 = 'in_flight' THEN NULL ELSE failure_class END WHERE id=$1`, id, state)
}
