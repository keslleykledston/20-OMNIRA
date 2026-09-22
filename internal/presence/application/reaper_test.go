package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/presence/ports"
)

type reaperStubStore struct {
	fakeStore
	batches [][]ports.AgentRef
	call    int
}

func (s *reaperStubStore) ExpireBatch(ctx context.Context, now time.Time, limit int) ([]ports.AgentRef, int, error) {
	if s.expireErr != nil {
		return nil, 0, s.expireErr
	}
	if s.call >= len(s.batches) {
		return nil, 0, nil
	}
	batch := s.batches[s.call]
	s.call++
	processed := len(batch)
	if processed == limit {
		// simulate a full page so Tick() knows to ask again
	}
	return batch, processed, nil
}

func TestReaper_Tick_PublishesOfflineOnlyForExpiredAgents(t *testing.T) {
	ref := ports.AgentRef{TenantID: uuid.New(), AgentProfileID: uuid.New()}
	store := &reaperStubStore{batches: [][]ports.AgentRef{{ref}}}
	pub := &fakePublisher{}
	ls := &fakeLastSeen{}

	r := NewReaper(store, pub, ls)
	r.BatchSize = 10
	r.Tick(context.Background())

	if pub.calls != 1 {
		t.Fatalf("expected exactly one offline transition, got %d", pub.calls)
	}
	if ls.marks != 1 {
		t.Fatalf("expected last_seen marked on offline transition, got %d", ls.marks)
	}
}

func TestReaper_Tick_DrainsFullBatchesBeforeReturning(t *testing.T) {
	refA := ports.AgentRef{TenantID: uuid.New(), AgentProfileID: uuid.New()}
	refB := ports.AgentRef{TenantID: uuid.New(), AgentProfileID: uuid.New()}
	store := &reaperStubStore{batches: [][]ports.AgentRef{{refA}, {refB}, {}}}
	pub := &fakePublisher{}

	r := NewReaper(store, pub, &fakeLastSeen{})
	r.BatchSize = 1 // each returned batch equals the limit -> Tick must keep draining
	r.Tick(context.Background())

	if pub.calls != 2 {
		t.Fatalf("expected reaper to drain both pending batches in one tick, got %d publishes", pub.calls)
	}
}

func TestReaper_Tick_StopsOnStoreError(t *testing.T) {
	store := &reaperStubStore{fakeStore: fakeStore{expireErr: errors.New("valkey unavailable")}}
	pub := &fakePublisher{}
	r := NewReaper(store, pub, &fakeLastSeen{})
	r.Tick(context.Background())
	if pub.calls != 0 {
		t.Fatalf("must not publish anything when ExpireBatch fails, got %d", pub.calls)
	}
}

func TestReaper_Tick_NoExpiredSessions_NoOp(t *testing.T) {
	store := &reaperStubStore{batches: [][]ports.AgentRef{{}}}
	pub := &fakePublisher{}
	r := NewReaper(store, pub, &fakeLastSeen{})
	r.Tick(context.Background())
	if pub.calls != 0 {
		t.Fatalf("expected no transitions when nothing expired, got %d", pub.calls)
	}
}
