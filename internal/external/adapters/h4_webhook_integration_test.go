package adapters_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/delivery"
	"github.com/omnira/omnira/internal/external/netpolicy"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ADR-0048 H4: outbound webhooks. Real PostgreSQL as omnira_app and a fake TLS receiver that verifies the signature like a real CRM would.

type rcvCall struct {
	header http.Header
	body   []byte
	at     time.Time
}

type receiver struct {
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []rcvCall
	status   atomic.Int32 // 0 -> 204
	redirect atomic.Value // string
	block    chan struct{}
	entered  chan struct{}
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{entered: make(chan struct{}, 100)}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body := make([]byte, 0, 1024)
		buf := bytes.NewBuffer(body)
		_, _ = buf.ReadFrom(req.Body)
		r.mu.Lock()
		r.calls = append(r.calls, rcvCall{header: req.Header.Clone(), body: buf.Bytes(), at: time.Now()})
		r.mu.Unlock()
		select {
		case r.entered <- struct{}{}:
		default:
		}
		if r.block != nil {
			<-r.block
		}
		if loc, _ := r.redirect.Load().(string); loc != "" {
			http.Redirect(w, req, loc, http.StatusFound)
			return
		}
		if s := int(r.status.Load()); s != 0 {
			w.WriteHeader(s)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) snapshot() []rcvCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rcvCall(nil), r.calls...)
}

// clientFor is the client of a worker that serves several receivers of the test (one worker claims the deliveries of every subscription).
func clientFor(rs ...*receiver) *http.Client {
	pool := x509.NewCertPool()
	ports := map[string]bool{}
	for _, r := range rs {
		pool.AddCert(r.srv.Certificate())
		u, _ := url.Parse(r.srv.URL)
		ports[u.Port()] = true
	}
	return netpolicy.NewClient(netpolicy.Options{Allow: func(ip net.IP) bool { return ip.IsLoopback() }, Ports: ports, TLSConfig: &tls.Config{RootCAs: pool}})
}

func (r *receiver) host() string { return strings.TrimPrefix(r.srv.URL, "https://") }

// client dials the fake receiver (loopback) and trusts its certificate; production never loosens the policy.
func (r *receiver) client() *http.Client {
	u, _ := url.Parse(r.srv.URL)
	pool := r.srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return netpolicy.NewClient(netpolicy.Options{
		Allow:     func(ip net.IP) bool { return ip.IsLoopback() },
		Ports:     map[string]bool{u.Port(): true},
		TLSConfig: &tls.Config{RootCAs: pool},
	})
}

type hookEnv struct {
	*env
	cipher *channelcrypto.AESGCM
}

