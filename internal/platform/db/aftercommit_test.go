package db

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

// AfterCommit on a real PostgreSQL with the application role: a callback runs only when the session's transaction commits, once, in order, and never for work a
// rolled-back savepoint registered.
func TestAfterCommitRunsOnlyWhenTheSessionCommits(t *testing.T) {
	_, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	tenant := uuid.New()

	var ran []string
	record := func(name string) func(context.Context) { return func(context.Context) { ran = append(ran, name) } }

	t.Run("a session that fails runs nothing", func(t *testing.T) {
		ran = nil
		boom := errors.New("boom")
		err := WithTenantSession(ctx, app, uuid.Nil, true, func(c context.Context) error {
			if !AfterCommit(c, record("lost")) {
				t.Error("inside a session AfterCommit must register")
			}
			return boom
		})
		if !errors.Is(err, boom) || len(ran) != 0 {
			t.Fatalf("err=%v ran=%v, want boom and nothing", err, ran)
		}
	})

	t.Run("a committed session runs its callbacks once, in order, after the commit", func(t *testing.T) {
		ran = nil
		var duringFn atomic.Bool
		err := WithTenantSession(ctx, app, uuid.Nil, true, func(c context.Context) error {
			AfterCommit(c, record("first"))
			AfterCommit(c, record("second"))
			duringFn.Store(len(ran) == 0) // nothing runs while the transaction is still open
			return nil
		})
		if err != nil || !duringFn.Load() || len(ran) != 2 || ran[0] != "first" || ran[1] != "second" {
			t.Fatalf("err=%v during=%v ran=%v", err, duringFn.Load(), ran)
		}
	})

	t.Run("work a rolled-back savepoint registered is dropped, what the kept one registered stays", func(t *testing.T) {
		ran = nil
		err := WithTenantSession(ctx, app, uuid.Nil, true, func(c context.Context) error {
			AfterCommit(c, record("before"))
			_ = WithSavepoint(c, app, func(sc context.Context) error {
				AfterCommit(sc, record("rolled-back"))
				return errors.New("undo")
			})
			if err := WithSavepoint(c, app, func(sc context.Context) error {
				AfterCommit(sc, record("kept"))
				return nil
			}); err != nil {
				return err
			}
			return nil
		})
		if err != nil || len(ran) != 2 || ran[0] != "before" || ran[1] != "kept" {
			t.Fatalf("err=%v ran=%v, want [before kept]", err, ran)
		}
	})

	t.Run("a nested session commits and runs its own callbacks; the outer one's wait for the outer commit", func(t *testing.T) {
		ran = nil
		err := WithTenantSession(ctx, app, uuid.Nil, true, func(c context.Context) error {
			AfterCommit(c, record("outer"))
			if err := WithSystemTenantSession(c, app, tenant, func(n context.Context) error {
				AfterCommit(n, record("inner"))
				return nil
			}); err != nil {
				return err
			}
			if len(ran) != 1 || ran[0] != "inner" {
				t.Errorf("after the inner commit only the inner callback ran: %v", ran)
			}
			return nil
		})
		if err != nil || len(ran) != 2 || ran[1] != "outer" {
			t.Fatalf("err=%v ran=%v", err, ran)
		}
	})

	t.Run("a panicking callback does not stop the next one or change the outcome", func(t *testing.T) {
		ran = nil
		err := WithTenantSession(ctx, app, uuid.Nil, true, func(c context.Context) error {
			AfterCommit(c, func(context.Context) { panic("x") })
			AfterCommit(c, record("after-panic"))
			return nil
		})
		if err != nil || len(ran) != 1 {
			t.Fatalf("err=%v ran=%v", err, ran)
		}
	})

	t.Run("outside any session there is nothing to register on", func(t *testing.T) {
		if AfterCommit(ctx, record("x")) {
			t.Error("AfterCommit registered without a session")
		}
	})
}
