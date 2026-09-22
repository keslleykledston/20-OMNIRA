package routing

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeLivenessRepo struct {
	mu             sync.Mutex
	retriggerCalls []retriggerCall
	batches        []int // successive return values for Retrigger, in order
	queuesByAgent  map[uuid.UUID][]uuid.UUID
}

type retriggerCall struct {
	tenantID *uuid.UUID
	queueID  *uuid.UUID
	limit    int
}

func (f *fakeLivenessRepo) Retrigger(_ context.Context, tenantID *uuid.UUID, queueID *uuid.UUID, limit int, _ time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retriggerCalls = append(f.retriggerCalls, retriggerCall{tenantID: tenantID, queueID: queueID, limit: limit})
	if len(f.batches) == 0 {
		return 0, nil
	}
	n := f.batches[0]
	f.batches = f.batches[1:]
	return n, nil
}

func (f *fakeLivenessRepo) ActiveQueuesForAgent(_ context.Context, _ uuid.UUID, agentProfileID uuid.UUID) ([]uuid.UUID, error) {
	return f.queuesByAgent[agentProfileID], nil
}

func (f *fakeLivenessRepo) calls() []retriggerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]retriggerCall, len(f.retriggerCalls))
	copy(out, f.retriggerCalls)
	return out
}

func TestSweep_Tick_DrainsFullBatchesBeforeReturning(t *testing.T) {
	repo := &fakeLivenessRepo{batches: []int{2, 2, 1}} // first two calls return a "full" batch, third is partial -> stop
	s := NewSweep(repo)
	s.BatchSize = 2
	s.Tick(context.Background())
	if got := len(repo.calls()); got != 3 {
		t.Fatalf("expected the sweep to keep draining while batches are full, got %d calls", got)
	}
}

func TestSweep_Tick_StopsAfterOnePartialBatch(t *testing.T) {
	repo := &fakeLivenessRepo{batches: []int{0}}
	s := NewSweep(repo)
	s.BatchSize = 50
	s.Tick(context.Background())
	if got := len(repo.calls()); got != 1 {
		t.Fatalf("expected exactly one call when nothing is due, got %d", got)
	}
}

func TestSweep_Tick_ScopesToNoTenantOrQueue(t *testing.T) {
	repo := &fakeLivenessRepo{batches: []int{0}}
	s := NewSweep(repo)
	s.Tick(context.Background())
	calls := repo.calls()
	if len(calls) != 1 || calls[0].tenantID != nil || calls[0].queueID != nil {
		t.Fatalf("expected the safety sweep to be unscoped (nil tenant, nil queue), got %+v", calls)
	}
}

func TestPresenceWakeup_OnlineTransition_RetriggersEachResolvedQueue(t *testing.T) {
	tenantID, agentID, queueA, queueB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := &fakeLivenessRepo{
		queuesByAgent: map[uuid.UUID][]uuid.UUID{agentID: {queueA, queueB}},
	}
	w := NewPresenceWakeup(repo, nil)
	body, _ := json.Marshal(presenceTransitionEvent{TenantID: tenantID, AgentProfileID: agentID, Status: "online"})
	w.handle(context.Background(), body)

	calls := repo.calls()
	if len(calls) != 2 {
		t.Fatalf("expected one Retrigger call per resolved queue (2), got %d", len(calls))
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range calls {
		if c.tenantID == nil || *c.tenantID != tenantID {
			t.Fatalf("expected every call scoped to the event's tenant, got %+v", c)
		}
		if c.queueID == nil {
			t.Fatalf("expected every call scoped to one queue, got nil")
		}
		if c.limit != WakeupPerQueue {
			t.Fatalf("expected limit=%d (WakeupPerQueue), got %d", WakeupPerQueue, c.limit)
		}
		seen[*c.queueID] = true
	}
	if !seen[queueA] || !seen[queueB] {
		t.Fatalf("expected both resolved queues to be retriggered, got %+v", calls)
	}
}

func TestPresenceWakeup_OfflineTransition_IsIgnored(t *testing.T) {
	repo := &fakeLivenessRepo{}
	w := NewPresenceWakeup(repo, nil)
	body, _ := json.Marshal(presenceTransitionEvent{TenantID: uuid.New(), AgentProfileID: uuid.New(), Status: "offline"})
	w.handle(context.Background(), body)
	if got := len(repo.calls()); got != 0 {
		t.Fatalf("expected an offline transition to trigger nothing, got %d calls", got)
	}
}

func TestPresenceWakeup_MalformedEvent_IsIgnored(t *testing.T) {
	repo := &fakeLivenessRepo{}
	w := NewPresenceWakeup(repo, nil)
	w.handle(context.Background(), []byte("not json"))
	if got := len(repo.calls()); got != 0 {
		t.Fatalf("expected a malformed event to trigger nothing, got %d calls", got)
	}
}

func TestPresenceWakeup_NoEligibleQueues_CallsNothing(t *testing.T) {
	tenantID, agentID := uuid.New(), uuid.New()
	repo := &fakeLivenessRepo{queuesByAgent: map[uuid.UUID][]uuid.UUID{}}
	w := NewPresenceWakeup(repo, nil)
	body, _ := json.Marshal(presenceTransitionEvent{TenantID: tenantID, AgentProfileID: agentID, Status: "online"})
	w.handle(context.Background(), body)
	if got := len(repo.calls()); got != 0 {
		t.Fatalf("expected no Retrigger calls when the agent has no eligible queues, got %d", got)
	}
}

func TestPresenceWakeup_RepeatedOnlineEvent_IsSafeAndIdempotentByConstruction(t *testing.T) {
	// The wakeup itself has no dedup state: it relies entirely on
	// Retrigger's SKIP LOCKED + forward-bump for idempotency (proven against
	// real Postgres in internal/routing/adapters). Here we only prove the
	// wakeup handler itself is safe to call twice in a row (no panic, no
	// duplicate side effects beyond the calls it makes).
	tenantID, agentID, queueA := uuid.New(), uuid.New(), uuid.New()
	repo := &fakeLivenessRepo{queuesByAgent: map[uuid.UUID][]uuid.UUID{agentID: {queueA}}, batches: []int{1, 0}}
	w := NewPresenceWakeup(repo, nil)
	body, _ := json.Marshal(presenceTransitionEvent{TenantID: tenantID, AgentProfileID: agentID, Status: "online"})
	w.handle(context.Background(), body)
	w.handle(context.Background(), body)
	if got := len(repo.calls()); got != 2 {
		t.Fatalf("expected two calls (one per event), got %d — each is safe because Retrigger itself is idempotent", got)
	}
}
