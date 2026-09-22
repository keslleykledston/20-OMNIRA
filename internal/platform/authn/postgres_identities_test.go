package authn

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
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

	userID, err := resolver.ProvisionIdentity(ctx, issuer, subject, email, displayName, false)
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
	userID2, err := resolver.ProvisionIdentity(ctx, issuer, subject, "updated@test.local", "Updated Name", false)
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
	userID3, err := resolver.ProvisionIdentity(ctx, issuer2, subject2, "other@test.local", "Other User", false)
	if err != nil {
		t.Fatalf("provision with different issuer failed: %v", err)
	}
	if userID3 == userID {
		t.Error("different issuer should create new user")
	}

	// Cleanup
	_, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, userID, userID3)
}

// Identity is (issuer, subject). Two IdPs may legitimately issue the same
// subject for different people, so resolution must never key on subject alone.
func TestIdentityIsScopedToIssuer(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()

	resolver := NewPostgresIdentityResolver(app)
	subject := "shared-subject-" + uuid.NewString()
	issuerA := "https://idp-a.test/realms/" + uuid.NewString()
	issuerB := "https://idp-b.test/realms/" + uuid.NewString()

	userA, err := resolver.ProvisionIdentity(ctx, issuerA, subject, "a@test.local", "User A", false)
	if err != nil {
		t.Fatalf("provision A: %v", err)
	}
	// Same identity again is idempotent.
	again, err := resolver.ProvisionIdentity(ctx, issuerA, subject, "a@test.local", "User A", false)
	if err != nil || again != userA {
		t.Fatalf("reprovision A: user=%s err=%v", again, err)
	}

	userB, err := resolver.ProvisionIdentity(ctx, issuerB, subject, "b@test.local", "User B", false)
	if err != nil {
		t.Fatalf("provision B: %v", err)
	}
	if userB == userA {
		t.Fatal("the same subject from a different issuer collapsed into one user")
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, userA, userB)
	})

	resolvedA, err := resolver.ResolveUserID(ctx, issuerA, subject)
	if err != nil || resolvedA != userA {
		t.Fatalf("resolve A: user=%s err=%v", resolvedA, err)
	}
	resolvedB, err := resolver.ResolveUserID(ctx, issuerB, subject)
	if err != nil || resolvedB != userB {
		t.Fatalf("resolve B: user=%s err=%v", resolvedB, err)
	}

	if _, err := resolver.ResolveUserID(ctx, "https://unknown-idp.test/realms/x", subject); err == nil {
		t.Fatal("an unknown issuer resolved to a user")
	}
	if _, err := resolver.ResolveUserID(ctx, issuerA, "no-such-subject-"+uuid.NewString()); err == nil {
		t.Fatal("an unknown subject resolved to a user")
	}
}

// email_verified is evidence from the IdP, refreshed on every login: it must be stored as
// asserted and must not survive a later login where the IdP no longer asserts it.
func TestProvisionIdentityStoresAndRefreshesEmailVerified(t *testing.T) {
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
	issuer, subject := "https://idp.test/realms/ev-"+uuid.NewString(), "sub-"+uuid.NewString()
	userID, err := resolver.ProvisionIdentity(ctx, issuer, subject, "ev@test.local", "EV", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })

	read := func() bool {
		var v bool
		if err := seed.QueryRow(ctx, `SELECT email_verified FROM user_identities WHERE issuer=$1 AND subject=$2`, issuer, subject).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !read() {
		t.Fatal("email_verified=true from the IdP was not stored")
	}
	if _, err := resolver.ProvisionIdentity(ctx, issuer, subject, "ev@test.local", "EV", false); err != nil {
		t.Fatal(err)
	}
	if read() {
		t.Fatal("email_verified must be refreshed to false when the IdP stops asserting it")
	}
}
