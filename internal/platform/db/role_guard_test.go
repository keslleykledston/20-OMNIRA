package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequireUnprivilegedRole(t *testing.T) {
	ownerURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if ownerURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
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
