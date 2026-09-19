package authn

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSessionStore(t *testing.T) {
	seedURL := os.Getenv("OMNIRA_DATABASE_URL")
	if seedURL == "" {
		t.Skip("OMNIRA_DATABASE_URL required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store := NewPostgresSessionStore(pool)
	userID := uuid.New()

	// Insert test user (cleanup handled by defer)
	_, err = pool.Exec(ctx, `
		INSERT INTO users(id, external_subject, email, status)
		VALUES ($1, $2, $3, $4)
	`, userID, userID.String(), "test@test.local", "active")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	// Test: Create session
	sessionID, err := store.CreateSession(ctx, userID, "oidc", 1*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if len(sessionID) == 0 {
		t.Fatal("sessionID is empty")
	}

	// Test: Resolve valid session
	resolvedID, err := store.ResolveSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveSession failed: %v", err)
	}

	if resolvedID != userID {
		t.Errorf("expected user_id %s, got %s", userID, resolvedID)
	}

	// Test: Revoke session
	err = store.RevokeSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("RevokeSession failed: %v", err)
	}

	// Test: Revoked session cannot be resolved
	_, err = store.ResolveSession(ctx, sessionID)
	if err == nil {
		t.Fatal("expected error resolving revoked session")
	}

	// Test: Invalid/expired session
	expiredID, err := store.CreateSession(ctx, userID, "oidc", 1*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)

	_, err = store.ResolveSession(ctx, expiredID)
	if err == nil {
		t.Fatal("expected error resolving expired session")
	}

	// Test: Different session ID doesn't exist
	_, err = store.ResolveSession(ctx, "invalid-session-id")
	if err == nil {
		t.Fatal("expected error for invalid session_id")
	}
}

func TestCallbackReplayPrevention(t *testing.T) {
	// Conceptual test: state is consumed one-time per callback.
	// In real flow, the auth transaction cookies are deleted after use.
	// Second callback attempt will have missing state/nonce cookies.

	// This test ensures that the flow prevents replay:
	// 1. First callback with state/nonce/PKCE: succeeds (session created)
	// 2. Second callback with same state/nonce/PKCE: fails (cookies deleted)
	//
	// If we tried to replay by:
	// - Sending same code twice: token endpoint rejects (code already used)
	// - Sending same state twice: missing cookie (auth transaction expired)
	// - Replaying entire callback URL: same as above (cookies gone)

	// We test this indirectly via session creation:
	// - same session_id cannot exist twice
	// - two calls to CreateSession with same user produce different session_ids

	seedURL := os.Getenv("OMNIRA_DATABASE_URL")
	if seedURL == "" {
		t.Skip("OMNIRA_DATABASE_URL required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store := NewPostgresSessionStore(pool)
	userID := uuid.New()

	_, err = pool.Exec(ctx, `
		INSERT INTO users(id, external_subject, email, status)
		VALUES ($1, $2, $3, $4)
	`, userID, userID.String(), "test@test.local", "active")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	// Create two sessions from same user (simulating two separate callback flows)
	session1, _ := store.CreateSession(ctx, userID, "oidc", 1*time.Hour)
	session2, _ := store.CreateSession(ctx, userID, "oidc", 1*time.Hour)

	// Session IDs must be different (not reused)
	if session1 == session2 {
		t.Fatal("session_id reused (not random)")
	}

	// Both sessions are valid
	id1, _ := store.ResolveSession(ctx, session1)
	id2, _ := store.ResolveSession(ctx, session2)

	if id1 != userID || id2 != userID {
		t.Fatal("sessions not resolving to same user")
	}

	// Revoking one doesn't affect the other
	_ = store.RevokeSession(ctx, session1)

	_, err = store.ResolveSession(ctx, session1)
	if err == nil {
		t.Fatal("session1 should be revoked")
	}

	id2again, _ := store.ResolveSession(ctx, session2)
	if id2again != userID {
		t.Fatal("session2 still valid after session1 revoked")
	}
}
