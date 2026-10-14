package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	"github.com/omnira/omnira/internal/conversationcontrol"
	"github.com/omnira/omnira/internal/external/crmsim"
	"github.com/omnira/omnira/internal/external/delivery"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/platform/authn"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapplication "github.com/omnira/omnira/internal/routing/application"
)

// H6 gate: a real Workspace (the same HTTP handlers and use cases the app uses) and the reference CRM (internal/external/crmsim, which only knows the HTTP contract)
// working the SAME company at the same time, against real PostgreSQL, with a webhook receiver that goes down and comes back.

// loseFirstResponse delivers the request, then loses the answer: the client cannot know whether it was processed (the classic timeout).
type loseFirstResponse struct {
	inner http.RoundTripper
	key   string
	lost  atomic.Bool
}

func (t *loseFirstResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(r)
	if err == nil && r.Header.Get("Idempotency-Key") == t.key && t.lost.CompareAndSwap(false, true) {
		resp.Body.Close()
		return nil, errors.New("read tcp: connection reset by peer")
	}
	return resp, err
}

func (e *env) workspaceAPI() {
	audit := auditadapters.NewPostgresAuditEventRepository(e.app)
	assign := routingadapters.NewAssignHandler(routingapplication.NewAssigner(routingadapters.NewPostgresConversationAssigner(e.app), routingadapters.NewAuditRecorder(audit)))
	take := conversationcontrol.NewTakeoverHandler(e.app, audit)
	e.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/assign", e.session(http.HandlerFunc(assign.Assign)))
	e.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/takeover", e.session(http.HandlerFunc(take.Takeover)))
}

func (e *env) ws(user, tenant uuid.UUID, path string, body any) (int, []byte) {
	e.t.Helper()
	return e.wsAt(user, tenant, "/api/v1/tenants/"+tenant.String()+path, body)
}

func (e *env) workspaceSend(tenant, user, conv uuid.UUID, text, key string) error {
	sender := messagesapp.NewSender(messagesadapters.NewPostgresOutboundStore(e.app), channeladapters.NewPostgresPermissionChecker(e.app))
	return e.human(tenant, user, func(ctx context.Context) error {
		_, err := sender.Send(ctx, conv, text, key)
		return err
	})
}

