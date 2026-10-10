package adapters_test

// ADR-0040 phase 05, REALTIME: a Hub agent attending an instance receives the live events of its inbox (the SSE streams) through the same routes a member
// uses. On a real PostgreSQL (application role) and a real NATS, with the real stream handler and middleware. What must hold:
//   - admission is the delegated door, in a SHORT transaction (none is held while streaming): no context, no key, no grant, another instance, the switch off,
//     a malformed header: all refused; a member without the header streams as before;
//   - the instance stream tells the agent only about conversations they may see (a contract limited to some queues must not leak the others' existence),
//     the conversation stream refuses a conversation outside the scope;
//   - it ends within the recheck interval after the grant is revoked, the key is lost, or the conversation leaves the contract's queue scope.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/omnira/omnira/internal/hub/provisioning"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

// streamAuth is the production authorizer (httpserver.sessionStreamAuthorizer) rebuilt over the same building blocks.
type streamAuth struct {
	w     *world
	authz *tenancyapplication.AuthorizationService
}

func (a streamAuth) Authorize(ctx context.Context, userID, tenantID uuid.UUID) (*tenancydomain.TenantContext, error) {
	var tc *tenancydomain.TenantContext
	err := platformdb.WithTenantSession(ctx, a.w.app, userID, false, func(c context.Context) error {
		var e error
		tc, e = a.authz.AuthorizeAccessToTenant(c, tenantID, userID)
		return e
	})
	return tc, err
}

func (a streamAuth) ConversationVisible(ctx context.Context, userID, tenantID, conversationID uuid.UUID) (bool, error) {
	var visible bool
	err := platformdb.WithTenantSession(ctx, a.w.app, userID, false, func(c context.Context) error {
		return platformdb.QuerierFromContext(c, a.w.app).QueryRow(c, `SELECT EXISTS(SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2)`, tenantID, conversationID).Scan(&visible)
	})
	return visible, err
}

func (a streamAuth) AuthorizeServing(ctx context.Context, userID, tenantID, hubID uuid.UUID, keys []string) (*tenancydomain.TenantContext, error) {
	return tenancyadapters.AuthorizeServing(ctx, a.w.app, userID, tenantID, hubID, keys)
}

func (a streamAuth) ScopeUnrestricted(ctx context.Context, userID, tenantID, hubID uuid.UUID) (bool, error) {
	return tenancyadapters.ServingScopeUnrestricted(ctx, a.w.app, userID, tenantID, hubID)
}

func (a streamAuth) ConversationVisibleServing(ctx context.Context, userID, tenantID, hubID, conversationID uuid.UUID) (bool, error) {
	return tenancyadapters.ServingConversationVisible(ctx, a.w.app, userID, tenantID, hubID, conversationID)
}

type rtAPI struct {
	w   *world
	srv *httptest.Server
	nc  *nats.Conn
}

func (w *world) realtimeAPI(t *testing.T) *rtAPI {
	t.Helper()
	cfg := testhelpers.RequireIntegrationNATS(t)
	nc, err := nats.Connect(cfg.URL)
	w.must(err)
	t.Cleanup(nc.Close)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	auth := streamAuth{w: w, authz: tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))}
	h := inboxadapters.NewRealtimeHandler(nc, auth, inboxadapters.RealtimeOptions{
		Recheck: time.Hour, DelegatedRecheck: 200 * time.Millisecond, VisibilityTTL: 50 * time.Millisecond, Keepalive: 100 * time.Millisecond,
	})
	who := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
				r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u, Subject: "u"}))
			}
			next.ServeHTTP(rw, r)
		})
	}
	mw := inboxadapters.StreamMiddleware(auth)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/events", tenancyadapters.Delegable("conversation.read", who(mw(http.HandlerFunc(h.StreamInboxEvents)))))
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/events", tenancyadapters.Delegable("conversation.read", who(mw(http.HandlerFunc(h.StreamConversationEvents)))))
	// a stream route that was NOT declared delegable
	mux.Handle("GET /api/v1/tenants/{tenant_id}/other/events", who(mw(http.HandlerFunc(h.StreamInboxEvents))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &rtAPI{w: w, srv: srv, nc: nc}
}

type rtStream struct {
	status int
	lines  chan string
	closed chan struct{}
}

func (a *rtAPI) open(t *testing.T, user uuid.UUID, path, acting string) *rtStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.srv.URL+path, nil)
	req.Header.Set("X-Test-User", user.String())
	if acting != "" {
		req.Header.Set(tenancydomain.ActingAsHeader, acting)
	}
	res, err := http.DefaultClient.Do(req)
	a.w.must(err)
	t.Cleanup(func() { res.Body.Close() })
	s := &rtStream{status: res.StatusCode, lines: make(chan string, 64), closed: make(chan struct{})}
	go func() {
		defer close(s.closed)
		r := bufio.NewReader(res.Body)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				s.lines <- strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
	}()
	if res.StatusCode == 200 {
		time.Sleep(150 * time.Millisecond) // the subscription is made before the first byte, but give the broker a moment
	}
	return s
}

