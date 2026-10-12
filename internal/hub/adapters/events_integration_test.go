package adapters_test

// The Hub's change signal (events.go) on a real PostgreSQL (application role, RLS) and a real NATS: a person receives "something changed" for the companies they
// serve through the Hub, for those only, nothing about WHAT changed, and loses it when the relationship ends.

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
	hubadapters "github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/testhelpers"
)

type evAPI struct {
	w   *world
	srv *httptest.Server
	nc  *nats.Conn
}

func (w *world) eventsAPI(t *testing.T, opts hubadapters.EventsOptions) *evAPI {
	t.Helper()
	cfg := testhelpers.RequireIntegrationNATS(t)
	nc, err := nats.Connect(cfg.URL)
	w.must(err)
	t.Cleanup(nc.Close)
	h := hubadapters.NewHTTPHandler(w.app).WithEvents(nc, opts)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hubs/{hub_id}/events", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
			r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u, Subject: "u"}))
		}
		h.StreamEvents(rw, r)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &evAPI{w: w, srv: srv, nc: nc}
}

type evStream struct {
	status int
	frames chan string // the raw text of each frame that carries an event or data
	closed chan struct{}
}

func (a *evAPI) open(t *testing.T, user uuid.UUID, query string) *evStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.srv.URL+"/api/v1/hubs/"+a.w.hub.String()+"/events"+query, nil)
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	res, err := http.DefaultClient.Do(req)
	a.w.must(err)
	t.Cleanup(func() { res.Body.Close() })
	s := &evStream{status: res.StatusCode, frames: make(chan string, 64), closed: make(chan struct{})}
	go func() {
		defer close(s.closed)
		r := bufio.NewReader(res.Body)
		var frame []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if len(frame) > 0 {
					s.frames <- strings.Join(frame, "|")
				}
				frame = nil
				continue
			}
			if !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "retry:") {
				frame = append(frame, line)
			}
		}
	}()
	if res.StatusCode == 200 {
		time.Sleep(200 * time.Millisecond) // the subscriptions are made before the first byte; give the broker a moment
	}
	return s
}

func (s *evStream) next(wait time.Duration) string {
	select {
	case f := <-s.frames:
		return f
	case <-time.After(wait):
		return ""
	}
}

func (s *evStream) endsWithin(d time.Duration) bool {
	select {
	case <-s.closed:
		return true
	case <-time.After(d):
		return false
	}
}

