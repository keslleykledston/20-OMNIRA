package adapters_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type fakeAuth struct {
	allowed map[uuid.UUID]bool // tenant -> allowed
	calls   atomic.Int32
	revoke  atomic.Bool
}

func (f *fakeAuth) Authorize(_ context.Context, userID, tenantID uuid.UUID) (*tenancydomain.TenantContext, error) {
	f.calls.Add(1)
	if f.revoke.Load() || !f.allowed[tenantID] {
		return nil, errors.New("denied")
	}
	return tenancydomain.NewTenantContext(tenantID, userID, tenancydomain.AccessSourceDirect)
}

func natsConn(t *testing.T) *nats.Conn {
	url := os.Getenv("OMNIRA_NATS_URL")
	if url == "" {
		t.Skip("OMNIRA_NATS_URL required")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

type sseFixture struct {
	srv         *httptest.Server
	auth        *fakeAuth
	nc          *nats.Conn
	tenant      uuid.UUID
	user        uuid.UUID
	conv        uuid.UUID
	principalOK bool
}

func newSSE(t *testing.T, recheck time.Duration) *sseFixture {
	f := &sseFixture{nc: natsConn(t), tenant: uuid.New(), user: uuid.New(), conv: uuid.New(), principalOK: true}
	f.auth = &fakeAuth{allowed: map[uuid.UUID]bool{f.tenant: true}}
	h := inboxadapters.NewRealtimeHandler(f.nc, f.auth, inboxadapters.RealtimeOptions{Recheck: recheck, Keepalive: 80 * time.Millisecond})
	withPrincipal := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if f.principalOK {
				r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: f.user, Subject: "u"}))
			}
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	mw := inboxadapters.StreamMiddleware(f.auth)
	mux.Handle("GET /t/{tenant_id}/events", withPrincipal(mw(http.HandlerFunc(h.StreamInboxEvents))))
	mux.Handle("GET /t/{tenant_id}/c/{conversation_id}/events", withPrincipal(mw(http.HandlerFunc(h.StreamConversationEvents))))
	f.srv = httptest.NewUnstartedServer(mux)
	f.srv.Config.WriteTimeout = 300 * time.Millisecond // the production server has a 15s WriteTimeout
	f.srv.Start()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *sseFixture) open(t *testing.T, path string) (*http.Response, *bufio.Reader, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+path, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); res.Body.Close() })
	return res, bufio.NewReader(res.Body), cancel
}

// nextData returns the next "data:" payload, skipping comments; "" on EOF.
func nextData(r *bufio.Reader) (string, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: "), nil
		}
	}
}

func publish(t *testing.T, nc *nats.Conn, tenant, conv uuid.UUID, typ string) {
	body, _ := json.Marshal(map[string]any{"type": typ, "id": conv.String(), "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "data": map[string]any{"message_id": "m1"}})
	if err := nc.Publish("inbox.events."+tenant.String()+"."+conv.String(), body); err != nil {
		t.Fatal(err)
	}
	_ = nc.Flush()
}

func TestSSEDeliversTenantEventsAndSurvivesWriteTimeout(t *testing.T) {
	f := newSSE(t, time.Hour)
	res, r, _ := f.open(t, "/t/"+f.tenant.String()+"/events")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("status=%d ct=%q cors=%q", res.StatusCode, res.Header.Get("Content-Type"), res.Header.Get("Access-Control-Allow-Origin"))
	}
	time.Sleep(600 * time.Millisecond) // beyond the 300ms server WriteTimeout: stream must still be alive
	publish(t, f.nc, f.tenant, f.conv, "message_received")
	data, err := nextData(r)
	if err != nil {
		t.Fatalf("stream died (WriteTimeout not disabled?): %v", err)
	}
	var ev map[string]any
	if json.Unmarshal([]byte(data), &ev) != nil || ev["type"] != "message_received" || ev["id"] != f.conv.String() {
		t.Fatalf("event: %s", data)
	}
}

func TestSSEIsolatesTenantsAndConversations(t *testing.T) {
	f := newSSE(t, time.Hour)
	otherTenant, otherConv := uuid.New(), uuid.New()
	_, r, _ := f.open(t, "/t/"+f.tenant.String()+"/c/"+f.conv.String()+"/events")
	time.Sleep(100 * time.Millisecond)
	publish(t, f.nc, otherTenant, f.conv, "message_received") // other tenant, same conversation id
	publish(t, f.nc, f.tenant, otherConv, "message_received") // same tenant, other conversation
	publish(t, f.nc, f.tenant, f.conv, "message_status")      // the only one that must arrive
	data, err := nextData(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, `"message_status"`) {
		t.Fatalf("received a foreign event: %s", data)
	}
}

func TestSSEUnauthorizedAndUnknownTenant(t *testing.T) {
	f := newSSE(t, time.Hour)
	res, _, _ := f.open(t, "/t/"+uuid.NewString()+"/events")
	if res.StatusCode != 404 {
		t.Fatalf("unknown tenant: %d", res.StatusCode)
	}
	f.principalOK = false
	res, _, _ = f.open(t, "/t/"+f.tenant.String()+"/events")
	if res.StatusCode != 401 {
		t.Fatalf("no principal: %d", res.StatusCode)
	}
	f.principalOK = true
	res, _, _ = f.open(t, "/t/not-a-uuid/events")
	if res.StatusCode != 400 {
		t.Fatalf("bad tenant id: %d", res.StatusCode)
	}
}

func TestSSEClosesWhenAuthorizationIsRevoked(t *testing.T) {
	f := newSSE(t, 150*time.Millisecond)
	_, r, _ := f.open(t, "/t/"+f.tenant.String()+"/events")
	time.Sleep(200 * time.Millisecond)
	if f.auth.calls.Load() < 2 {
		t.Fatalf("expected periodic re-authorization, calls=%d", f.auth.calls.Load())
	}
	f.auth.revoke.Store(true)
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := r.ReadString('\n'); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case <-done: // stream closed by the server
	case <-time.After(3 * time.Second):
		t.Fatal("stream stayed open after the membership was revoked")
	}
}
