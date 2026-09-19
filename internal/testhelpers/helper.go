package testing

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateTenantWithRLS creates a tenant in the database using system admin context to bypass RLS policies.
// Must be called from tests that use seed/admin pool (omnira role).
func CreateTenantWithRLS(ctx context.Context, seedPool *pgxpool.Pool, tenantID uuid.UUID, legalName string) error {
	conn, err := seedPool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenantID, legalName); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ExecInTenant executes a SQL statement within a tenant admin context (for test setup).
func ExecInTenant(ctx context.Context, seedPool *pgxpool.Pool, sql string, args ...any) error {
	conn, err := seedPool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// QueryInTenant queries within a tenant admin context (for test assertions).
func QueryInTenant(ctx context.Context, seedPool *pgxpool.Pool, sql string, args ...any) (any, error) {
	conn, err := seedPool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, sql, args...)
	_ = tx.Commit(ctx)
	return row, nil
}
