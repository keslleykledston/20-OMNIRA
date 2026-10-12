package adapters

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// MT-AT-025: cache keys are scoped by tenant. The same person id and the same session id live in two companies at once (a
// Hub agent has one agent profile id per company in real data, but nothing in the store may depend on that): one company's
// heartbeat, expiry and snapshot must never touch the other's.
func TestValkeyStore_SameAgentAndSessionInTwoTenants_NeverCollide(t *testing.T) {
	store, cli := testStore(t)
	ctx := context.Background()
	tenantA, tenantB, tenantC := uuid.New(), uuid.New(), uuid.New()
	agent, session := uuid.New(), uuid.New().String()

	if online, err := store.Touch(ctx, tenantA, agent, session, 10*time.Millisecond); err != nil || !online {
		t.Fatalf("A touch: online=%v err=%v", online, err)
	}
	// B's first heartbeat must be its OWN online transition: if the key were global, A's session would make this "already online"
	if online, err := store.Touch(ctx, tenantB, agent, session, time.Minute); err != nil || !online {
		t.Fatalf("B touch must be its own first online transition: online=%v err=%v", online, err)
	}

	for _, tc := range []struct {
		name   string
		tenant uuid.UUID
		want   bool
	}{{"A", tenantA, true}, {"B", tenantB, true}, {"C (never touched)", tenantC, false}} {
		snap, err := store.Snapshot(ctx, tc.tenant)
		if err != nil {
			t.Fatalf("snapshot %s: %v", tc.name, err)
		}
		if got := containsUUID(snap, agent); got != tc.want {
			t.Errorf("snapshot of %s lists the agent = %v, want %v", tc.name, got, tc.want)
		}
		m, err := store.OnlineMembers(ctx, tc.tenant, []uuid.UUID{agent})
		if err != nil {
			t.Fatalf("online members %s: %v", tc.name, err)
		}
		if m[agent] != tc.want {
			t.Errorf("OnlineMembers of %s = %v, want %v", tc.name, m[agent], tc.want)
		}
	}

	time.Sleep(30 * time.Millisecond)
	offline, _, err := store.ExpireBatch(ctx, time.Now(), 100)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	var sawA, sawB bool
	for _, ref := range offline {
		if ref.AgentProfileID != agent {
			continue
		}
		sawA = sawA || ref.TenantID == tenantA
		sawB = sawB || ref.TenantID == tenantB
	}
	if !sawA || sawB {
		t.Fatalf("only company A's session expired: offline for A=%v, for B=%v (B must stay online)", sawA, sawB)
	}
	if snap, _ := store.Snapshot(ctx, tenantB); !containsUUID(snap, agent) {
		t.Fatal("B lost its online agent when A's session expired")
	}
	if snap, _ := store.Snapshot(ctx, tenantA); containsUUID(snap, agent) {
		t.Fatal("A still lists the agent after its session expired")
	}

	// every key this store wrote carries a tenant id, except the single global expiry index, whose members start with the tenant id
	iter := cli.Scan(ctx, 0, "presence:*", 1000).Iterator()
	for iter.Next(ctx) {
		k := iter.Val()
		if k == keyExpiryZSet {
			members, _ := cli.ZRange(ctx, k, 0, -1).Result()
			for _, mem := range members {
				if !strings.HasPrefix(mem, tenantA.String()+"|") && !strings.HasPrefix(mem, tenantB.String()+"|") {
					t.Errorf("expiry member without a tenant prefix: %q", mem)
				}
			}
			continue
		}
		if !strings.Contains(k, tenantA.String()) && !strings.Contains(k, tenantB.String()) {
			t.Errorf("presence key without a tenant id: %q", k)
		}
	}
}
