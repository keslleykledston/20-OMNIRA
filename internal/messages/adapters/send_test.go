package adapters_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/messages/ports"
	"github.com/omnira/omnira/internal/testhelpers"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/testhelpers"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

type env struct {
	t                                                      *testing.T
	seed, app                                              *pgxpool.Pool
	mux                                                    *http.ServeMux
	tenantA, tenantB                                       uuid.UUID
	agent1, agent2, supervisor, viewer, revoked            uuid.UUID
	convA, convUnassigned, convInactive, convNoConn, convB uuid.UUID
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, seed: seed, app: app, tenantA: uuid.New(), tenantB: uuid.New()}
	users := []*uuid.UUID{&e.agent1, &e.agent2, &e.supervisor, &e.viewer, &e.revoked}
	for _, u := range users {
		*u = uuid.New()
		e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, *u, *u, u.String()+"@invalid")
	}
	for _, tn := range []uuid.UUID{e.tenantA, e.tenantB} {
		e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM outbox_events WHERE tenant_id IN ($1,$2)`, e.tenantA, e.tenantB)
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, e.tenantA, e.tenantB)
		for _, u := range users {
			_, _ = seed.Exec(bg, `DELETE FROM users WHERE id=$1`, *u)
		}
		_, _ = seed.Exec(bg, `DELETE FROM roles WHERE tenant_id=$1`, e.tenantA)
		seed.Close()
		app.Close()
	})
	role := func(key string) (id uuid.UUID) {
		if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return
	}
	viewerRole := uuid.New()
	e.exec(`INSERT INTO roles(id,tenant_id,key,name) VALUES($1,$2,'viewer_only','Viewer')`, viewerRole, e.tenantA)
	e.exec(`INSERT INTO role_permissions(role_id,permission_key) VALUES($1,'tenant.read')`, viewerRole)
	member := func(tn, u, r uuid.UUID, status string) {
		e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4)`, tn, u, r, status)
	}
	member(e.tenantA, e.agent1, role("tenant_agent"), "active")
	member(e.tenantA, e.agent2, role("tenant_agent"), "active")
	member(e.tenantA, e.supervisor, role("tenant_supervisor"), "active")
	member(e.tenantA, e.viewer, viewerRole, "active")
	member(e.tenantA, e.revoked, role("tenant_agent"), "revoked")

	conn := func(tn uuid.UUID, status string) uuid.UUID {
		id := uuid.New()
		e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,$4,'["text"]')`, id, tn, id.String(), status)
		return id
	}
	n := 0
	conv := func(tn uuid.UUID, connID *uuid.UUID, assignee *uuid.UUID) uuid.UUID {
		n++
		contact, c := uuid.New(), uuid.New()
		e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'C',$3)`, contact, tn, fmt.Sprintf("+55119%08d", contact.ID()%100000000))
		e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status,assigned_to_user_id) VALUES($1,$2,$3,$4,'open',$5)`, c, tn, contact, connID, assignee)
		return c
	}
	active, inactive := conn(e.tenantA, "active"), conn(e.tenantA, "disconnected")
	e.convA = conv(e.tenantA, &active, &e.agent1)
	e.convUnassigned = conv(e.tenantA, &active, nil)
	e.convInactive = conv(e.tenantA, &inactive, &e.agent1)
	e.convNoConn = conv(e.tenantA, nil, &e.agent1)
	activeB := conn(e.tenantB, "active")
	e.convB = conv(e.tenantB, &activeB, nil)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	mw := tenancyadapters.AuthorizationMiddleware(app, authz)
	h := messagesadapters.NewSendHandler(messagesapplication.NewSender(messagesadapters.NewPostgresOutboundStore(app), channeladapters.NewPostgresPermissionChecker(app)))
	e.mux = http.NewServeMux()
	e.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", mw(http.HandlerFunc(h.Send)))
	return e
}

type res struct {
	code     int
	body     string
	m        map[string]any
	replayed bool
}

func (e *env) send(user, tenant, conv uuid.UUID, key, body string) res {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenant.String()+"/inbox/conversations/"+conv.String()+"/messages", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	r := res{code: rec.Code, body: rec.Body.String(), replayed: rec.Header().Get("Idempotent-Replayed") == "true"}
	_ = json.Unmarshal(rec.Body.Bytes(), &r.m)
	return r
}

func (e *env) count(sql string, args ...any) (n int) {
	if err := e.seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return
}

func code(t *testing.T, r res, want int, msg string) {
	t.Helper()
	if r.code != want {
		t.Fatalf("%s: code=%d body=%q want %d", msg, r.code, r.body, want)
	}
}

func key() string { return "key-" + uuid.NewString() }

func TestSendQueuesMessageAndReferenceOnlyJob(t *testing.T) {
	e := newEnv(t)
	k := key()
	r := e.send(e.agent1, e.tenantA, e.convA, k, `{"text":"olá, tudo bem?","tenant_id":"`+e.tenantB.String()+`"}`)
	code(t, r, 202, "assigned agent sends")
	id := r.m["id"].(string)
	if r.m["status"] != "queued" || r.m["direction"] != "outbound" || r.m["body"] != "olá, tudo bem?" {
		t.Fatalf("response: %v", r.m)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE id=$1 AND tenant_id=$2 AND conversation_id=$3 AND direction='outbound' AND status='queued' AND sent_by_user_id=$4 AND idempotency_key=$5 AND channel_connection_id IS NOT NULL AND provider_message_id=''`, id, e.tenantA, e.convA, e.agent1, k); n != 1 {
		t.Fatalf("message row mismatch (%d)", n)
	}
	// The delivery job carries only a reference: no text, phone or secret in the queue.
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.channel.send_text.v1' AND aggregate_type='message' AND aggregate_id=$2 AND payload='{}'::jsonb`, e.tenantA, id); n != 1 {
		t.Fatalf("outbox job mismatch (%d)", n)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE tenant_id=$1`, e.tenantB); n != 0 {
		t.Fatal("forged tenant_id in body leaked into another tenant")
	}
}

func TestSendIdempotency(t *testing.T) {
	e := newEnv(t)
	k := key()
	first := e.send(e.agent1, e.tenantA, e.convA, k, `{"text":"uma vez"}`)
	code(t, first, 202, "first")
	again := e.send(e.agent1, e.tenantA, e.convA, k, `{"text":"uma vez"}`)
	code(t, again, 200, "replay")
	if !again.replayed || again.m["id"] != first.m["id"] {
		t.Fatalf("replay must return the same message: %v", again.m)
	}
	code(t, e.send(e.agent1, e.tenantA, e.convA, k, `{"text":"outro texto"}`), 422, "same key, different body")
	if n := e.count(`SELECT count(*) FROM messages WHERE tenant_id=$1 AND idempotency_key=$2`, e.tenantA, k); n != 1 {
		t.Fatalf("messages for key=%d", n)
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, first.m["id"]); n != 1 {
		t.Fatalf("jobs for message=%d", n)
	}
	// Keys are scoped per sender: the same key from another authorized sender is independent.
	other := e.send(e.supervisor, e.tenantA, e.convA, k, `{"text":"uma vez"}`)
	code(t, other, 202, "same key, other sender")
	if other.m["id"] == first.m["id"] {
		t.Fatal("idempotency key leaked across senders")
	}
	// Concurrent retries of the same request create exactly one message and one job.
	ck := key()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.send(e.agent1, e.tenantA, e.convA, ck, `{"text":"concorrente"}`)
			if r.code != 200 && r.code != 202 {
				t.Errorf("concurrent code=%d body=%q", r.code, r.body)
				return
			}
			ids <- r.m["id"].(string)
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("concurrent retries produced %d distinct messages", len(seen))
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE tenant_id=$1 AND idempotency_key=$2`, e.tenantA, ck); n != 1 {
		t.Fatalf("concurrent rows=%d", n)
	}
}

