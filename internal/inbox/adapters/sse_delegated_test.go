package adapters_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// A Hub agent's stream must end within the recheck even when the lookups it makes per event stall (Codex review of phase 05): the watchdog runs apart from the
// event loop and every lookup is bounded.
type stallingAuth struct {
	fakeAuth
	hub       uuid.UUID
	revoked   atomic.Bool
	lookups   atomic.Int32
	unrestric bool
}

func (s *stallingAuth) AuthorizeServing(_ context.Context, userID, tenantID, hubID uuid.UUID, _ []string) (*tenancydomain.TenantContext, error) {
	if s.revoked.Load() || hubID != s.hub {
		return nil, tenancyadapters.ErrServeDenied
	}
	return tenancydomain.NewHubServeTenantContext(tenantID, userID, hubID, uuid.New(), uuid.New(), []string{"conversation.read"}, "")
}

func (s *stallingAuth) ConversationVisibleServing(ctx context.Context, _, _, _, _ uuid.UUID) (bool, error) {
	s.lookups.Add(1)
	<-ctx.Done() // a stalled database
	return false, errors.New("stalled")
}

func (s *stallingAuth) ScopeUnrestricted(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	return s.unrestric, nil
}

func TestDelegatedStreamEndsWithinTheRecheckEvenWhenTheLookupsStall(t *testing.T) {
	nc := natsConn(t)
	tenant, user, hub, conv := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	auth := &stallingAuth{hub: hub}
	auth.allowed = map[uuid.UUID]bool{tenant: true}
	h := inboxadapters.NewRealtimeHandler(nc, auth, inboxadapters.RealtimeOptions{
		DelegatedRecheck: 100 * time.Millisecond, DatabaseTimeout: 400 * time.Millisecond, Keepalive: time.Hour, WriteTimeout: time.Second,
	})
	who := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: user, Subject: "u"})))
		})
	}
	mux := http.NewServeMux()
	mux.Handle("GET /t/{tenant_id}/events", tenancyadapters.Delegable("conversation.read", who(inboxadapters.StreamMiddleware(auth)(http.HandlerFunc(h.StreamInboxEvents)))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/t/"+tenant.String()+"/events", nil)
	req.Header.Set(tenancydomain.ActingAsHeader, "hub:"+hub.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	time.Sleep(100 * time.Millisecond)
	publish(t, nc, tenant, conv, "message_received") // the per-event lookup now stalls (restricted contract)
	time.Sleep(50 * time.Millisecond)
	if auth.lookups.Load() == 0 {
		t.Fatal("the event did not reach the lookup")
	}
	auth.revoked.Store(true)
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := res.Body.Read(buf); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream stayed open after the access was withdrawn while a lookup was stalled")
	}
}
