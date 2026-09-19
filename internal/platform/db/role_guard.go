package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPrivilegedRole means the runtime pool connects as a role that bypasses
// Row Level Security.
var ErrPrivilegedRole = errors.New("db: runtime role bypasses RLS")

// RequireUnprivilegedRole refuses a pool whose current role is a superuser or
// has BYPASSRLS. Such a role ignores every tenant-isolation policy (even
// FORCE ROW LEVEL SECURITY), silently turning RLS into a no-op. The runtime
// must connect as the application role (omnira_app); migrations run as the
// owner through a separate path.
func RequireUnprivilegedRole(ctx context.Context, pool *pgxpool.Pool) error {
	var name string
	var super, bypass bool
	err := pool.QueryRow(ctx, `
		SELECT r.rolname, r.rolsuper, r.rolbypassrls
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&name, &super, &bypass)
	if err != nil {
		return fmt.Errorf("db: inspect runtime role: %w", err)
	}
	if super || bypass {
		return fmt.Errorf("%w: role %q (superuser=%t, bypassrls=%t); use the application role (omnira_app) in OMNIRA_DATABASE_URL", ErrPrivilegedRole, name, super, bypass)
	}
	return nil
}
