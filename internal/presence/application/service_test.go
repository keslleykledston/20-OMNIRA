package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	presencedomain "github.com/omnira/omnira/internal/presence/domain"
	"github.com/omnira/omnira/internal/presence/ports"
)

type fakeStore struct {
	touchOnline     bool
	touchErr        error
	touchCalls      int
	expireOffline   []ports.AgentRef
	expireProcessed int
	expireErr       error
	snapshot        []uuid.UUID
	snapshotErr     error
}

func (f *fakeStore) Touch(ctx context.Context, tenantID, agentProfileID uuid.UUID, sessionID string, ttl time.Duration) (bool, error) {
	f.touchCalls++
	return f.touchOnline, f.touchErr
}

func (f *fakeStore) ExpireBatch(ctx context.Context, now time.Time, limit int) ([]ports.AgentRef, int, error) {
	return f.expireOffline, f.expireProcessed, f.expireErr
}

func (f *fakeStore) Snapshot(ctx context.Context, tenantID uuid.UUID) ([]uuid.UUID, error) {
	return f.snapshot, f.snapshotErr
}

var _ ports.Store = (*fakeStore)(nil)

type fakePublisher struct {
	calls  int
	status presencedomain.Status
	err    error
}

func (f *fakePublisher) PublishTransition(ctx context.Context, tenantID, agentProfileID uuid.UUID, status presencedomain.Status) error {
	f.calls++
	f.status = status
	return f.err
}

var _ ports.TransitionPublisher = (*fakePublisher)(nil)

type fakeLastSeen struct {
	marks int
}

func (f *fakeLastSeen) MarkSeen(tenantID, agentProfileID uuid.UUID, at time.Time) { f.marks++ }
func (f *fakeLastSeen) FlushNow(ctx context.Context) error                        { return nil }

var _ ports.LastSeenWriter = (*fakeLastSeen)(nil)

func TestHeartbeat_FirstSession_PublishesOnline(t *testing.T) {
	store := &fakeStore{touchOnline: true}
	pub := &fakePublisher{}
	ls := &fakeLastSeen{}
	svc := NewService(store, pub, ls)

	err := svc.Heartbeat(context.Background(), uuid.New(), uuid.New(), uuid.New().String())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pub.calls != 1 {
		t.Fatalf("expected exactly one transition publish, got %d", pub.calls)
	}
	if pub.status != presencedomain.StatusOnline {
		t.Fatalf("expected online transition, got %s", pub.status)
	}
	if ls.marks != 1 {
		t.Fatalf("expected last_seen to be marked once, got %d", ls.marks)
	}
}

func TestHeartbeat_Refresh_NoDuplicateTransition(t *testing.T) {
	store := &fakeStore{touchOnline: false} // store reports no transition: already online
	pub := &fakePublisher{}
	ls := &fakeLastSeen{}
	svc := NewService(store, pub, ls)

	if err := svc.Heartbeat(context.Background(), uuid.New(), uuid.New(), uuid.New().String()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pub.calls != 0 {
		t.Fatalf("refresh heartbeat must not publish a transition, got %d calls", pub.calls)
	}
	if ls.marks != 1 {
		t.Fatalf("last_seen should still be marked on a refresh, got %d", ls.marks)
	}
}

func TestHeartbeat_RejectsInvalidInput(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakePublisher{}, &fakeLastSeen{})
	cases := []struct {
		name           string
		tenantID       uuid.UUID
		agentProfileID uuid.UUID
		sessionID      string
	}{
		{"nil tenant", uuid.Nil, uuid.New(), uuid.New().String()},
		{"nil agent profile", uuid.New(), uuid.Nil, uuid.New().String()},
		{"empty session", uuid.New(), uuid.New(), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := svc.Heartbeat(context.Background(), c.tenantID, c.agentProfileID, c.sessionID); !errors.Is(err, ErrInvalidHeartbeat) {
				t.Fatalf("expected ErrInvalidHeartbeat, got %v", err)
			}
		})
	}
}

func TestHeartbeat_StoreFailure_NeverPublishesOnline(t *testing.T) {
	store := &fakeStore{touchErr: errors.New("valkey unavailable")}
	pub := &fakePublisher{}
	svc := NewService(store, pub, &fakeLastSeen{})

	err := svc.Heartbeat(context.Background(), uuid.New(), uuid.New(), uuid.New().String())
	if err == nil {
		t.Fatal("expected error when store is unavailable")
	}
	if pub.calls != 0 {
		t.Fatalf("must not publish a transition when the store call failed, got %d", pub.calls)
	}
}

func TestSnapshot_RequiresTenant(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakePublisher{}, &fakeLastSeen{})
	if _, err := svc.Snapshot(context.Background(), uuid.Nil); !errors.Is(err, ErrInvalidTenant) {
		t.Fatalf("expected ErrInvalidTenant, got %v", err)
	}
}

func TestSnapshot_ReturnsStoreResult(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	store := &fakeStore{snapshot: ids}
	svc := NewService(store, &fakePublisher{}, &fakeLastSeen{})
	got, err := svc.Snapshot(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 agents online, got %d", len(got))
	}
}
