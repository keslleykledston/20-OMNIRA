package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

func TestRequireUnprivilegedRole(t *testing.T) {
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := RequireUnprivilegedRole(ctx, owner); !errors.Is(err, ErrPrivilegedRole) {
		t.Fatalf("owner/superuser role must be rejected, got %v", err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := RequireUnprivilegedRole(ctx, app); err != nil {
		t.Fatalf("application role must be accepted: %v", err)
	}
}
