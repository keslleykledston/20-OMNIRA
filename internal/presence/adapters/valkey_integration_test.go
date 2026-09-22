package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// testStore connects to a real Valkey/Redis instance. Skipped when
// OMNIRA_VALKEY_TEST_URL is unset, same convention as
// internal/worker/publisher/publisher_integration_test.go and
// internal/worker/routing/consumer_test.go for their real-dependency tests.
func testStore(t *testing.T) (*Store, *redis.Client) {
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
	// Isolate each test run: flush only the keys this suite may have left
	// behind, never the whole database (shared instance in CI/dev).
	t.Cleanup(func() {
		iter := cli.Scan(context.Background(), 0, "presence:*", 1000).Iterator()
		for iter.Next(context.Background()) {
			cli.Del(context.Background(), iter.Val())
		}
	})
	return NewStore(cli), cli
}

func TestValkeyStore_FirstHeartbeat_TransitionsOnline(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	tenant, agent := uuid.New(), uuid.New()

	online, err := store.Touch(ctx, tenant, agent, uuid.New().String(), time.Minute)
	if err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	if !online {
		t.Fatal("first heartbeat for an agent must report becameOnline=true")
	}
}

func TestValkeyStore_SecondHeartbeatSameSession_NoDuplicateTransition(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	tenant, agent := uuid.New(), uuid.New()
	session := uuid.New().String()

	if _, err := store.Touch(ctx, tenant, agent, session, time.Minute); err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	online, err := store.Touch(ctx, tenant, agent, session, time.Minute)
	if err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	if online {
		t.Fatal("refreshing the same live session must not report a new transition")
	}
}

func TestValkeyStore_SecondTabWhileOnline_NoDuplicateTransition(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	tenant, agent := uuid.New(), uuid.New()

	if _, err := store.Touch(ctx, tenant, agent, uuid.New().String(), time.Minute); err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	online, err := store.Touch(ctx, tenant, agent, uuid.New().String(), time.Minute) // new tab, same agent
	if err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	if online {
		t.Fatal("a second tab while the agent is already online must not report a new transition")
	}
}

func TestValkeyStore_OneTabExpires_OtherKeepsAgentOnline(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	tenant, agent := uuid.New(), uuid.New()
	sessionA, sessionB := uuid.New().String(), uuid.New().String()

	if _, err := store.Touch(ctx, tenant, agent, sessionA, 10*time.Millisecond); err != nil {
		t.Fatalf("touch A failed: %v", err)
	}
	if _, err := store.Touch(ctx, tenant, agent, sessionB, time.Minute); err != nil {
		t.Fatalf("touch B failed: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	offline, _, err := store.ExpireBatch(ctx, time.Now(), 100)
	if err != nil {
		t.Fatalf("expire batch failed: %v", err)
	}
	if len(offline) != 0 {
		t.Fatalf("expected no offline transition while session B is still alive, got %v", offline)
	}
	online, err := store.Snapshot(ctx, tenant)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if !containsUUID(online, agent) {
		t.Fatal("agent must still be reported online after only one of two sessions expired")
	}
}

func TestValkeyStore_LastTabExpires_TransitionsOffline(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	tenant, agent := uuid.New(), uuid.New()

	if _, err := store.Touch(ctx, tenant, agent, uuid.New().String(), 10*time.Millisecond); err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	offline, processed, err := store.ExpireBatch(ctx, time.Now(), 100)
	if err != nil {
		t.Fatalf("expire batch failed: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected exactly one expired entry processed, got %d", processed)
	}
	if len(offline) != 1 || offline[0].AgentProfileID != agent {
		t.Fatalf("expected agent %s to transition offline, got %v", agent, offline)
	}
	online, err := store.Snapshot(ctx, tenant)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if containsUUID(online, agent) {
		t.Fatal("agent must no longer be online after its last session expired")
	}
}

func TestValkeyStore_ExpireBatch_IdempotentUnderConcurrentReapers(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	tenant, agent := uuid.New(), uuid.New()

	if _, err := store.Touch(ctx, tenant, agent, uuid.New().String(), 10*time.Millisecond); err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	// Simulate two reaper instances racing over the same expiry window: the
	// second call must see nothing left to retire and must not report a
	// second offline transition for the same agent (ADR-0010 §7 idempotency).
	now := time.Now()
	offline1, _, err := store.ExpireBatch(ctx, now, 100)
	if err != nil {
		t.Fatalf("first expire batch failed: %v", err)
	}
	offline2, processed2, err := store.ExpireBatch(ctx, now, 100)
	if err != nil {
		t.Fatalf("second expire batch failed: %v", err)
	}
	if len(offline1) != 1 {
		t.Fatalf("expected the first reaper to report the offline transition, got %v", offline1)
	}
	if len(offline2) != 0 || processed2 != 0 {
		t.Fatalf("expected the second reaper to find nothing left to retire, got offline=%v processed=%d", offline2, processed2)
	}
}

func containsUUID(list []uuid.UUID, target uuid.UUID) bool {
	for _, id := range list {
		if id == target {
			return true
		}
	}
	return false
}
