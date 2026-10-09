package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

// LockTenantActive answers "is the company active?" and keeps the answer true until the caller's transaction ends:
// the UPDATE that suspends the company must wait for it. That wait is what stops a background write that already
// decided to go ahead from landing after the suspension became visible.
func TestLockTenantActiveHoldsTheSuspensionBack(t *testing.T) {
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	tenant := uuid.New()
	if err := WithTenantSession(ctx, owner, uuid.Nil, true, func(c context.Context) error {
		_, err := QuerierFromContext(c, owner).Exec(c, `INSERT INTO tenants(id, legal_name, status) VALUES ($1, $2, 'active')`, tenant, tenant.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = WithTenantSession(context.Background(), owner, uuid.Nil, true, func(c context.Context) error {
			_, err := QuerierFromContext(c, owner).Exec(c, `DELETE FROM tenants WHERE id = $1`, tenant)
			return err
		})
	})

	t.Run("a company that does not exist is not active", func(t *testing.T) {
		if err := WithTenantSession(ctx, app, uuid.Nil, true, func(c context.Context) error {
			ok, err := LockTenantActive(c, QuerierFromContext(c, app), uuid.New())
			if err != nil || ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("the suspension waits for a transaction that asked first, then wins", func(t *testing.T) {
		holder, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(context.Background()) }()
		if _, err := holder.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
			t.Fatal(err)
		}
		ok, err := LockTenantActive(ctx, holder, tenant)
		if err != nil || !ok {
			t.Fatalf("active company: ok=%v err=%v", ok, err)
		}
		suspended := make(chan error, 1)
		go func() {
			suspended <- WithTenantSession(ctx, owner, uuid.Nil, true, func(c context.Context) error {
				_, err := QuerierFromContext(c, owner).Exec(c, `UPDATE tenants SET status = 'suspended' WHERE id = $1`, tenant)
				return err
			})
		}()
		select {
		case err := <-suspended:
			t.Fatalf("the suspension did not wait for the in-flight write (err=%v)", err)
		case <-time.After(1200 * time.Millisecond):
		}
		if err := holder.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-suspended:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the suspension never finished")
		}
		// from now on every new write finds the company suspended
		if err := WithTenantSession(ctx, app, uuid.Nil, true, func(c context.Context) error {
			ok, err := LockTenantActive(c, QuerierFromContext(c, app), tenant)
			if err != nil || ok {
				t.Fatalf("suspended company: ok=%v err=%v", ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
