// Package e2e holds the automated vertical test of the goal flow, without a phone:
// connect a WAHA number -> inbound webhook (signed like WAHA) -> Inbox -> claim -> reply
// -> worker delivery -> acks. Everything is real (HTTP handlers, middleware, Postgres as the
// application role, encrypted credentials, realtime NOTIFY) except WhatsApp itself, which is a
// fake WAHA HTTP server. Real WhatsApp is covered by scripts/w3-smoke.sh (needs a phone).
package e2e_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	channelapp "github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	inboxapp "github.com/omnira/omnira/internal/inbox/application"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapp "github.com/omnira/omnira/internal/routing/application"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapp "github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/worker/delivery"
)

// ---- fake WAHA (WhatsApp) ----

type fakeWAHA struct {
	mu       sync.Mutex
	sends    []map[string]string
	failWith int // when non-zero, /api/sendText answers this status
	srv      *httptest.Server
}

func newFakeWAHA(t *testing.T) *fakeWAHA {
	f := &fakeWAHA{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "waha-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/new-message-id") && r.Method == http.MethodGet {
			f.mu.Lock()
			defer f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("reserved-e2e-%d", len(f.sends)+1)})
			return
		}
		if r.URL.Path == "/api/sendText" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var req map[string]string
			_ = json.Unmarshal(body, &req)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.failWith != 0 {
				http.Error(w, "rejected", f.failWith)
				return
			}
			f.sends = append(f.sends, req)
			// Echo the caller-supplied id verbatim, like real WAHA/GOWS
			// (PILOT.4A0/4A1) — a real send always carries a reserved id now.
			id := req["id"]
			if id == "" {
				id = fmt.Sprintf("true_%s_ABC%d", req["chatId"], len(f.sends))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWAHA) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

// ---- fake session controller (pairing is a phone step; covered in W3) ----

type pairedSessions struct{}

func (pairedSessions) Status(context.Context, domain.ChannelConnection) (ports.SessionStatus, error) {
	return ports.SessionWorking, nil
}
func (pairedSessions) Create(context.Context, domain.ChannelConnection, string) error { return nil }
func (pairedSessions) Start(context.Context, domain.ChannelConnection) error          { return nil }
func (pairedSessions) Stop(context.Context, domain.ChannelConnection) error           { return nil }
func (pairedSessions) QR(context.Context, domain.ChannelConnection) (ports.QRImage, error) {
	return ports.QRImage{}, nil
}
func (pairedSessions) Account(context.Context, domain.ChannelConnection) (string, error) {
	return "5511900000000", nil
}

// ---- the stack ----

type stack struct {
	t                             *testing.T
	seed, app                     *pgxpool.Pool
	mux                           *http.ServeMux
	waha                          *fakeWAHA
	credStore                     ports.CredentialStore
	delivery                      *delivery.Handler
	tenantA, tenantB              uuid.UUID
	admin, agent1, agent2, agentB uuid.UUID
}

func (s *stack) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.seed.Exec(context.Background(), sql, args...); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
}

func (s *stack) count(sql string, args ...any) (n int) {
	s.t.Helper()
	if err := s.seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return
}