// next returns the next event or "" after the wait.
func (s *rtStream) next(wait time.Duration) string {
	select {
	case l := <-s.lines:
		return l
	case <-time.After(wait):
		return ""
	}
}

func (s *rtStream) endsWithin(d time.Duration) bool {
	select {
	case <-s.closed:
		return true
	case <-time.After(d):
		return false
	}
}

func (a *rtAPI) publish(tenant, conv uuid.UUID, typ string) {
	body, _ := json.Marshal(map[string]any{"type": typ, "id": conv.String(), "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "data": map[string]any{"message_id": "m1"}})
	a.w.must(a.nc.Publish("inbox.events."+tenant.String()+"."+conv.String(), body))
	_ = a.nc.Flush()
}

func TestDelegatedRealtimeStreams(t *testing.T) {
	w := newWorld(t)
	api := w.realtimeAPI(t)
	w.ceiling("A", provisioning.ServingPresets["atendimento"]...)
	agent := w.hubAgent("agent")
	g := w.grantReply(agent, "A")
	w.grantKeys(g, provisioning.ServingPresets["atendimento"]...)
	hub := "hub:" + w.hub.String()
	inbox := "/api/v1/tenants/" + w.tenant["A"].String() + "/inbox/events"
	convEvents := func(tenantKey, convKey string) string {
		return "/api/v1/tenants/" + w.tenant[tenantKey].String() + "/inbox/conversations/" + w.conv[convKey].String() + "/events"
	}
	narrow := func(queues ...uuid.UUID) { w.queueScope("A", queues...) }
	open := func() {
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
	}

	t.Run("the instance stream delivers the events of the instance to a Hub agent, and only of that instance", func(t *testing.T) {
		open()
		s := api.open(t, agent, inbox, hub)
		if s.status != 200 {
			t.Fatalf("status %d", s.status)
		}
		api.publish(w.tenant["B"], w.conv["B"], "message_received") // another instance
		api.publish(w.tenant["A"], w.conv["A"], "message_received")
		api.publish(w.tenant["A"], w.conv["A2"], "message_received")
		got := []string{s.next(3 * time.Second), s.next(3 * time.Second)}
		if !strings.Contains(got[0], w.conv["A"].String()) || !strings.Contains(got[1], w.conv["A2"].String()) {
			t.Errorf("events = %v, want the two conversations of A in order and nothing of B", got)
		}
		if extra := s.next(300 * time.Millisecond); extra != "" {
			t.Errorf("an extra event arrived: %s", extra)
		}
	})

	t.Run("a contract limited to some queues: the agent is never told about the conversations of the others", func(t *testing.T) {
		narrow(w.queue1["A"])
		t.Cleanup(open)
		s := api.open(t, agent, inbox, hub)
		if s.status != 200 {
			t.Fatalf("status %d", s.status)
		}
		api.publish(w.tenant["A"], w.conv["A2"], "message_received") // queue 2: outside
		api.publish(w.tenant["A"], w.conv["A0"], "message_received") // no queue: outside
		api.publish(w.tenant["A"], w.conv["A"], "message_status")    // queue 1: the only one
		got := s.next(3 * time.Second)
		if !strings.Contains(got, w.conv["A"].String()) {
			t.Errorf("first event = %q, want the one of the allowed queue", got)
		}
		if extra := s.next(300 * time.Millisecond); extra != "" {
			t.Errorf("an event of a conversation outside the scope arrived: %s", extra)
		}
	})

	t.Run("the instance stream follows a scope that narrows while it is open (the visibility answer is reused only briefly)", func(t *testing.T) {
		open()
		t.Cleanup(open)
		s := api.open(t, agent, inbox, hub)
		if s.status != 200 {
			t.Fatalf("status %d", s.status)
		}
		api.publish(w.tenant["A"], w.conv["A2"], "message_received")
		if got := s.next(3 * time.Second); !strings.Contains(got, w.conv["A2"].String()) {
			t.Fatalf("before narrowing: %q", got)
		}
		narrow(w.queue1["A"])
		time.Sleep(600 * time.Millisecond) // beyond the visibility TTL and the watchdog interval (the contract was open: no per-event filter until the recheck sees the new scope)
		api.publish(w.tenant["A"], w.conv["A2"], "message_received")
		if extra := s.next(500 * time.Millisecond); extra != "" {
			t.Errorf("an event of a conversation that left the scope still arrived: %s", extra)
		}
	})

	t.Run("with a limited contract the visibility answer is reused only briefly: a conversation that leaves the scope stops being announced", func(t *testing.T) {
		narrow(w.queue1["A"], w.queue2["A"])
		t.Cleanup(open)
		s := api.open(t, agent, inbox, hub)
		if s.status != 200 {
			t.Fatalf("status %d", s.status)
		}
		api.publish(w.tenant["A"], w.conv["A2"], "message_received")
		if got := s.next(3 * time.Second); !strings.Contains(got, w.conv["A2"].String()) {
			t.Fatalf("inside the scope: %q", got)
		}
		narrow(w.queue1["A"])
		time.Sleep(120 * time.Millisecond) // beyond the 50 ms visibility TTL, before the next watchdog tick would matter
		api.publish(w.tenant["A"], w.conv["A2"], "message_received")
		if extra := s.next(500 * time.Millisecond); extra != "" {
			t.Errorf("a conversation that left the scope was still announced: %s", extra)
		}
	})

	t.Run("the conversation stream: inside the scope yes, outside or in another instance no", func(t *testing.T) {
		narrow(w.queue1["A"])
		t.Cleanup(open)
		if s := api.open(t, agent, convEvents("A", "A2"), hub); s.status != 404 {
			t.Errorf("a conversation outside the contract's queues: %d, want 404", s.status)
		}
		if s := api.open(t, agent, "/api/v1/tenants/"+w.tenant["A"].String()+"/inbox/conversations/"+w.conv["B"].String()+"/events", hub); s.status != 404 {
			t.Errorf("another instance's conversation under this instance's URL: %d, want 404", s.status)
		}
		s := api.open(t, agent, convEvents("A", "A"), hub)
		if s.status != 200 {
			t.Fatalf("a conversation inside the scope: %d", s.status)
		}
		api.publish(w.tenant["A"], w.conv["A"], "message_received")
		if got := s.next(3 * time.Second); !strings.Contains(got, w.conv["A"].String()) {
			t.Errorf("event = %q", got)
		}
	})

	t.Run("admission: nothing opens without the delegated door", func(t *testing.T) {
		open()
		stranger := w.hubAgent("stranger") // in the hub, no grant for A
		cases := []struct {
			name   string
			user   uuid.UUID
			path   string
			acting string
			want   int
		}{
			{"no acting header and not a member", agent, inbox, "", 404},
			{"malformed acting header", agent, inbox, "hub:xyz", 400},
			{"a hub the person has no relationship with", agent, inbox, "hub:" + uuid.NewString(), 404},
			{"a person with no grant in the instance", stranger, inbox, hub, 404},
			{"an instance with no grant", agent, "/api/v1/tenants/" + w.tenant["B"].String() + "/inbox/events", hub, 404},
			{"a stream route that is not declared delegable", agent, "/api/v1/tenants/" + w.tenant["A"].String() + "/other/events", hub, 404},
		}
		for _, c := range cases {
			if s := api.open(t, c.user, c.path, c.acting); s.status != c.want {
				t.Errorf("%s: %d, want %d", c.name, s.status, c.want)
			}
		}
		// a grant that does not carry conversation.read (reading the card only)
		w.grantKeys(g, "contact.read")
		if s := api.open(t, agent, inbox, hub); s.status != 404 {
			t.Errorf("without conversation.read: %d, want 404", s.status)
		}
		w.grantKeys(g, provisioning.ServingPresets["atendimento"]...)
		// the process switch
		tenancyadapters.EnableDelegatedServing(false)
		if s := api.open(t, agent, inbox, hub); s.status != 404 {
			t.Errorf("serving switched off: %d, want 404", s.status)
		}
		tenancyadapters.EnableDelegatedServing(true)
		if s := api.open(t, agent, inbox, hub); s.status != 200 {
			t.Errorf("switched back on: %d, want 200", s.status)
		}
	})

	t.Run("a member without the header streams exactly as before", func(t *testing.T) {
		member := w.user("member")
		w.directMember(member, "A")
		s := api.open(t, member, inbox, "")
		if s.status != 200 {
			t.Fatalf("member stream: %d", s.status)
		}
		api.publish(w.tenant["A"], w.conv["A2"], "message_received") // members are not scope-filtered
		if got := s.next(3 * time.Second); !strings.Contains(got, w.conv["A2"].String()) {
			t.Errorf("member event = %q", got)
		}
	})

	t.Run("it ends within the recheck after the grant is revoked", func(t *testing.T) {
		open()
		s := api.open(t, agent, inbox, hub)
		if s.status != 200 {
			t.Fatalf("status %d", s.status)
		}
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		t.Cleanup(func() { w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g) })
		if !s.endsWithin(3 * time.Second) {
			t.Error("the stream stayed open after the grant was revoked")
		}
	})

	t.Run("it ends within the recheck after the key is lost", func(t *testing.T) {
		open()
		s := api.open(t, agent, inbox, hub)
		if s.status != 200 {
			t.Fatalf("status %d", s.status)
		}
		w.grantKeys(g, "contact.read")
		t.Cleanup(func() { w.grantKeys(g, provisioning.ServingPresets["atendimento"]...) })
		if !s.endsWithin(3 * time.Second) {
			t.Error("the stream stayed open after conversation.read was withdrawn")
		}
	})

	t.Run("a conversation stream ends when the conversation leaves the contract's queue scope", func(t *testing.T) {
		open()
		s := api.open(t, agent, convEvents("A", "A"), hub)
		if s.status != 200 {
			t.Fatalf("status %d", s.status)
		}
		narrow(w.queue2["A"])
		t.Cleanup(open)
		if !s.endsWithin(3 * time.Second) {
			t.Error("the stream stayed open after the conversation left the scope")
		}
	})
}
