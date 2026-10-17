package adapters_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ADR-0043 S3 on the realtime streams: a person the sector scope restricts is told only about conversations they may see (instance stream), and a conversation stream ends
// when its conversation leaves their reach. An unknown answer fails closed.

type sectorAuth struct {
	*fakeAuth
	restricted atomic.Bool
	failing    atomic.Bool
}

func (s *sectorAuth) SectorRestricted(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	if s.failing.Load() {
		return false, context.DeadlineExceeded
	}
	return s.restricted.Load(), nil
}

var _ inboxadapters.SectorScopedStreamAuthorizer = (*sectorAuth)(nil)

func sectorFixture(t *testing.T, restricted bool) (*sseFixture, *sectorAuth) {
	t.Helper()
	var sa *sectorAuth
	f := newSSEAuthWith(t, inboxadapters.RealtimeOptions{Recheck: 100 * time.Millisecond, Keepalive: 80 * time.Millisecond, VisibilityTTL: time.Millisecond}, func(base *fakeAuth) inboxadapters.StreamAuthorizer {
		sa = &sectorAuth{fakeAuth: base}
		sa.restricted.Store(restricted)
		return sa
	})
	return f, sa
}

func TestSSEInstanceStreamTellsARestrictedPersonOnlyAboutTheirConversations(t *testing.T) {
	f, sa := sectorFixture(t, true)
	hidden := uuid.New()
	sa.hidden[hidden] = true
	_, r, _ := f.open(t, "/t/"+f.tenant.String()+"/events")
	time.Sleep(150 * time.Millisecond)
	publish(t, f.nc, f.tenant, hidden, "message_received") // outside their queues: must not arrive
	publish(t, f.nc, f.tenant, f.conv, "message_received") // inside: arrives
	data, err := nextData(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, hidden.String()) || !strings.Contains(data, f.conv.String()) {
		t.Fatalf("a restricted person was told about a conversation outside their reach: %s", data)
	}
}

func TestSSEInstanceStreamIsNotFilteredWhenThePersonIsNotRestricted(t *testing.T) {
	f, sa := sectorFixture(t, false)
	other := uuid.New()
	sa.hidden[other] = true // would be hidden if the filter ran
	_, r, _ := f.open(t, "/t/"+f.tenant.String()+"/events")
	time.Sleep(150 * time.Millisecond)
	publish(t, f.nc, f.tenant, other, "message_received")
	data, err := nextData(r)
	if err != nil || !strings.Contains(data, other.String()) {
		t.Fatalf("an unrestricted person lost an event: %q %v", data, err)
	}
}

func TestSSEInstanceStreamFailsClosedWhenTheScopeCannotBeRead(t *testing.T) {
	f, sa := sectorFixture(t, false)
	sa.failing.Store(true)
	hidden := uuid.New()
	sa.hidden[hidden] = true
	_, r, _ := f.open(t, "/t/"+f.tenant.String()+"/events")
	time.Sleep(150 * time.Millisecond)
	publish(t, f.nc, f.tenant, hidden, "message_received")
	publish(t, f.nc, f.tenant, f.conv, "message_received")
	// closing the stream (the client reconnects and refetches) is as safe as filtering; telling the hidden event is not
	if data, err := nextData(r); err == nil && strings.Contains(data, hidden.String()) {
		t.Fatalf("with the scope unreadable the stream told a hidden event: %q", data)
	}
}

func TestSSEConversationStreamEndsWhenTheConversationLeavesTheirReach(t *testing.T) {
	f, sa := sectorFixture(t, true)
	_, r, _ := f.open(t, "/t/"+f.tenant.String()+"/c/"+f.conv.String()+"/events")
	time.Sleep(150 * time.Millisecond)
	publish(t, f.nc, f.tenant, f.conv, "message_received")
	if data, err := nextData(r); err != nil || !strings.Contains(data, f.conv.String()) {
		t.Fatalf("event while in reach: %q %v", data, err)
	}
	sa.hidden[f.conv] = true // sent to another queue
	done := make(chan struct{})
	go func() {
		for {
			if _, err := r.ReadString('\n'); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the conversation stream stayed open after the conversation left the person's reach")
	}
}

var _ = tenancydomain.AccessSourceDirect