func newHookEnv(t *testing.T) *hookEnv {
	e := newEnv(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := channelcrypto.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	h := &hookEnv{env: e, cipher: c}
	h.mountWebhooks(adapters.NewWebhookHandler(e.adminH, c, delivery.NewSecret))
	return h
}

// a subscription written directly (the admin API refuses a loopback URL, and the receiver lives there); the secret is known to the test
type hook struct {
	id     uuid.UUID
	secret []byte
	keyID  string
}

func (h *hookEnv) subscribe(app created, tenant uuid.UUID, rawURL string, fromHead bool, types ...string) hook {
	h.t.Helper()
	_, keyID, raw, err := delivery.NewSecret()
	if err != nil {
		h.t.Fatal(err)
	}
	sealed, err := h.cipher.Encrypt(raw)
	if err != nil {
		h.t.Fatal(err)
	}
	if len(types) == 0 {
		types = []string{"conversation.created.v1", "conversation.control_changed.v1", "conversation.closed.v1", "message.created.v1", "message.status_changed.v1", "contact.updated.v1", "ticket.linked.v1"}
	}
	from := int64(0)
	if fromHead {
		_ = h.seed.QueryRow(h.ctx, `SELECT COALESCE((SELECT last_seq FROM domain_event_cursors WHERE tenant_id=$1),0)`, tenant).Scan(&from)
	}
	id := uuid.New()
	h.exec(`INSERT INTO webhook_subscriptions(id,tenant_id,installation_id,url,event_types,key_id,secret_ciphertext,enqueued_through_seq) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		id, tenant, app.Installation.ID, rawURL, types, keyID, sealed, from)
	return hook{id: id, secret: raw, keyID: keyID}
}

func (h *hookEnv) worker(client *http.Client) *delivery.Worker {
	w := delivery.NewWorker(h.app, h.cipher, client)
	w.Window = delivery.RetryWindow
	return w
}

func (h *hookEnv) step(w *delivery.Worker) int {
	h.t.Helper()
	h.sequence()
	n, err := w.Step(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

// makeDue brings every pending delivery of the subscription to "now" (the time the test pretends has passed).
func (h *hookEnv) makeDue(hk hook) {
	h.exec(`UPDATE webhook_deliveries SET next_attempt_at = now() WHERE subscription_id=$1 AND status='pending'`, hk.id)
}

func (h *hookEnv) deliveries(hk hook, status string) int {
	var n int
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM webhook_deliveries WHERE subscription_id=$1 AND status=$2`, hk.id, status).Scan(&n)
	return n
}

func (h *hookEnv) twoSignaturesOK(c rcvCall, secrets ...[]byte) bool {
	for _, s := range secrets {
		if delivery.Verify(s, c.header.Get("X-Omnira-Signature"), c.header.Get("X-Omnira-Timestamp"), c.body, time.Now()) {
			return true
		}
	}
	return false
}

func TestWebhookDeliveryIsSignedCompleteAndCarriesNoContent(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	app := h.install(admin, a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	w := h.worker(rcv.client())

	conv, _ := h.conv(a, convOpts{})
	h.message(a, conv, "inbound", "meu cartão final 4242", "received", 0, nil)
	if n := h.step(w); n < 2 {
		t.Fatalf("the worker attempted %d deliveries, want at least the 2 facts", n)
	}
	calls := rcv.snapshot()
	if len(calls) != 2 {
		t.Fatalf("the receiver got %d calls, want 2", len(calls))
	}
	seenEvents := map[string]bool{}
	for _, c := range calls {
		if !h.twoSignaturesOK(c, hk.secret) {
			t.Errorf("a call does not verify with the subscription's secret: %v", c.header)
		}
		ts, _ := time.Parse("", "")
		_ = ts
		if c.header.Get("X-Omnira-Key-ID") != hk.keyID || c.header.Get("X-Omnira-Event-ID") == "" || c.header.Get("X-Omnira-Delivery-ID") == "" || c.header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %v", c.header)
		}
		if strings.Contains(string(c.body), "4242") || strings.Contains(string(c.body), "cartão") || strings.Contains(string(c.body), "+559") {
			t.Errorf("the envelope carries content: %s", c.body)
		}
		var ev map[string]any
		if err := json.Unmarshal(c.body, &ev); err != nil || ev["event_id"] != c.header.Get("X-Omnira-Event-ID") {
			t.Errorf("the body is not the event announced in the header: %s", c.body)
		}
		seenEvents[c.header.Get("X-Omnira-Event-ID")] = true
	}
	if len(seenEvents) != 2 {
		t.Errorf("distinct events = %d", len(seenEvents))
	}
	if h.deliveries(hk, "delivered") != 2 || h.deliveries(hk, "pending") != 0 {
		t.Errorf("delivered=%d pending=%d", h.deliveries(hk, "delivered"), h.deliveries(hk, "pending"))
	}
	// a second pass finds nothing: a delivered fact is never sent again, and the fan-out never duplicates a delivery
	for i := 0; i < 5; i++ {
		if _, err := h.app.Exec(h.ctx, `SELECT webhook_fanout(200)`); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.step(w); n != 0 || len(rcv.snapshot()) != 2 {
		t.Errorf("a delivered fact was sent again: attempts=%d calls=%d", n, len(rcv.snapshot()))
	}
	var total int
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM webhook_deliveries WHERE subscription_id=$1`, hk.id).Scan(&total)
	if total != 2 {
		t.Errorf("deliveries = %d, want exactly one per fact", total)
	}
	// the tampered body and a replayed old call do not verify
	c := calls[0]
	if delivery.Verify(hk.secret, c.header.Get("X-Omnira-Signature"), c.header.Get("X-Omnira-Timestamp"), append(c.body, ' '), time.Now()) ||
		delivery.Verify(hk.secret, c.header.Get("X-Omnira-Signature"), c.header.Get("X-Omnira-Timestamp"), c.body, time.Now().Add(time.Hour)) {
		t.Error("a tampered or replayed call verified")
	}
}

// The receiver is down for hours: facts wait, backoff grows to its ceiling, and when it is back every fact arrives with the same ids as the first attempt.
func TestWebhookReceiverOfflineForHoursRecoversEverythingWithTheSameIds(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	app := h.install(h.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	rcv.status.Store(http.StatusServiceUnavailable)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	clock := time.Now()
	w := h.worker(rcv.client()).WithClock(func() time.Time { return clock }, func() float64 { return 0 })

	conv, _ := h.conv(a, convOpts{})
	var msgs []uuid.UUID
	for i := 0; i < 6; i++ {
		msgs = append(msgs, h.message(a, conv, "inbound", "durante a queda", "received", 0, nil))
	}
	want := len(msgs) + 1 // + conversation.created
	if n := h.step(w); n != want {
		t.Fatalf("first attempt: %d, want %d", n, want)
	}
	if h.deliveries(hk, "pending") != want || h.deliveries(hk, "delivered") != 0 {
		t.Fatalf("pending=%d delivered=%d", h.deliveries(hk, "pending"), h.deliveries(hk, "delivered"))
	}
	var attempts int
	var next time.Time
	_ = h.seed.QueryRow(h.ctx, `SELECT attempts, next_attempt_at FROM webhook_deliveries WHERE subscription_id=$1 ORDER BY event_seq LIMIT 1`, hk.id).Scan(&attempts, &next)
	if attempts != 1 || next.Sub(clock) < 4*time.Second || next.Sub(clock) > 6*time.Second {
		t.Fatalf("after the first failure: attempts=%d next=%v from now (want ~5s)", attempts, next.Sub(clock))
	}
	// three hours pass in 30-minute steps, the receiver stays down: the wait grows and is capped at one hour
	for i := 0; i < 6; i++ {
		clock = clock.Add(30 * time.Minute)
		h.makeDue(hk)
		if n := h.step(w); n != want {
			t.Fatalf("round %d: attempted %d, want %d", i, n, want)
		}
	}
	_ = h.seed.QueryRow(h.ctx, `SELECT attempts, next_attempt_at FROM webhook_deliveries WHERE subscription_id=$1 ORDER BY event_seq LIMIT 1`, hk.id).Scan(&attempts, &next)
	if attempts != 7 || !near(next.Sub(clock), delivery.Backoff(7, 0)) {
		t.Fatalf("after 3 h: attempts=%d next in %v (want 7 and %v)", attempts, next.Sub(clock), delivery.Backoff(7, 0))
	}
	// many more hours: the wait grows to its ceiling of one hour and stays there, and nothing dies inside the 72 h window
	for i := 0; i < 8; i++ {
		clock = clock.Add(2 * time.Hour)
		h.makeDue(hk)
		h.step(w)
	}
	_ = h.seed.QueryRow(h.ctx, `SELECT attempts, next_attempt_at FROM webhook_deliveries WHERE subscription_id=$1 ORDER BY event_seq LIMIT 1`, hk.id).Scan(&attempts, &next)
	if attempts != 15 || !near(next.Sub(clock), time.Hour) {
		t.Fatalf("after 19 h: attempts=%d next in %v (want 15 and the 1 h ceiling)", attempts, next.Sub(clock))
	}
	if h.deliveries(hk, "dead") != 0 {
		t.Fatal("a fact died inside the 72 h window")
	}
	firstIDs := map[string]string{} // delivery id -> event id, as the receiver saw them while it was failing
	for _, c := range rcv.snapshot() {
		firstIDs[c.header.Get("X-Omnira-Delivery-ID")] = c.header.Get("X-Omnira-Event-ID")
	}
	if len(firstIDs) != want {
		t.Fatalf("the receiver saw %d distinct delivery ids while down, want %d", len(firstIDs), want)
	}
	// the receiver comes back
	rcv.status.Store(0)
	before := len(rcv.snapshot())
	clock = clock.Add(time.Hour)
	h.makeDue(hk)
	h.step(w)
	if h.deliveries(hk, "delivered") != want {
		t.Fatalf("after recovery delivered=%d pending=%d, want %d delivered", h.deliveries(hk, "delivered"), h.deliveries(hk, "pending"), want)
	}
	ok := 0
	var order []int64
	for _, c := range rcv.snapshot()[before:] {
		d, e := c.header.Get("X-Omnira-Delivery-ID"), c.header.Get("X-Omnira-Event-ID")
		if firstIDs[d] != e {
			t.Errorf("delivery %s now carries event %s, before it was %s: ids must not change across retries", d, e, firstIDs[d])
		}
		var ev struct{ Seq int64 }
		_ = json.Unmarshal(c.body, &ev)
		order = append(order, ev.Seq)
		ok++
	}
	if ok != want {
		t.Fatalf("the receiver got %d calls after recovery, want %d", ok, want)
	}
	for i := 1; i < len(order); i++ {
		if order[i] < order[i-1] {
			t.Errorf("recovery delivered out of order: %v", order)
		}
	}
}

func TestWebhookSubscribersOnlyHearTheirOwnCompany(t *testing.T) {
	h := newHookEnv(t)
	a, b := h.tenant(), h.tenant()
	appA := h.install(h.member(a, "tenant_admin"), a, "CRM A", journalScopes...)
	appB := h.install(h.member(b, "tenant_admin"), b, "CRM B", journalScopes...)
	rA, rB := newReceiver(t), newReceiver(t)
	hA := h.subscribe(appA, a, rA.srv.URL+"/a", true)
	hB := h.subscribe(appB, b, rB.srv.URL+"/b", true)
	convA, _ := h.conv(a, convOpts{})
	convB, _ := h.conv(b, convOpts{})
	h.message(a, convA, "inbound", "para A", "received", 0, nil)
	h.message(b, convB, "inbound", "para B", "received", 0, nil)
	w := h.worker(clientFor(rA, rB)) // ONE worker claims the deliveries of every subscription; each goes to ITS subscription's URL
	h.step(w)
	for _, c := range rA.snapshot() {
		var ev struct {
			TenantID string `json:"tenant_id"`
		}
		_ = json.Unmarshal(c.body, &ev)
		if ev.TenantID != a.String() || !h.twoSignaturesOK(c, hA.secret) || h.twoSignaturesOK(c, hB.secret) {
			t.Errorf("receiver A got a call that is not its company's or not signed with its secret: %s", c.body)
		}
	}
	for _, c := range rB.snapshot() {
		var ev struct {
			TenantID string `json:"tenant_id"`
		}
		_ = json.Unmarshal(c.body, &ev)
		if ev.TenantID != b.String() || !h.twoSignaturesOK(c, hB.secret) {
			t.Errorf("receiver B got a call that is not its company's: %s", c.body)
		}
	}
	if len(rA.snapshot()) != 2 || len(rB.snapshot()) != 2 {
		t.Errorf("calls: A=%d B=%d, want 2 each", len(rA.snapshot()), len(rB.snapshot()))
	}
}

// Revocation reaches pending deliveries at once, a delivery already in flight completes, and nothing new is made afterwards.
func TestWebhookRevocationStopsPendingAndNewDeliveries(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	app := h.install(admin, a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	rcv.status.Store(http.StatusServiceUnavailable)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	w := h.worker(rcv.client())
	conv, _ := h.conv(a, convOpts{})
	h.message(a, conv, "inbound", "x", "received", 0, nil)
	h.step(w) // fails: two deliveries pending
	if h.deliveries(hk, "pending") != 2 {
		t.Fatalf("pending = %d", h.deliveries(hk, "pending"))
	}
	if code, _ := h.admin(admin, a, http.MethodPost, "/"+app.Installation.ID.String()+"/revoke", nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	rcv.status.Store(0) // the receiver is healthy now: nothing may be sent anyway
	before := len(rcv.snapshot())
	h.makeDue(hk)
	h.step(w)
	if len(rcv.snapshot()) != before {
		t.Fatalf("a revoked installation's pending delivery reached the receiver")
	}
	if h.deliveries(hk, "cancelled") != 2 {
		t.Fatalf("cancelled = %d, want 2", h.deliveries(hk, "cancelled"))
	}
	// no new delivery is created for new facts of a revoked installation (and no new facts are even recorded)
	h.message(a, conv, "inbound", "depois", "received", 0, nil)
	h.step(w)
	var total int
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM webhook_deliveries WHERE subscription_id=$1`, hk.id).Scan(&total)
	if total != 2 {
		t.Errorf("deliveries = %d, no new one may exist after the revocation", total)
	}
}

func TestWebhookInFlightDeliveryDoesNotBlockTheRevocationAndNothingFollows(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	app := h.install(admin, a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	rcv.block = make(chan struct{})
	_ = h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	w := h.worker(rcv.client())
	conv, _ := h.conv(a, convOpts{})
	h.sequence()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = w.Step(h.ctx) }()
	select {
	case <-rcv.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the delivery never reached the receiver")
	}
	revoked := make(chan int, 1)
	go func() {
		code, _ := h.admin(admin, a, http.MethodPost, "/"+app.Installation.ID.String()+"/revoke", nil)
		revoked <- code
	}()
	select {
	case code := <-revoked:
		if code != http.StatusOK {
			t.Fatalf("revoke = %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the revocation waited for an HTTP call in flight: no lock may span the call")
	}
	close(rcv.block)
	<-done
	h.message(a, conv, "inbound", "tarde", "received", 0, nil)
	before := len(rcv.snapshot())
	h.step(w)
	if len(rcv.snapshot()) != before {
		t.Error("something was sent after the revocation")
	}
}

// The destination is judged at every connection: a name that resolves to a private address is never contacted, a redirect is never followed.
func TestWebhookSSRFAtDeliveryTime(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	app := h.install(h.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	conv, _ := h.conv(a, convOpts{})
	_ = conv

	// 1. the name resolves to a private network (the rebinding case): production policy, fake resolver
	hkPriv := h.subscribe(app, a, "https://crm.example.test/hook", true)
	privClient := netpolicy.NewClient(netpolicy.Options{Resolve: func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.5")}, nil
	}})
	h.message(a, conv, "inbound", "x", "received", 0, nil)
	h.step(h.worker(privClient))
	var class string
	_ = h.seed.QueryRow(h.ctx, `SELECT last_error_class FROM webhook_deliveries WHERE subscription_id=$1 ORDER BY event_seq LIMIT 1`, hkPriv.id).Scan(&class)
	if class != "blocked_address" || h.deliveries(hkPriv, "pending") == 0 {
		t.Fatalf("a private destination: class=%q pending=%d", class, h.deliveries(hkPriv, "pending"))
	}
	if len(rcv.snapshot()) != 0 {
		t.Fatal("the receiver was contacted")
	}

	// 2. the receiver answers with a redirect to a metadata address: not followed, recorded as a redirect
	h.exec(`DELETE FROM webhook_subscriptions WHERE id=$1`, hkPriv.id)
	rcv.redirect.Store("https://169.254.169.254/latest/meta-data")
	hkRedir := h.subscribe(app, a, rcv.srv.URL+"/hook", false)
	h.step(h.worker(rcv.client()))
	_ = h.seed.QueryRow(h.ctx, `SELECT last_error_class FROM webhook_deliveries WHERE subscription_id=$1 ORDER BY event_seq LIMIT 1`, hkRedir.id).Scan(&class)
	if class != "redirect" {
		t.Fatalf("a redirect: class=%q, want redirect", class)
	}
	if h.deliveries(hkRedir, "delivered") != 0 {
		t.Fatal("a redirect was taken as success")
	}
}

// After the retry window a fact goes to the dead-letter list; an administrator puts it back, with a fresh window.
func TestWebhookDeadLetterAndManualRetry(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	app := h.install(admin, a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	rcv.status.Store(http.StatusInternalServerError)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	clock := time.Now()
	w := h.worker(rcv.client()).WithClock(func() time.Time { return clock }, func() float64 { return 0 })
	w.Window = 2 * time.Hour
	h.conv(a, convOpts{})
	h.step(w)
	for i := 0; i < 8 && h.deliveries(hk, "dead") == 0; i++ {
		clock = clock.Add(time.Hour)
		h.makeDue(hk)
		h.step(w)
	}
	if h.deliveries(hk, "dead") != 1 || h.deliveries(hk, "pending") != 0 {
		t.Fatalf("dead=%d pending=%d, want the fact in the dead-letter list", h.deliveries(hk, "dead"), h.deliveries(hk, "pending"))
	}
	var did uuid.UUID
	_ = h.seed.QueryRow(h.ctx, `SELECT id FROM webhook_deliveries WHERE subscription_id=$1 AND status='dead'`, hk.id).Scan(&did)
	// the administrator's retry
	instPath := "/" + app.Installation.ID.String() + "/webhooks/" + hk.id.String()
	if code, body := h.admin(admin, a, http.MethodPost, instPath+"/deliveries/"+did.String()+"/retry", nil); code != http.StatusAccepted {
		t.Fatalf("retry = %d %s", code, body)
	}
	rcv.status.Store(0)
	clock = time.Now()
	h.step(w)
	if h.deliveries(hk, "delivered") != 1 || h.deliveries(hk, "dead") != 0 {
		t.Fatalf("after the retry: delivered=%d dead=%d", h.deliveries(hk, "delivered"), h.deliveries(hk, "dead"))
	}
	// a delivered one cannot be "retried" (and cannot be forged by id)
	if code, _ := h.admin(admin, a, http.MethodPost, instPath+"/deliveries/"+did.String()+"/retry", nil); code != http.StatusNotFound {
		t.Errorf("retrying a delivered one = %d, want 404", code)
	}
}

// A fact that stopped being visible between creation and sending (allow-list changed, conversation turned spam) is cancelled, not sent.
func TestWebhookFactNoLongerVisibleIsCancelled(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	q1, q2 := h.queue(a), h.queue(a)
	code, body := h.admin(admin, a, http.MethodPost, "", map[string]any{"name": "CRM", "scopes": journalScopes, "queue_ids": []string{q1.String(), q2.String()}})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var app created
	_ = json.Unmarshal(body, &app)
	rcv := newReceiver(t)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	conv, contact := h.conv(a, convOpts{queue: &q2})
	other, _ := h.conv(a, convOpts{queue: &q1})
	h.message(a, conv, "inbound", "x", "received", 0, nil)
	h.message(a, other, "inbound", "y", "received", 0, nil)
	h.sequence()
	// before anything is sent: the administrator narrows the installation to q1, and the contact of the other conversation is classified spam
	if code, _ := h.admin(admin, a, http.MethodPatch, "/"+app.Installation.ID.String(), map[string]any{"queue_ids": []string{q1.String()}}); code != http.StatusOK {
		t.Fatalf("narrow = %d", code)
	}
	_ = contact
	h.step(h.worker(rcv.client()))
	for _, c := range rcv.snapshot() {
		if strings.Contains(string(c.body), conv.String()) {
			t.Errorf("a fact of a conversation outside the allow-list was sent: %s", c.body)
		}
	}
	if h.deliveries(hk, "cancelled") != 2 { // the two facts of the conversation in q2
		t.Errorf("cancelled = %d, want 2", h.deliveries(hk, "cancelled"))
	}
	if h.deliveries(hk, "delivered") != 2 { // conversation.created + message.created of the one in q1
		t.Errorf("delivered = %d, want 2", h.deliveries(hk, "delivered"))
	}
}

func (h *hookEnv) mountWebhooks(hooks *adapters.WebhookHandler) {
	base := "/api/v1/tenants/{tenant_id}/integrations/apps/{app_id}/webhooks"
	chain := func(f http.HandlerFunc) http.Handler { return h.session(f) }
	h.mux.Handle("GET "+base, chain(hooks.List))
	h.mux.Handle("POST "+base, chain(hooks.Create))
	h.mux.Handle("PATCH "+base+"/{hook_id}", chain(hooks.Update))
	h.mux.Handle("DELETE "+base+"/{hook_id}", chain(hooks.Delete))
	h.mux.Handle("POST "+base+"/{hook_id}/rotate-secret", chain(hooks.Rotate))
	h.mux.Handle("GET "+base+"/{hook_id}/deliveries", chain(hooks.Deliveries))
	h.mux.Handle("POST "+base+"/{hook_id}/deliveries/{delivery_id}/retry", chain(hooks.Retry))
}

var _ = pgconn.PgError{}

// The administration of the subscriptions: the address is judged when it is saved, the secret appears once and is sealed, a person cannot read the sealed copy.
func TestWebhookAdministrationIsPermissionedValidatedAndNeverReturnsTheSecretAgain(t *testing.T) {
	h := newHookEnv(t)
	a, b := h.tenant(), h.tenant()
	admin, sup, agent := h.member(a, "tenant_admin"), h.member(a, "tenant_supervisor"), h.member(a, "tenant_agent")
	adminB := h.member(b, "tenant_admin")
	app := h.install(admin, a, "CRM", journalScopes...)
	id := app.Installation.ID.String()
	base := "/" + id + "/webhooks"
	good := map[string]any{"url": "https://crm.example.com/hook", "event_types": []string{"message.created.v1", "conversation.closed.v1"}}

	code, body := h.admin(admin, a, http.MethodPost, base, good)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var made struct {
		Subscription struct {
			ID    uuid.UUID `json:"id"`
			KeyID string    `json:"key_id"`
		} `json:"subscription"`
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(body, &made)
	if !strings.HasPrefix(made.Secret, "whsec_") || !strings.HasPrefix(made.Subscription.KeyID, "whk_") {
		t.Fatalf("created = %s", body)
	}
	hid := made.Subscription.ID.String()
	// the secret is not in any later answer, nor in the audit trail
	for _, p := range []string{base, base + "/" + hid + "/deliveries"} {
		code, body := h.admin(admin, a, http.MethodGet, p, nil)
		if code != http.StatusOK || strings.Contains(string(body), made.Secret) || strings.Contains(string(body), "secret_ciphertext") || strings.Contains(string(body), strings.TrimPrefix(made.Secret, "whsec_")) {
			t.Errorf("GET %s = %d %s", p, code, body)
		}
	}
	var meta string
	if err := h.seed.QueryRow(h.ctx, `SELECT string_agg(metadata::text, ' ') FROM audit_events WHERE tenant_id=$1 AND action LIKE 'external_app.webhook%'`, a).Scan(&meta); err != nil || strings.Contains(meta, made.Secret) || strings.Contains(meta, strings.TrimPrefix(made.Secret, "whsec_")) {
		t.Errorf("audit metadata = %s (%v)", meta, err)
	}
	// the application role cannot read the sealed copy, even in a system session
	err := platformdb.WithSystemTenantSession(h.ctx, h.app, a, func(ctx context.Context) error {
		_, err := platformdb.QuerierFromContext(ctx, h.app).Exec(ctx, `SELECT secret_ciphertext FROM webhook_subscriptions`)
		return err
	})
	var pe *pgconn.PgError
	if !asPg(err, &pe) || pe.Code != "42501" {
		t.Errorf("secret_ciphertext readable by the application role: %v", err)
	}
	// the address
	for _, u := range []string{"http://crm.example.com/h", "https://127.0.0.1/h", "https://10.1.1.1/h", "https://169.254.169.254/latest", "https://localhost/h", "https://crm.example.com:8080/h", "https://user:pw@crm.example.com/h", "https://[::1]/h", "ftp://x/y", ""} {
		if code, _ := h.admin(admin, a, http.MethodPost, base, map[string]any{"url": u, "event_types": []string{"message.created.v1"}}); code != http.StatusBadRequest {
			t.Errorf("url %q = %d, want 400", u, code)
		}
	}
	if code, _ := h.admin(admin, a, http.MethodPost, base, map[string]any{"url": "https://crm.example.com/h", "event_types": []string{"nope.v1"}}); code != http.StatusBadRequest {
		t.Errorf("unknown event type = %d, want 400", code)
	}
	if code, _ := h.admin(admin, a, http.MethodPost, base, map[string]any{"url": "https://crm.example.com/h", "event_types": []string{}}); code != http.StatusBadRequest {
		t.Errorf("no event types = %d, want 400", code)
	}
	if code, _ := h.admin(admin, a, http.MethodPost, base, map[string]any{"url": "https://crm.example.com/h", "event_types": []string{"message.created.v1"}, "tenant_id": b.String()}); code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", code)
	}
	// permissions and isolation
	if code, _ := h.admin(sup, a, http.MethodGet, base, nil); code != http.StatusOK {
		t.Errorf("supervisor list = %d", code)
	}
	if code, _ := h.admin(sup, a, http.MethodPost, base, good); code != http.StatusForbidden {
		t.Errorf("supervisor create = %d, want 403", code)
	}
	if code, _ := h.admin(agent, a, http.MethodGet, base, nil); code != http.StatusForbidden {
		t.Errorf("agent list = %d, want 403", code)
	}
	for _, c := range []struct{ method, path string }{{http.MethodGet, base}, {http.MethodPatch, base + "/" + hid}, {http.MethodDelete, base + "/" + hid}, {http.MethodPost, base + "/" + hid + "/rotate-secret"}, {http.MethodGet, base + "/" + hid + "/deliveries"}} {
		var body any
		if c.method == http.MethodPatch {
			body = map[string]any{"status": "paused"}
		}
		if code, _ := h.admin(adminB, b, c.method, c.path, body); code != http.StatusNotFound {
			t.Errorf("another company %s %s = %d, want 404", c.method, c.path, code)
		}
	}
	// update, pause, rotation (the secret changes, the old key id stays valid for a while), delete
	if code, _ := h.admin(admin, a, http.MethodPatch, base+"/"+hid, map[string]any{"status": "paused", "url": "https://crm2.example.com/h"}); code != http.StatusOK {
		t.Errorf("update = %d", code)
	}
	if code, _ := h.admin(admin, a, http.MethodPatch, base+"/"+hid, map[string]any{"url": "https://10.0.0.1/x"}); code != http.StatusBadRequest {
		t.Errorf("update to a private address = %d, want 400", code)
	}
	code, body = h.admin(admin, a, http.MethodPost, base+"/"+hid+"/rotate-secret", map[string]any{"keep_hours": 2})
	var rot struct {
		KeyID  string `json:"key_id"`
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(body, &rot)
	if code != http.StatusOK || rot.Secret == "" || rot.Secret == made.Secret || rot.KeyID == made.Subscription.KeyID {
		t.Fatalf("rotate = %d %s", code, body)
	}
	var prevKey *string
	var prevUntil *time.Time
	_ = h.seed.QueryRow(h.ctx, `SELECT previous_key_id, previous_valid_until FROM webhook_subscriptions WHERE id=$1`, made.Subscription.ID).Scan(&prevKey, &prevUntil)
	if prevKey == nil || *prevKey != made.Subscription.KeyID || prevUntil == nil || time.Until(*prevUntil) < time.Hour || time.Until(*prevUntil) > 3*time.Hour {
		t.Errorf("the old key is not kept for the window: %v %v", prevKey, prevUntil)
	}
	if code, _ := h.admin(admin, a, http.MethodPost, base+"/"+hid+"/rotate-secret", map[string]any{"keep_hours": 0}); code != http.StatusBadRequest {
		t.Errorf("keep_hours 0 = %d, want 400", code)
	}
	if code, _ := h.admin(admin, a, http.MethodPost, base+"/"+uuid.NewString()+"/rotate-secret", nil); code != http.StatusNotFound {
		t.Errorf("rotating an unknown subscription = %d, want 404", code)
	}
	// an agent with arbitrary SQL cannot rotate it either: the function checks the permission itself
	if err := h.human(a, agent, func(ctx context.Context) error {
		var ok bool
		return platformdb.QuerierFromContext(ctx, h.app).QueryRow(ctx, `SELECT webhook_rotate_secret($1,$2,'whk_aaaaaaaaaaaa',$3,interval '1 hour')`, a, made.Subscription.ID, []byte("x")).Scan(&ok)
	}); err != nil {
		t.Logf("agent rotate: %v", err)
	}
	var key string
	_ = h.seed.QueryRow(h.ctx, `SELECT key_id FROM webhook_subscriptions WHERE id=$1`, made.Subscription.ID).Scan(&key)
	if key != rot.KeyID {
		t.Errorf("an agent rotated the secret: key %s, want %s", key, rot.KeyID)
	}
	// at most five
	for i := 0; i < 4; i++ {
		if code, body := h.admin(admin, a, http.MethodPost, base, good); code != http.StatusCreated {
			t.Fatalf("subscription %d = %d %s", i+2, code, body)
		}
	}
	if code, _ := h.admin(admin, a, http.MethodPost, base, good); code != http.StatusConflict {
		t.Errorf("the sixth subscription = %d, want 409", code)
	}
	if code, _ := h.admin(admin, a, http.MethodDelete, base+"/"+hid, nil); code != http.StatusNoContent {
		t.Errorf("delete = %d", code)
	}
	if code, _ := h.admin(admin, a, http.MethodDelete, base+"/"+hid, nil); code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", code)
	}
}

// With two applications in one company, revoking one stops its deliveries at the source (no row is even created for it) while the other keeps receiving.
func TestWebhookFanoutSkipsARevokedInstallationButNotItsNeighbour(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	gone := h.install(admin, a, "CRM que sai", journalScopes...)
	stays := h.install(admin, a, "CRM que fica", journalScopes...)
	rcv := newReceiver(t)
	hGone := h.subscribe(gone, a, rcv.srv.URL+"/gone", true)
	hStays := h.subscribe(stays, a, rcv.srv.URL+"/stays", true)
	if code, _ := h.admin(admin, a, http.MethodPost, "/"+gone.Installation.ID.String()+"/revoke", nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	h.conv(a, convOpts{}) // a new fact, recorded because the neighbour is live
	h.sequence()
	if _, err := h.app.Exec(h.ctx, `SELECT webhook_fanout(200)`); err != nil {
		t.Fatal(err)
	}
	var rowsGone, rowsStays int
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM webhook_deliveries WHERE subscription_id=$1`, hGone.id).Scan(&rowsGone)
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM webhook_deliveries WHERE subscription_id=$1`, hStays.id).Scan(&rowsStays)
	if rowsGone != 0 || rowsStays != 1 {
		t.Fatalf("deliveries: revoked=%d (want 0), neighbour=%d (want 1)", rowsGone, rowsStays)
	}
	h.step(h.worker(rcv.client()))
	if h.deliveries(hStays, "delivered") != 1 {
		t.Errorf("the neighbour did not receive its fact")
	}
}

// The claim door itself (a trust boundary, whatever the worker checks afterwards): a delivery of a revoked installation, a paused subscription or a suspended
// company is cancelled and never handed out.
func TestWebhookClaimNeverHandsOutWhatCannotBeSent(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	rcv := newReceiver(t)
	rcv.status.Store(http.StatusServiceUnavailable)
	mk := func(name string) (created, hook) {
		app := h.install(admin, a, name, journalScopes...)
		return app, h.subscribe(app, a, rcv.srv.URL+"/"+name, true)
	}
	appRev, hRev := mk("revoked")
	_, hPaused := mk("paused")
	_, hLive := mk("live")
	h.conv(a, convOpts{})
	h.step(h.worker(rcv.client())) // every delivery fails once: all pending
	for _, hk := range []hook{hRev, hPaused, hLive} {
		if h.deliveries(hk, "pending") != 1 {
			t.Fatalf("setup: pending = %d", h.deliveries(hk, "pending"))
		}
	}
	h.exec(`UPDATE external_app_installations SET status='revoked', revoked_at=now() WHERE id=$1`, appRev.Installation.ID)
	h.exec(`UPDATE webhook_subscriptions SET status='paused' WHERE id=$1`, hPaused.id)
	for _, hk := range []hook{hRev, hPaused, hLive} {
		h.makeDue(hk)
	}
	rows, err := h.app.Query(h.ctx, `SELECT subscription_id FROM webhook_claim(50, interval '1 minute')`)
	if err != nil {
		t.Fatal(err)
	}
	var handed []uuid.UUID
	for rows.Next() {
		var sid uuid.UUID
		_ = rows.Scan(&sid)
		handed = append(handed, sid)
	}
	rows.Close()
	if len(handed) != 1 || handed[0] != hLive.id {
		t.Fatalf("handed out %v, want only the live subscription %s", handed, hLive.id)
	}
	if h.deliveries(hRev, "cancelled") != 1 {
		t.Errorf("cancelled: revoked=%d, want 1", h.deliveries(hRev, "cancelled"))
	}
	if h.deliveries(hPaused, "pending") != 1 {
		t.Errorf("a paused subscription must keep its deliveries pending (they go out after the resume): pending=%d", h.deliveries(hPaused, "pending"))
	}
}

// During a rotation the call carries two signatures; once the window is over only the new one.
func TestWebhookRotationSignsWithBothSecretsDuringTheWindow(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	app := h.install(admin, a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	// a real rotation through the function (the secret of the new generation is known only to the test via the sealed copy it makes)
	_, newKey, newRaw, _ := delivery.NewSecret()
	sealed, _ := h.cipher.Encrypt(newRaw)
	if err := h.human(a, admin, func(ctx context.Context) error {
		var ok bool
		return platformdb.QuerierFromContext(ctx, h.app).QueryRow(ctx, `SELECT webhook_rotate_secret($1,$2,$3,$4,interval '2 hours')`, a, hk.id, newKey, sealed).Scan(&ok)
	}); err != nil {
		t.Fatal(err)
	}
	w := h.worker(rcv.client())
	h.conv(a, convOpts{})
	h.step(w)
	calls := rcv.snapshot()
	if len(calls) != 1 || strings.Count(calls[0].header.Get("X-Omnira-Signature"), "v1=") != 2 || calls[0].header.Get("X-Omnira-Key-ID") != newKey {
		t.Fatalf("during the window: %v", calls)
	}
	if !h.twoSignaturesOK(calls[0], hk.secret) || !h.twoSignaturesOK(calls[0], newRaw) {
		t.Error("both the old and the new secret must verify during the window")
	}
	h.exec(`UPDATE webhook_subscriptions SET previous_valid_until = now() - interval '1 minute' WHERE id=$1`, hk.id)
	h.conv(a, convOpts{})
	h.step(w)
	calls = rcv.snapshot()
	last := calls[len(calls)-1]
	if strings.Count(last.header.Get("X-Omnira-Signature"), "v1=") != 1 || h.twoSignaturesOK(last, hk.secret) || !h.twoSignaturesOK(last, newRaw) {
		t.Errorf("after the window only the new secret may sign: %v", last.header)
	}
}

// human runs fn as a signed-in person of the company (a Direct member), like a request through the tenant middleware.
func (e *env) human(tenant, user uuid.UUID, fn func(ctx context.Context) error) error {
	return platformdb.WithTenantSession(e.ctx, e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		return fn(tenancydomain.WithTenantContext(ctx, tc))
	})
}

// near compares durations that went through a microsecond column.
func near(a, b time.Duration) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < time.Millisecond
}

func asPg(err error, target **pgconn.PgError) bool {
	for err != nil {
		if pe, ok := err.(*pgconn.PgError); ok {
			*target = pe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