func newStack(t *testing.T) *stack {
	t.Helper()
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{t: t, seed: seed, app: app, waha: newFakeWAHA(t), tenantA: uuid.New(), tenantB: uuid.New()}
	users := []*uuid.UUID{&s.admin, &s.agent1, &s.agent2, &s.agentB}
	for _, u := range users {
		*u = uuid.New()
		s.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, *u, *u, u.String()+"@invalid")
	}
	for _, tn := range []uuid.UUID{s.tenantA, s.tenantB} {
		s.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
		s.exec(`INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Default','manual',true)`, uuid.New(), tn)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM outbox_events WHERE tenant_id IN ($1,$2)`, s.tenantA, s.tenantB)
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, s.tenantA, s.tenantB)
		for _, u := range users {
			_, _ = seed.Exec(bg, `DELETE FROM users WHERE id=$1`, *u)
		}
		seed.Close()
		app.Close()
	})
	role := func(key string) (id uuid.UUID) {
		if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return
	}
	member := func(tn, u uuid.UUID, r string) {
		s.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tn, u, role(r))
	}
	member(s.tenantA, s.admin, "tenant_admin")
	member(s.tenantA, s.agent1, "tenant_agent")
	member(s.tenantA, s.agent2, "tenant_agent")
	member(s.tenantB, s.agentB, "tenant_admin")
	// IAM4 routing requires an explicit operational profile; role alone remains insufficient.
	s.exec(`INSERT INTO agent_profiles(tenant_id,membership_id,status)
		SELECT tenant_id,id,'active' FROM memberships WHERE tenant_id=$1 AND user_id IN ($2,$3)`, s.tenantA, s.agent1, s.agent2)
	s.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,available,capacity)
		SELECT q.tenant_id,q.id,u.id,true,2 FROM queues q CROSS JOIN (VALUES ($2::uuid),($3::uuid)) AS u(id)
		WHERE q.tenant_id=$1 AND q.is_default`, s.tenantA, s.agent1, s.agent2)

	// Real wiring, as in apps/api and apps/worker.
	cipher, err := channelcrypto.NewAESGCM([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	s.credStore = channeladapters.NewPostgresCredentialStore(app, cipher)
	connRepo := channeladapters.NewPostgresChannelConnectionRepository(app)
	eventStore := channeladapters.NewPostgresWebhookEventStore(app)
	perms := channeladapters.NewPostgresPermissionChecker(app)
	auditRepo := auditadapters.NewPostgresAuditEventRepository(app)

	wahaClient, err := waha.NewClient(s.waha.srv.URL, "waha-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := waha.NewProvider(wahaClient, s.credStore)
	if err != nil {
		t.Fatal(err)
	}
	inboundStore := inboxadapters.NewPostgresInboundStore(app)
	inbound := inboxapp.NewInboundService(inboundStore, inboundStore, inboundStore, inboxadapters.TicketStore{PostgresInboundStore: inboundStore}, inboundStore)
	intake := inboxadapters.NewWebhookIntake(app, eventStore, inbound)
	webhook := waha.NewWebhookHandler(provider, channeladapters.NewWahaWebhookConnectionResolver(app, connRepo), eventStore).UseSession(func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
		return platformdb.WithSystemTenantSession(ctx, app, tenantID, fn)
	}).UseIntake(intake)

	registry := channelapp.NewMapProviderRegistry()
	registry.Register(domain.ProviderWAHA, provider)
	s.delivery, err = delivery.NewHandler(delivery.NewPostgresOutboundStore(app), channelapp.NewChannelService(connRepo, registry), delivery.MaxAttempts)
	if err != nil {
		t.Fatal(err)
	}

	authz := tenancyapp.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	mw := tenancyadapters.AuthorizationMiddleware(app, authz)
	inboxAPI := inboxadapters.NewInboxAPIHandler(app)
	assign := routingadapters.NewAssignHandler(routingapp.NewAssigner(routingadapters.NewPostgresConversationAssigner(app), routingadapters.NewAuditRecorder(auditRepo)))
	send := messagesadapters.NewSendHandler(messagesapp.NewSender(messagesadapters.NewPostgresOutboundStore(app), perms))
	conns := channeladapters.NewConnectionHandler(channelapp.NewWahaConnectionService(connRepo, s.credStore, pairedSessions{}, perms, channeladapters.NewChannelAuditRecorder(auditRepo), "https://omnira.example.com"))

	inner := http.NewServeMux()
	const T = "/api/v1/tenants/{tenant_id}"
	inner.Handle("POST "+T+"/channels/waha/connections", mw(http.HandlerFunc(conns.Create)))
	inner.Handle("POST "+T+"/channels/waha/connections/{connection_id}/session/start", mw(http.HandlerFunc(conns.StartSession)))
	inner.Handle("GET "+T+"/inbox/conversations", mw(http.HandlerFunc(inboxAPI.ListConversations)))
	inner.Handle("GET "+T+"/inbox/conversations/{conversation_id}", mw(http.HandlerFunc(inboxAPI.GetConversation)))
	inner.Handle("GET "+T+"/inbox/conversations/{conversation_id}/messages", mw(http.HandlerFunc(inboxAPI.ListMessages)))
	inner.Handle("POST "+T+"/inbox/conversations/{conversation_id}/assign", mw(http.HandlerFunc(assign.Assign)))
	inner.Handle("POST "+T+"/inbox/conversations/{conversation_id}/messages", mw(http.HandlerFunc(send.Send)))
	inner.Handle("POST /webhooks/v1/whatsapp/waha/{connection_id}", webhook)
	s.mux = inner
	return s
}

type resp struct {
	code int
	body string
	m    map[string]any
}

func (s *stack) api(user uuid.UUID, method, path, body string, headers ...string) resp {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	r := resp{code: rec.Code, body: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &r.m)
	return r
}

// webhookPost delivers a body signed the way WAHA does (HMAC-SHA512 of the raw body).
func (s *stack) webhookPost(connectionID uuid.UUID, key string, body string) int {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/v1/whatsapp/waha/"+connectionID.String(), strings.NewReader(body))
	mac := hmac.New(sha512.New, []byte(key))
	mac.Write([]byte(body))
	req.Header.Set("X-Webhook-Hmac", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Webhook-Hmac-Algorithm", "sha512")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec.Code
}

func want(t *testing.T, r resp, code int, msg string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("%s: code=%d body=%q want %d", msg, r.code, r.body, code)
	}
}

// listen collects realtime NOTIFY payloads for one tenant.
type listener struct {
	mu   sync.Mutex
	seen []map[string]any
}

func (s *stack) listen(tenant uuid.UUID) *listener {
	conn, err := pgx.Connect(context.Background(), os.Getenv("OMNIRA_APP_DATABASE_URL"))
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), "LISTEN omnira_inbox_events"); err != nil {
		s.t.Fatal(err)
	}
	l := &listener{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			n, err := conn.WaitForNotification(ctx)
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal([]byte(n.Payload), &m) == nil && m["tenant_id"] == tenant.String() {
				l.mu.Lock()
				l.seen = append(l.seen, m)
				l.mu.Unlock()
			}
		}
	}()
	s.t.Cleanup(func() { cancel(); <-done; conn.Close(context.Background()) })
	return l
}

