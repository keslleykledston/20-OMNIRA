package authn

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	testhelper "github.com/omnira/omnira/internal/testhelpers"
)

func TestPostgresProvisionIdentity(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()

	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	resolver := NewPostgresIdentityResolver(app)

	// Test: Provision new identity (JIT)
	issuer := "https://idp.test/realms/test"
	subject := "user-123"
	email := "user@test.local"
	displayName := "Test User"

	userID, err := resolver.ProvisionIdentity(ctx, issuer, subject, email, displayName)
	if err != nil {
		t.Fatalf("provision failed: %v", err)
	}
	if userID == uuid.Nil {
		t.Fatal("expected non-nil user ID")
	}

	// Verify user exists
	var verifyEmail, verifyName string
	err = platformdb.WithTenantSession(ctx, app, uuid.Nil, true, func(scoped context.Context) error {
		return platformdb.QuerierFromContext(scoped, app).QueryRow(scoped,
			`SELECT email, display_name FROM users WHERE id=$1`, userID).Scan(&verifyEmail, &verifyName)
	})
	if err != nil {
		t.Fatalf("user not found: %v", err)
	}
	if verifyEmail != email || verifyName != displayName {
		t.Errorf("user attributes mismatch: email=%s name=%s", verifyEmail, verifyName)
	}

	// Test: Resolve existing identity
	resolvedID, err := resolver.ResolveIdentity(ctx, issuer, subject)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolvedID != userID {
		t.Errorf("expected user ID %s, got %s", userID, resolvedID)
	}

	// Test: Provision same identity (idempotent)
	userID2, err := resolver.ProvisionIdentity(ctx, issuer, subject, "updated@test.local", "Updated Name")
	if err != nil {
		t.Fatalf("reprovision failed: %v", err)
	}
	if userID2 != userID {
		t.Errorf("expected same user ID, got %s", userID2)
	}

	// Verify attributes were updated
	err = platformdb.WithTenantSession(ctx, app, uuid.Nil, true, func(scoped context.Context) error {
		return platformdb.QuerierFromContext(scoped, app).QueryRow(scoped,
			`SELECT email, display_name FROM users WHERE id=$1`, userID).Scan(&verifyEmail, &verifyName)
	})
	if err != nil {
		t.Fatalf("user not found after update: %v", err)
	}
	if verifyEmail != "updated@test.local" || verifyName != "Updated Name" {
		t.Errorf("attributes not updated: email=%s name=%s", verifyEmail, verifyName)
	}

	// Test: Different issuer, same subject = different user
	subject2 := "user-123" // same subject
	issuer2 := "https://other-idp.test/realms/other"
	userID3, err := resolver.ProvisionIdentity(ctx, issuer2, subject2, "other@test.local", "Other User")
	if err != nil {
		t.Fatalf("provision with different issuer failed: %v", err)
	}
	if userID3 == userID {
		t.Error("different issuer should create new user")
	}

	// Cleanup
	_, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, userID, userID3)
}