func TestSendValidationAndAuthorization(t *testing.T) {
	e := newEnv(t)
	body := `{"text":"oi"}`
	// Validation.
	code(t, e.send(e.agent1, e.tenantA, e.convA, "", body), 400, "missing key")
	code(t, e.send(e.agent1, e.tenantA, e.convA, "short", body), 400, "short key")
	code(t, e.send(e.agent1, e.tenantA, e.convA, "bad key with spaces", body), 400, "malformed key")
	code(t, e.send(e.agent1, e.tenantA, e.convA, key(), `not json`), 400, "malformed body")
	code(t, e.send(e.agent1, e.tenantA, e.convA, key(), `{"text":"   "}`), 422, "blank text")
	code(t, e.send(e.agent1, e.tenantA, e.convA, key(), `{"text":"`+strings.Repeat("a", 4097)+`"}`), 422, "too long")
	// Assignment / role rules.
	code(t, e.send(e.agent2, e.tenantA, e.convA, key(), body), 403, "agent not assigned")
	code(t, e.send(e.agent1, e.tenantA, e.convUnassigned, key(), body), 409, "unassigned conversation")
	code(t, e.send(e.viewer, e.tenantA, e.convA, key(), body), 403, "role without conversation.claim")
	code(t, e.send(e.revoked, e.tenantA, e.convA, key(), body), 404, "revoked membership")
	code(t, e.send(e.supervisor, e.tenantA, e.convA, key(), body), 202, "supervisor (conversation.manage) may reply on any conversation")
	// Channel state.
	code(t, e.send(e.agent1, e.tenantA, e.convInactive, key(), body), 409, "channel not active")
	code(t, e.send(e.agent1, e.tenantA, e.convNoConn, key(), body), 409, "no channel")
	// Only the one allowed request wrote anything.
	if n := e.count(`SELECT count(*) FROM messages WHERE tenant_id=$1 AND direction='outbound'`, e.tenantA); n != 1 {
		t.Fatalf("rejected requests wrote messages: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.channel.send_text.v1'`, e.tenantA); n != 1 {
		t.Fatalf("rejected requests enqueued jobs: %d", n)
	}
}

func TestSendTenantIsolation(t *testing.T) {
	e := newEnv(t)
	body := `{"text":"oi"}`
	// Tenant B's conversation UUID is useless from tenant A (same answer as an unknown id).
	a := e.send(e.agent1, e.tenantA, e.convB, key(), body)
	b := e.send(e.agent1, e.tenantA, uuid.New(), key(), body)
	if a.code != 404 || b.code != 404 || a.body != b.body {
		t.Fatalf("enumeration oracle: %d %q vs %d %q", a.code, a.body, b.code, b.body)
	}
	code(t, e.send(e.agent1, e.tenantB, e.convB, key(), body), 404, "tenant B path without membership")
	if n := e.count(`SELECT count(*) FROM messages WHERE tenant_id=$1 AND direction='outbound'`, e.tenantB); n != 0 {
		t.Fatalf("cross-tenant message created: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1`, e.tenantB); n != 0 {
		t.Fatalf("cross-tenant job created: %d", n)
	}
}

// A stale LoadSendContext (assignee changed between the checks and the insert) must not let the
// former assignee queue a reply, and must not leave a message or job behind.
type staleStore struct {
	ports.OutboundStore
	assignee uuid.UUID
}

func (s staleStore) LoadSendContext(ctx context.Context, id uuid.UUID) (*ports.SendContext, error) {
	sc, err := s.OutboundStore.LoadSendContext(ctx, id)
	if sc != nil {
		sc.AssignedTo = &s.assignee // what the sender saw before the reassignment
	}
	return sc, err
}

func TestSendRechecksAssigneeAtInsertTime(t *testing.T) {
	e := newEnv(t)
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	mw := tenancyadapters.AuthorizationMiddleware(e.app, authz)
	perms := channeladapters.NewPostgresPermissionChecker(e.app)
	stale := messagesadapters.NewSendHandler(messagesapplication.NewSender(staleStore{messagesadapters.NewPostgresOutboundStore(e.app), e.agent1}, perms))
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", mw(http.HandlerFunc(stale.Send)))
	post := func(user uuid.UUID, conv uuid.UUID) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+e.tenantA.String()+"/inbox/conversations/"+conv.String()+"/messages", strings.NewReader(`{"text":"oi"}`))
		req.Header.Set("Idempotency-Key", key())
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	// convA is assigned to agent1 in the fixture; move it to agent2 behind the sender's back.
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, e.convA, e.agent2)
	if code := post(e.agent1, e.convA); code != http.StatusConflict {
		t.Fatalf("former assignee (stale view) got %d, want 409", code)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, e.convA); n != 0 {
		t.Fatalf("stale send created %d messages", n)
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.channel.send_text.v1'`, e.tenantA); n != 0 {
		t.Fatalf("stale send enqueued %d jobs", n)
	}
	// A manager may still reply on a conversation assigned to someone else (no assignee predicate for them).
	if code := post(e.supervisor, e.convA); code != http.StatusAccepted {
		t.Fatalf("manager send got %d", code)
	}
	// Channel disabled after the checks: refused too.
	e.exec(`UPDATE channel_connections SET status='disconnected' WHERE id=(SELECT channel_connection_id FROM conversations WHERE id=$1)`, e.convA)
	if code := post(e.supervisor, e.convA); code != http.StatusConflict {
		t.Fatalf("channel went inactive after the checks: got %d, want 409", code)
	}
}
