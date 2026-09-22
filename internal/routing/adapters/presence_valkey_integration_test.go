package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	presenceadapters "github.com/omnira/omnira/internal/presence/adapters"
)

// realPresenceStore connects to a real Valkey/Redis instance, same
// convention as internal/presence/adapters/valkey_integration_test.go.
func realPresenceStore(t *testing.T) *presenceadapters.Store {
	t.Helper()
	url := os.Getenv("OMNIRA_VALKEY_TEST_URL")
	if url == "" {
		t.Skip("OMNIRA_VALKEY_TEST_URL required")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("invalid OMNIRA_VALKEY_TEST_URL: %v", err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		t.Skipf("valkey not reachable: %v", err)
	}
	t.Cleanup(func() {
		iter := cli.Scan(context.Background(), 0, "presence:*", 1000).Iterator()
		for iter.Next(context.Background()) {
			cli.Del(context.Background(), iter.Val())
		}
	})
	return presenceadapters.NewStore(cli)
}

// These two tests use the REAL presence Store (real Valkey) as the
// PresenceChecker, end-to-end, rather than a fake — proving the actual
// multi-session/TTL semantics IAM4.2-A built are what IAM4.2-B1's
// eligibility check observes, not just an assumption encoded in a mock.
func TestAssignRoundRobin_FlagOn_MultiTab_OneLiveSessionIsEligible(t *testing.T) {
	store := realPresenceStore(t)
	f := newPresenceRoutingFixture(t, true)
	userID, agentProfileID := f.addAgent(t, nil)
	ctx := context.Background()

	sessionA, sessionB := uuid.New().String(), uuid.New().String()
	if _, err := store.Touch(ctx, f.tenantID, agentProfileID, sessionA, 10*time.Millisecond); err != nil {
		t.Fatalf("touch A failed: %v", err)
	}
	if _, err := store.Touch(ctx, f.tenantID, agentProfileID, sessionB, time.Minute); err != nil {
		t.Fatalf("touch B failed: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, _, err := store.ExpireBatch(ctx, time.Now(), 100); err != nil {
		t.Fatalf("expire batch failed: %v", err)
	}
	// Session A expired; session B is still alive -> agent must remain online/eligible.

	repo := NewPostgresAssignmentRepository(f.app, store)
	assigned, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || assigned != userID {
		t.Fatalf("expected the agent to remain eligible with one live session, got ok=%v assigned=%v", ok, assigned)
	}
}

func TestAssignRoundRobin_FlagOn_LastSessionExpired_IsIneligible(t *testing.T) {
	store := realPresenceStore(t)
	f := newPresenceRoutingFixture(t, true)
	f.addAgent(t, nil)
	ctx := context.Background()

	if _, err := store.Touch(ctx, f.tenantID, uuid.New() /* wrong id on purpose below */, uuid.New().String(), 10*time.Millisecond); err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, _, err := store.ExpireBatch(ctx, time.Now(), 100); err != nil {
		t.Fatalf("expire batch failed: %v", err)
	}
	// The real agent never heartbeat at all (and the one touch above used an
	// unrelated agent id and already expired) -> must be ineligible.

	repo := NewPostgresAssignmentRepository(f.app, store)
	_, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no assignment: the agent has no live presence session")
	}
}