func (a *evAPI) publish(tenant, conv uuid.UUID) {
	body, _ := json.Marshal(map[string]any{"type": "message_received", "id": conv.String(), "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "data": map[string]any{"message_id": "SECRET-BODY-REFERENCE"}})
	a.w.must(a.nc.Publish("inbox.events."+tenant.String()+"."+conv.String(), body))
	_ = a.nc.Flush()
}

var fastEvents = hubadapters.EventsOptions{Recheck: 250 * time.Millisecond, MinInterval: 150 * time.Millisecond, Keepalive: time.Hour, VisibilityTTL: 50 * time.Millisecond, MaxPerUser: 2}

func TestHubEvents_SignalsOnlyTheCompaniesThePersonServesAndNeverSaysWhat(t *testing.T) {
	w := newWorld(t)
	api := w.eventsAPI(t, fastEvents)
	agent := w.hubAgent("agent")
	w.grant(agent, "A")
	w.grant(agent, "B") // not C

	s := api.open(t, agent, "")
	if s.status != 200 {
		t.Fatalf("status %d", s.status)
	}
	api.publish(w.tenant["A"], w.conv["A"])
	if got := s.next(2 * time.Second); got != "event: changed|data: {}" {
		t.Fatalf("a company the person serves signalled %q, want exactly 'changed' with an empty object", got)
	}
	api.publish(w.tenant["B"], w.conv["B"])
	if got := s.next(2 * time.Second); got != "event: changed|data: {}" {
		t.Fatalf("the second served company signalled %q", got)
	}
	// a company the person does not serve: nothing, not even that something happened
	api.publish(w.tenant["C"], w.conv["C"])
	if got := s.next(600 * time.Millisecond); got != "" {
		t.Fatalf("a company the person does not serve signalled %q", got)
	}
	// nothing of the event travels: no id, no reference
	api.publish(w.tenant["A"], w.conv["A"])
	if got := s.next(2 * time.Second); strings.Contains(got, w.conv["A"].String()) || strings.Contains(got, w.tenant["A"].String()) || strings.Contains(got, "SECRET") {
		t.Fatalf("the signal leaked content: %q", got)
	}
}

func TestHubEvents_ABurstIsOneSignalAndAScopedContractHidesTheOtherQueues(t *testing.T) {
	w := newWorld(t)
	api := w.eventsAPI(t, fastEvents)
	agent := w.hubAgent("agent")
	w.grant(agent, "A")

	s := api.open(t, agent, "")
	for i := 0; i < 30; i++ {
		api.publish(w.tenant["A"], w.conv["A"])
	}
	n := 0
	for s.next(700*time.Millisecond) != "" {
		n++
	}
	if n < 1 || n > 4 {
		t.Fatalf("30 events in a burst produced %d signals, want a few (coalesced)", n)
	}

	// the contract names only queue 1: a conversation of queue 2 (or of none) must not be signalled; one of queue 1 is
	w.queueScope("A", w.queue1["A"])
	time.Sleep(700 * time.Millisecond) // the scope is part of what the stream asks again every recheck
	api.publish(w.tenant["A"], w.conv["A2"])
	if got := s.next(700 * time.Millisecond); got != "" {
		t.Fatalf("a conversation outside the contract's queues was signalled: %q", got)
	}
	api.publish(w.tenant["A"], w.conv["A"])
	if got := s.next(2 * time.Second); got != "event: changed|data: {}" {
		t.Fatalf("a conversation inside the contract's queues was not signalled: %q", got)
	}
}

func TestHubEvents_TheRelationshipEndingEndsOrNarrowsTheStream(t *testing.T) {
	w := newWorld(t)
	api := w.eventsAPI(t, fastEvents)
	agent := w.hubAgent("agent")
	gA := w.grant(agent, "A")
	w.grant(agent, "B")

	s := api.open(t, agent, "")
	api.publish(w.tenant["B"], w.conv["B"])
	if s.next(2*time.Second) == "" {
		t.Fatal("no signal before the revocation")
	}
	// the grant on B is revoked: B stops being signalled after the next recheck, A keeps working
	w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1 AND tenant_id = $2`, agent, w.tenant["B"])
	time.Sleep(700 * time.Millisecond)
	api.publish(w.tenant["B"], w.conv["B"])
	if got := s.next(700 * time.Millisecond); got != "" {
		t.Fatalf("a revoked company was still signalled: %q", got)
	}
	api.publish(w.tenant["A"], w.conv["A"])
	if s.next(2*time.Second) == "" {
		t.Fatal("the company that is still served stopped being signalled")
	}
	// leaving the Hub ends the stream
	_ = gA
	w.exec(`DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, w.hub, agent)
	if !s.endsWithin(2 * time.Second) {
		t.Fatal("the stream outlived the person's membership of the Hub")
	}
}

func TestHubEvents_AdmissionAndLimits(t *testing.T) {
	w := newWorld(t)
	api := w.eventsAPI(t, fastEvents)
	agent := w.hubAgent("agent")
	w.grant(agent, "A")
	stranger := w.user("stranger")
	memberOnly := w.user("member")
	w.directMember(memberOnly, "A")

	if s := api.open(t, uuid.Nil, ""); s.status != http.StatusUnauthorized {
		t.Errorf("no credential: %d, want 401", s.status)
	}
	if s := api.open(t, stranger, ""); s.status != http.StatusNotFound {
		t.Errorf("a person who is not in the Hub: %d, want 404", s.status)
	}
	if s := api.open(t, memberOnly, ""); s.status != http.StatusNotFound {
		t.Errorf("a company member who is not in the Hub: %d, want 404 (a company membership is not a Hub membership)", s.status)
	}
	if s := api.open(t, agent, "?tenant_id="+w.tenant["A"].String()); s.status != http.StatusBadRequest {
		t.Errorf("a company chosen by the client: %d, want 400", s.status)
	}
	one, two := api.open(t, agent, ""), api.open(t, agent, "")
	if one.status != 200 || two.status != 200 {
		t.Fatalf("the first two streams: %d, %d", one.status, two.status)
	}
	if third := api.open(t, agent, ""); third.status != http.StatusTooManyRequests {
		t.Errorf("a third stream for the same person: %d, want 429", third.status)
	}
}