func TestE2EWorkspaceAndTheReferenceCRMWorkTheSameCompany(t *testing.T) {
	h := newHookEnv(t)
	h.writeAPI()
	h.workspaceAPI()
	tn := h.tenant()
	admin := h.member(tn, "tenant_admin")
	agent := h.member(tn, "tenant_agent")
	supervisor := h.member(tn, "tenant_supervisor")
	h.exec(`INSERT INTO agent_profiles(id,tenant_id,membership_id,status) SELECT gen_random_uuid(), $1, m.id, 'active' FROM memberships m WHERE m.tenant_id=$1 AND m.user_id=$2`, tn, agent)
	q := h.queue(tn)
	h.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,true,true,50)`, tn, q, agent)
	conn := h.connection(tn)

	// the CRM's installation, a read-only one, and a TLS receiver for the CRM's webhooks
	code, body := h.admin(admin, tn, http.MethodPost, "", map[string]any{"name": "CRM de referência", "scopes": writeScopes, "rate_limit_per_min": 6000})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var crmApp created
	mustJSON(t, body, &crmApp)
	readOnly := h.install(admin, tn, "só leitura", "conversation.read")
	secret := []byte("segredo-do-webhook-do-simulador")
	rcv := crmsim.NewReceiver(secret)
	tls := httptest.NewTLSServer(rcv)
	t.Cleanup(tls.Close)
	rcvDummy := &receiver{srv: tls} // reuse the helper that lets the worker trust this server
	sub := h.subscribeWith(crmApp, tn, tls.URL+"/omnira/events", secret)
	w := h.worker(clientFor(rcvDummy))
	api := httptest.NewServer(h.ext)
	t.Cleanup(api.Close)
	flaky := &loseFirstResponse{inner: http.DefaultTransport, key: "e2e-send-flaky-0001"}
	crm := crmsim.NewClient(api.URL+"/api/v1/external", crmApp.Credential.Token, &http.Client{Transport: flaky, Timeout: 20 * time.Second})
	crm.Sleep = func(time.Duration) {}
	ro := crmsim.NewClient(api.URL+"/api/v1/external", readOnly.Credential.Token, nil)
	ctx := context.Background()

	// conversations: three ordinary ones, one in a queue that is closed now, one with a team member (invisible)
	var convs [3]uuid.UUID
	for i := range convs {
		convs[i], _ = h.conv(tn, convOpts{queue: &q, conn: &conn})
		h.message(tn, convs[i], "inbound", fmt.Sprintf("olá %d", i), "received", time.Minute, nil)
	}
	closedCal := uuid.New()
	h.exec(`INSERT INTO calendars(id,tenant_id,name,timezone,kind,weekly) VALUES($1,$2,'fechado','America/Manaus','custom','[{"day":"mon","intervals":[]}]'::jsonb)`, closedCal, tn)
	qClosed := h.queue(tn)
	h.exec(`UPDATE queues SET calendar_id=$2 WHERE id=$1`, qClosed, closedCal)
	afterHours, _ := h.conv(tn, convOpts{queue: &qClosed, conn: &conn})
	staff := h.member(tn, "tenant_agent")
	staffConv, _ := h.conv(tn, convOpts{staff: &staff})
	h.sequence()

	// 1. bootstrap: checkpoint -> snapshot -> events; the invisible conversation is not in it
	sync := crmsim.NewSyncer(crm)
	if err := sync.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, known := sync.Control[staffConv.String()]; known {
		t.Error("the CRM learned about a conversation with a team member")
	}
	if _, err := crm.Conversation(ctx, staffConv.String()); !crmsim.IsNotFound(err) {
		t.Errorf("a conversation with a team member = %v, want the uniform 404", err)
	}
	if len(sync.Control) != 4 {
		t.Fatalf("the CRM sees %d conversations, want 4", len(sync.Control))
	}

	// 2. two clients at the same instant: the Workspace agent and the CRM go for the same conversation; exactly one wins
	var wg sync2
	var wsCode int
	var crmErr error
	start := make(chan struct{})
	wg.Go(func() {
		<-start
		wsCode, _ = h.ws(agent, tn, "/inbox/conversations/"+convs[0].String()+"/assign", map[string]any{})
	})
	wg.Go(func() { <-start; _, crmErr = crm.Claim(ctx, convs[0].String(), crmsim.NewKey("claim"), 0) })
	close(start)
	wg.Wait()
	crmWon := crmErr == nil
	if crmWon == (wsCode == 200) {
		t.Fatalf("both or neither won: workspace=%d crm=%v", wsCode, crmErr)
	}
	if !crmWon && !(crmsim.IsConflict(crmErr)) {
		t.Fatalf("the CRM lost with %v, want a 409 held_by_other", crmErr)
	}
	// the free ones: the CRM takes convs[1]; the Workspace takes convs[2] first and the CRM is refused
	r1, err := crm.Claim(ctx, convs[1].String(), crmsim.NewKey("claim"), 0)
	if err != nil || r1.Control.Holder == nil || !r1.Control.Holder.Mine {
		t.Fatalf("claim of a free conversation: %+v %v", r1, err)
	}
	if code, _ := h.ws(agent, tn, "/inbox/conversations/"+convs[2].String()+"/assign", map[string]any{}); code != 200 {
		t.Fatalf("workspace claim = %d", code)
	}
	if _, err := crm.Claim(ctx, convs[2].String(), crmsim.NewKey("claim"), 0); !crmsim.IsConflict(err) {
		t.Errorf("the CRM claiming what a person holds = %v, want 409", err)
	}
	// the Workspace is refused on what the CRM holds, both to claim and to answer
	if code, _ := h.ws(agent, tn, "/inbox/conversations/"+convs[1].String()+"/assign", map[string]any{}); code != 409 {
		t.Errorf("the Workspace claiming what the CRM holds = %d, want 409", code)
	}
	if err := h.workspaceSend(tn, agent, convs[1], "por cima do CRM", "ws-over-crm-0001"); !errors.Is(err, messagesapp.ErrHeldByExternal) {
		t.Errorf("the Workspace answering over the CRM = %v, want ErrHeldByExternal", err)
	}
	// after hours: refused unless allowed; the invisible one is a 404; a read-only token cannot write
	if _, err := crm.Claim(ctx, afterHours.String(), crmsim.NewKey("claim"), 0); !crmsim.IsConflict(err) {
		t.Errorf("claim outside the queue's hours = %v, want 409 outside_hours", err)
	}
	if _, err := crm.Claim(ctx, staffConv.String(), crmsim.NewKey("claim"), 0); !crmsim.IsNotFound(err) {
		t.Errorf("claim of an invisible conversation = %v, want 404", err)
	}
	var pr *crmsim.Problem
	if _, err := ro.Claim(ctx, convs[1].String(), crmsim.NewKey("claim"), 0); !errors.As(err, &pr) || pr.Status != 403 {
		t.Errorf("a read-only token writing = %v, want 403", err)
	}

	// 3. the CRM answers; the answer is lost on the way back and the client repeats it with the SAME key: one message, the same id
	v := r1.Control.Version
	sent, err := crm.Send(ctx, convs[1].String(), flaky.key, v, "Olá! Aqui é o CRM.")
	if err != nil {
		t.Fatal(err)
	}
	if !flaky.lost.Load() || !sent.Replay || sent.MessageID == "" || sent.Delivery != "queued" {
		t.Fatalf("send after a lost answer: %+v lost=%v", sent, flaky.lost.Load())
	}
	if n := h.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, convs[1]); n != 1 {
		t.Fatalf("outbound messages = %d, want 1", n)
	}

	// 4. a supervisor takes the conversation back; the CRM's next command is refused, it re-reads and sees why, and never retries blindly
	if code, body := h.ws(supervisor, tn, "/inbox/conversations/"+convs[1].String()+"/takeover", map[string]any{"reason": "cliente pediu atendimento humano", "confirm": true}); code != 200 {
		t.Fatalf("takeover = %d %s", code, body)
	}
	_, err = crm.Send(ctx, convs[1].String(), crmsim.NewKey("send"), v, "ainda aqui?")
	if !crmsim.IsStale(err) && !crmsim.IsConflict(err) {
		t.Fatalf("the CRM writing after the takeover = %v, want 412/409", err)
	}
	now, err := sync.Reread(ctx, convs[1].String())
	if err != nil || now.Control.Holder == nil || now.Control.Holder.Kind != "tenant_user" || now.Control.Holder.Mine {
		t.Fatalf("after the refusal the CRM reads %+v (%v)", now.Control, err)
	}

	// 5. the CRM's endpoint is down for hours while the company keeps working; when it is back it recovers everything, the same ids, no duplicates
	rcv.SetOffline(true)
	clock := time.Now()
	w = w.WithClock(func() time.Time { return clock }, func() float64 { return 0 })
	rcv.SetClock(func() time.Time { return clock })
	for i := 0; i < 5; i++ {
		h.message(tn, convs[2], "inbound", fmt.Sprintf("durante a queda %d", i), "received", 0, nil)
	}
	h.step(w)
	if len(rcv.Events()) != 0 {
		t.Fatal("an offline endpoint accepted facts")
	}
	for hours := 0; hours < 3; hours++ {
		clock = clock.Add(time.Hour)
		h.makeDue(sub)
		h.step(w)
	}
	firstSeen := map[string]string{}
	rcv.SetOffline(false)
	clock = clock.Add(time.Hour)
	h.makeDue(sub)
	h.step(w)
	// the CRM catches up from the journal too (the source of truth), and both roads agree
	if err := sync.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	var journal int
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1 AND seq IS NOT NULL`, tn).Scan(&journal)
	got := rcv.Events()
	ids := map[string]bool{}
	for _, ev := range got {
		if ids[ev.EventID] {
			t.Errorf("the receiver recorded %s twice", ev.EventID)
		}
		ids[ev.EventID] = true
		firstSeen[ev.EventID] = rcv.DeliveryIDs[ev.EventID]
	}
	// every fact the CRM was subscribed to reached the receiver (the subscription began at the head when it was created: everything after it)
	var due int
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM webhook_deliveries WHERE subscription_id=$1 AND status='delivered'`, sub.id).Scan(&due)
	if len(got) != due || due == 0 {
		t.Fatalf("receiver has %d facts, deliveries delivered=%d", len(got), due)
	}
	if h.deliveries(sub, "pending") != 0 || h.deliveries(sub, "dead") != 0 {
		t.Fatalf("pending=%d dead=%d after the recovery", h.deliveries(sub, "pending"), h.deliveries(sub, "dead"))
	}
	// webhooks and journal give the CRM the same view of who holds what
	for _, c := range convs {
		var v int64
		_ = h.seed.QueryRow(h.ctx, `SELECT control_version FROM conversations WHERE id=$1`, c).Scan(&v)
		if sync.Control[c.String()].Version != v {
			t.Errorf("conversation %s: the CRM thinks v%d, the database has v%d (cursor=%d)", c, sync.Control[c.String()].Version, v, sync.Cursor)
			rows, _ := h.seed.Query(h.ctx, `SELECT seq, event_type, aggregate_version, origin_kind FROM domain_events WHERE conversation_id=$1 ORDER BY ord`, c)
			for rows.Next() {
				var sq *int64
				var ty, org string
				var av *int64
				_ = rows.Scan(&sq, &ty, &av, &org)
				t.Logf("   journal: seq=%v %s aggver=%v origin=%s", sq, ty, av, org)
			}
			rows.Close()
		}
	}
	_ = journal

	// 6. the administrator suspends the application: its token stops at once, and what it held goes back to the company
	if crmWon {
		if n := h.count(`SELECT count(*) FROM conversations WHERE id=$1 AND control_kind='crm_application'`, convs[0]); n != 1 {
			t.Fatalf("the CRM won convs[0] but does not hold it")
		}
	}
	if code, body := h.admin(admin, tn, http.MethodPost, "/"+crmApp.Installation.ID.String()+"/suspend", nil); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, body)
	}
	if _, err := crm.Conversations(ctx, "open"); !errors.As(err, &pr) || pr.Status != 401 {
		t.Errorf("the suspended application reading = %v, want 401", err)
	}
	if n := h.count(`SELECT count(*) FROM conversations WHERE tenant_id=$1 AND control_kind='crm_application'`, tn); n != 0 {
		t.Errorf("%d conversations are still held by a suspended application", n)
	}
}

type sync2 struct{ wg sync.WaitGroup }

func (s *sync2) Go(f func()) { s.wg.Add(1); go func() { defer s.wg.Done(); f() }() }
func (s *sync2) Wait()       { s.wg.Wait() }

func mustJSON(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
}

// wsAt calls an app route (not under /integrations/apps) as a signed-in person of the company.
func (e *env) wsAt(user, tenant uuid.UUID, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(http.MethodPost, path, rd)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// subscribeWith writes a subscription whose secret the test (the CRM) already knows.
func (h *hookEnv) subscribeWith(app created, tenant uuid.UUID, rawURL string, secret []byte) hook {
	h.t.Helper()
	_, keyID, _, err := delivery.NewSecret()
	if err != nil {
		h.t.Fatal(err)
	}
	sealed, err := h.cipher.Encrypt(secret)
	if err != nil {
		h.t.Fatal(err)
	}
	var from int64
	_ = h.seed.QueryRow(h.ctx, `SELECT COALESCE((SELECT last_seq FROM domain_event_cursors WHERE tenant_id=$1),0)`, tenant).Scan(&from)
	id := uuid.New()
	h.exec(`INSERT INTO webhook_subscriptions(id,tenant_id,installation_id,url,event_types,key_id,secret_ciphertext,enqueued_through_seq) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		id, tenant, app.Installation.ID, rawURL, []string{"conversation.created.v1", "conversation.control_changed.v1", "conversation.closed.v1", "message.created.v1", "message.status_changed.v1", "contact.updated.v1", "ticket.linked.v1"}, keyID, sealed, from)
	return hook{id: id, secret: secret, keyID: keyID}
}
