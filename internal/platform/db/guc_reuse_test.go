package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Regression: a pooled connection that ran a tenant/system session keeps the transaction-local
// GUCs as ” afterwards; the RLS helper functions must treat that as "unset", not raise 22P02.
func TestRLSHelpersSurviveConnectionReuse(t *testing.T) {
	appURL := os.Getenv("OMNIRA_APP_DATABASE_URL")
	if appURL == "" {
		t.Skip("OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1 // every statement below reuses the same physical connection
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// A system session and a user session set the GUCs (transaction-local) ...
	if err := WithTenantSession(ctx, pool, uuid.Nil, true, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := WithTenantSession(ctx, pool, uuid.New(), false, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// ... and the next, unscoped use of the connection must still evaluate the helpers.
	var isSystem bool
	var userID *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT is_system_admin(), current_user_id()`).Scan(&isSystem, &userID); err != nil {
		t.Fatalf("RLS helpers failed on a reused connection: %v", err)
	}
	if isSystem || userID != nil {
		t.Fatalf("leaked session state: is_system_admin=%v current_user_id=%v", isSystem, userID)
	}
	// A non-member session on the reused connection is denied (0 rows), not an error.
	if err := WithTenantSession(ctx, pool, uuid.New(), false, func(sc context.Context) error {
		var n int
		return QuerierFromContext(sc, pool).QueryRow(sc, `SELECT count(*) FROM tenants`).Scan(&n)
	}); err != nil {
		t.Fatalf("non-member query on reused connection: %v", err)
	}
}