func (l *listener) has(typ string, match func(data map[string]any) bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.seen {
		if m["type"] == typ && match(m["data"].(map[string]any)) {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- the vertical ----

func TestVerticalWhatsAppInboxFlow(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	rt := s.listen(s.tenantA)
	A := "/api/v1/tenants/" + s.tenantA.String()

	// 1. Admin connects a WhatsApp number (risk acknowledged) and the connection goes active.
	r := s.api(s.admin, "POST", A+"/channels/waha/connections", `{"risk_acknowledged":true}`)
	want(t, r, 201, "create connection")
	connID := uuid.MustParse(r.m["id"].(string))
	want(t, s.api(s.admin, "POST", A+"/channels/waha/connections/"+connID.String()+"/session/start", ""), 200, "start session")
	if n := s.count(`SELECT count(*) FROM channel_connections WHERE id=$1 AND status='active'`, connID); n != 1 {
		t.Fatal("connection did not become active after the (paired) session started")
	}
	var secretRef string
	if err := s.seed.QueryRow(ctx, `SELECT secret_ref::text FROM channel_connections WHERE id=$1`, connID).Scan(&secretRef); err != nil {
		t.Fatal(err)
	}
	var hmacKey string
	if err := platformdb.WithTenantSession(ctx, s.app, s.admin, false, func(sc context.Context) error {
		cred, err := s.credStore.Resolve(sc, secretRef)
		hmacKey = cred.Fields["webhook_hmac_key"]
		return err
	}); err != nil || len(hmacKey) != 64 {
		t.Fatalf("hmac key: %v len=%d", err, len(hmacKey))
	}
	session := "omnira_" + connID.String()

	// 2. A customer writes: signed webhook -> contact + conversation + message.
	msg := func(id, body string) string {
		return fmt.Sprintf(`{"id":"evt-%s","event":"message.any","session":%q,"payload":{"id":%q,"timestamp":1789000000,"from":"5511988887777@c.us","fromMe":false,"body":%q}}`, id, session, id, body)
	}
	if code := s.webhookPost(connID, "wrong-key", msg("in-0", "forged")); code != http.StatusUnauthorized {
		t.Fatalf("bad signature must be 401, got %d", code)
	}
	if n := s.count(`SELECT count(*) FROM messages WHERE tenant_id=$1`, s.tenantA); n != 0 {
		t.Fatal("unsigned webhook wrote data")
	}
	if code := s.webhookPost(connID, hmacKey, msg("in-1", "Oi, preciso de ajuda")); code != 200 {
		t.Fatalf("inbound webhook: %d", code)
	}
	if code := s.webhookPost(connID, hmacKey, msg("in-1", "Oi, preciso de ajuda")); code != 200 { // WAHA redelivery
		t.Fatalf("redelivery: %d", code)
	}
	if n := s.count(`SELECT count(*) FROM messages WHERE tenant_id=$1 AND direction='inbound'`, s.tenantA); n != 1 {
		t.Fatalf("redelivery duplicated the inbound message (%d)", n)
	}

	// 3. The agent sees it in the Inbox.
	r = s.api(s.agent1, "GET", A+"/inbox/conversations", "")
	want(t, r, 200, "list conversations")
	items := r.m["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("inbox items=%d", len(items))
	}
	conv := items[0].(map[string]any)
	convID := conv["id"].(string)
	if conv["contact_phone"] != "+5511988887777" || conv["channel_connection_id"] != connID.String() {
		t.Fatalf("conversation: %v", conv)
	}
	r = s.api(s.agent1, "GET", A+"/inbox/conversations/"+convID+"/messages", "")
	want(t, r, 200, "messages")
	if got := r.m["items"].([]any)[0].(map[string]any); got["body"] != "Oi, preciso de ajuda" || got["direction"] != "inbound" {
		t.Fatalf("message: %v", got)
	}
	waitFor(t, "realtime message_received (inbound)", func() bool {
		return rt.has("message_received", func(d map[string]any) bool { return d["direction"] == "inbound" })
	})

	// 4. Cannot reply before claiming; exactly one of two agents wins the claim.
	want(t, s.api(s.agent1, "POST", A+"/inbox/conversations/"+convID+"/messages", `{"text":"antes"}`, "Idempotency-Key", "k-before-claim"), 409, "reply before claim")
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, u := range []uuid.UUID{s.agent1, s.agent2} {
		wg.Add(1)
		go func(i int, u uuid.UUID) {
			defer wg.Done()
			codes[i] = s.api(u, "POST", A+"/inbox/conversations/"+convID+"/assign", "").code
		}(i, u)
	}
	wg.Wait()
	winner := s.agent1
	if codes[1] == 200 {
		winner = s.agent2
	}
	if !((codes[0] == 200 && codes[1] == 409) || (codes[0] == 409 && codes[1] == 200)) {
		t.Fatalf("claim race codes=%v", codes)
	}
	loser := s.agent2
	if winner == s.agent2 {
		loser = s.agent1
	}
	waitFor(t, "realtime conversation_updated (assignment)", func() bool {
		return rt.has("conversation_updated", func(d map[string]any) bool { return d["assigned_to_user_id"] == winner.String() })
	})

	// 5. The winner replies (idempotent); the loser cannot.
	want(t, s.api(loser, "POST", A+"/inbox/conversations/"+convID+"/messages", `{"text":"intruso"}`, "Idempotency-Key", "key-loser-0001"), 403, "non-assignee reply")
	reply := s.api(winner, "POST", A+"/inbox/conversations/"+convID+"/messages", `{"text":"Olá! Como posso ajudar?"}`, "Idempotency-Key", "k-reply-1")
	want(t, reply, 202, "reply")
	want(t, s.api(winner, "POST", A+"/inbox/conversations/"+convID+"/messages", `{"text":"Olá! Como posso ajudar?"}`, "Idempotency-Key", "k-reply-1"), 200, "reply replay")
	msgID := uuid.MustParse(reply.m["id"].(string))
	if n := s.count(`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='job.channel.send_text.v1'`, msgID.String()); n != 1 {
		t.Fatalf("delivery jobs=%d", n)
	}

	// 6. The worker delivers it through (fake) WAHA, exactly once even on redelivery.
	job, _ := json.Marshal(map[string]string{"aggregate_id": msgID.String()})
	if err := s.delivery.Handle(ctx, job, 1); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if err := s.delivery.Handle(ctx, job, 2); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if s.waha.sendCount() != 1 {
		t.Fatalf("WAHA received %d sends for one message", s.waha.sendCount())
	}
	sent := s.waha.sends[0]
	if sent["session"] != session || sent["chatId"] != "5511988887777@c.us" || sent["text"] != "Olá! Como posso ajudar?" {
		t.Fatalf("WAHA request: %v", sent)
	}
	var status, providerID string
	if err := s.seed.QueryRow(ctx, `SELECT status, provider_message_id FROM messages WHERE id=$1`, msgID).Scan(&status, &providerID); err != nil || status != "sent" || providerID == "" {
		t.Fatalf("after delivery: %s %q %v", status, providerID, err)
	}

	// 7. WAHA acks (signed webhooks): delivered, read; duplicates and stale acks are harmless.
	ack := func(evt, name string) int {
		return s.webhookPost(connID, hmacKey, fmt.Sprintf(`{"id":%q,"event":"message.ack","session":%q,"payload":{"id":%q,"ackName":%q,"timestamp":1789000100}}`, evt, session, providerID, name))
	}
	for _, step := range [][2]string{{"ack-1", "DEVICE"}, {"ack-1", "DEVICE"}, {"ack-2", "READ"}, {"ack-3", "PENDING"} /* stale */} {
		if code := ack(step[0], step[1]); code != 200 {
			t.Fatalf("ack %v: %d", step, code)
		}
	}
	if err := s.seed.QueryRow(ctx, `SELECT status FROM messages WHERE id=$1`, msgID).Scan(&status); err != nil || status != "read" {
		t.Fatalf("after acks status=%s (a stale ack must not downgrade)", status)
	}
	for _, st := range []string{"sent", "delivered", "read"} {
		st := st
		waitFor(t, "realtime message_status "+st, func() bool {
			return rt.has("message_status", func(d map[string]any) bool { return d["message_id"] == msgID.String() && d["status"] == st })
		})
	}
	waitFor(t, "realtime message_received (outbound queued)", func() bool {
		return rt.has("message_received", func(d map[string]any) bool { return d["message_id"] == msgID.String() && d["direction"] == "outbound" })
	})

	// 8. A provider rejection is recorded as failed with a class-only reason (no retry).
	s.waha.mu.Lock()
	s.waha.failWith = http.StatusUnprocessableEntity
	s.waha.mu.Unlock()
	bad := s.api(winner, "POST", A+"/inbox/conversations/"+convID+"/messages", `{"text":"vai falhar"}`, "Idempotency-Key", "k-reply-2")
	want(t, bad, 202, "second reply")
	badJob, _ := json.Marshal(map[string]string{"aggregate_id": bad.m["id"].(string)})
	if err := s.delivery.Handle(ctx, badJob, 1); err != nil {
		t.Fatalf("permanent failure must ack: %v", err)
	}
	var reason string
	if err := s.seed.QueryRow(ctx, `SELECT status, failure_reason FROM messages WHERE id=$1`, bad.m["id"]).Scan(&status, &reason); err != nil || status != "failed" || reason != "rejected" {
		t.Fatalf("rejected send: %s %q %v", status, reason, err)
	}

	// 9. Tenant B sees nothing of tenant A and cannot reuse A's credentials.
	B := "/api/v1/tenants/" + s.tenantB.String()
	r = s.api(s.agentB, "GET", B+"/inbox/conversations", "")
	want(t, r, 200, "tenant B list")
	if len(r.m["items"].([]any)) != 0 {
		t.Fatalf("tenant B sees tenant A conversations: %s", r.body)
	}
	want(t, s.api(s.agentB, "GET", B+"/inbox/conversations/"+convID, ""), 404, "tenant B reads tenant A conversation")
	want(t, s.api(s.agentB, "GET", A+"/inbox/conversations/"+convID, ""), 404, "tenant B on A's path")
	if code := s.webhookPost(connID, "another-tenants-key", msg("in-x", "cross-tenant")); code != http.StatusUnauthorized {
		t.Fatalf("webhook signed with another key: %d", code)
	}
	if n := s.count(`SELECT count(*) FROM messages WHERE tenant_id=$1`, s.tenantB); n != 0 {
		t.Fatal("tenant B has messages it never received")
	}
}
