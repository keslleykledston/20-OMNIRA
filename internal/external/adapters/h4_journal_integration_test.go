package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnira/omnira/internal/conversationcontrol"
	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
)

// ADR-0046 H4: the durable journal. Real PostgreSQL as omnira_app.

var journalScopes = append(append([]string{}, readScopes...), "events.read")

func (e *env) eventsAPI() {
	mw := adapters.NewMiddleware(adapters.NewPostgresLookup(e.app))
	h := adapters.NewEventsHandler(e.app, 30)
	e.ext.Handle("GET /api/v1/external/sync/checkpoint", mw.Authenticated(adapters.RequireScope(domain.ScopeEventsRead, http.HandlerFunc(h.Checkpoint))))
	e.ext.Handle("GET /api/v1/external/events", mw.Authenticated(adapters.RequireScope(domain.ScopeEventsRead, http.HandlerFunc(h.ListEvents))))
}

// sequence numbers whatever can be numbered now (what the worker's sequencer does every half second) until nothing is left.
func (e *env) sequence() int {
	e.t.Helper()
	total := 0
	for {
		var n int
		if err := e.app.QueryRow(e.ctx, `SELECT domain_events_sequence(500)`).Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		total += n
		if n < 500 {
			return total
		}
	}
}

type journalEvent struct {
	Seq            int64      `json:"seq"`
	EventID        uuid.UUID  `json:"event_id"`
	Type           string     `json:"type"`
	Version        int        `json:"version"`
	ConversationID *uuid.UUID `json:"conversation_id"`
	AggregateVer   *int64     `json:"aggregate_version"`
	Aggregate      struct {
		Type string
		ID   uuid.UUID
	} `json:"aggregate"`
	Origin struct {
		Kind string `json:"kind"`
		Mine bool   `json:"mine"`
	} `json:"origin"`
	Data map[string]any `json:"data"`
}

type journalPage struct {
	Items      []journalEvent `json:"items"`
	NextCursor string         `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
	Head       int64          `json:"head"`
}

func (e *env) events(token string, after int64, extra string) (int, journalPage, []byte) {
	e.t.Helper()
	code, body, _ := e.callExt(http.MethodGet, fmt.Sprintf("/api/v1/external/events?after=%d%s", after, extra), token, nil)
	var p journalPage
	if code == http.StatusOK {
		if err := json.Unmarshal(body, &p); err != nil {
			e.t.Fatalf("%s: %v", body, err)
		}
	}
	return code, p, body
}

func (e *env) allEvents(token string, after int64) []journalEvent {
	e.t.Helper()
	var out []journalEvent
	for {
		code, p, body := e.events(token, after, "&limit=200")
		if code != http.StatusOK {
			e.t.Fatalf("events after %d = %d %s", after, code, body)
		}
		out = append(out, p.Items...)
		if !p.HasMore {
			return out
		}
		var next int64
		fmt.Sscan(p.NextCursor, &next)
		after = next
	}
}

func types(evs []journalEvent) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

// A fact is written in the transaction of the change; a rolled-back change leaves none; a company without an installation, a conversation with staff, an internal or
// spam contact leave none; the envelope names no content.
func TestJournalFactsAreWrittenWithTheChangeAndCarryNoContent(t *testing.T) {
	e := newEnv(t)
	e.eventsAPI()
	a, c := e.tenant(), e.tenant()
	admin := e.member(a, "tenant_admin")
	app := e.install(admin, a, "CRM", journalScopes...)
	staff := e.member(a, "tenant_agent")
	secretText := "senha-123 do cliente"

	conv, contact := e.conv(a, convOpts{})
	e.message(a, conv, "inbound", secretText, "received", time.Minute, nil)
	out := e.message(a, conv, "outbound", "resposta", "queued", 30*time.Second, &staff)
	e.exec(`UPDATE messages SET status='sent' WHERE id=$1`, out)
	e.exec(`UPDATE messages SET status='delivered' WHERE id=$1`, out)
	e.exec(`UPDATE contacts SET display_name='Nome Novo' WHERE id=$1`, contact)
	if _, _, err := newClaim(e, a, conv, app.Installation.ID); err != nil {
		t.Fatal(err)
	}
	ticket := uuid.New()
	e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,subject,provider,external_ticket_id) VALUES($1,$2,$3,'assunto sigiloso','k3g_crm','ERP-1')`, ticket, a, conv)
	e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, conv)

	// nothing that must stay silent
	staffConv, _ := e.conv(a, convOpts{staff: &staff})
	e.message(a, staffConv, "inbound", "papo interno", "received", time.Minute, nil)
	internalConv, _ := e.conv(a, convOpts{contactKind: "internal"})
	e.message(a, internalConv, "inbound", "parceiro", "received", time.Minute, nil)
	spamConv, spamContact := e.conv(a, convOpts{contactKind: "spam"})
	e.message(a, spamConv, "inbound", "promoção", "received", time.Minute, nil)
	e.exec(`UPDATE contacts SET display_name='Spammer 2' WHERE id=$1`, spamContact)
	cConv, _ := e.conv(c, convOpts{}) // a company with no installation
	e.message(c, cConv, "inbound", "sem integração", "received", time.Minute, nil)

	if n := e.sequence(); n == 0 {
		t.Fatal("nothing was numbered")
	}
	// the hidden conversations leave nothing in the journal itself (the read filter is a second barrier, not the first)
	var hidden int
	if err := e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE conversation_id = ANY($1)`, []uuid.UUID{staffConv, internalConv, spamConv}).Scan(&hidden); err != nil || hidden != 0 {
		t.Fatalf("%d facts of conversations an application may never see were recorded (%v)", hidden, err)
	}
	var spamContactFacts int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE aggregate_type='contact' AND aggregate_id=$1`, spamContact).Scan(&spamContactFacts)
	if spamContactFacts != 0 {
		t.Fatalf("%d contact facts of a spam contact were recorded", spamContactFacts)
	}
	evs := e.allEvents(app.Credential.Token, 0)
	got := types(evs)
	want := []string{"conversation.created.v1", "message.created.v1", "message.created.v1", "message.status_changed.v1", "message.status_changed.v1", "contact.updated.v1",
		"conversation.control_changed.v1", "ticket.linked.v1", "conversation.closed.v1"}
	if len(got) != len(want) {
		t.Fatalf("facts = %v, want %v", got, want)
	}
	count := map[string]int{}
	for _, g := range got {
		count[g]++
	}
	for _, w := range want {
		count[w]--
	}
	for k, v := range count {
		if v != 0 {
			t.Errorf("fact type %s off by %d (got %v)", k, v, got)
		}
	}
	// dense, increasing sequence numbers; unique event ids
	seen := map[uuid.UUID]bool{}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Errorf("event %d has seq %d, want %d", i, ev.Seq, i+1)
		}
		if seen[ev.EventID] {
			t.Errorf("event id %s repeated", ev.EventID)
		}
		seen[ev.EventID] = true
	}
	// no content, no phone, no name in any envelope
	raw, _ := json.Marshal(evs)
	for _, forbidden := range []string{secretText, "resposta", "assunto sigiloso", "Nome Novo", "+5592", "phone_e164\":\"", "papo interno", "parceiro", "promoção", "sem integração"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the journal leaks %q: %s", forbidden, raw)
		}
	}
	// the claim is attributed to this installation (so the application recognises its own echo), the rest is not
	for _, ev := range evs {
		if ev.Type == "conversation.control_changed.v1" && (!ev.Origin.Mine || ev.Origin.Kind != "crm") {
			t.Errorf("the application's own claim is not marked as its own: %+v", ev.Origin)
		}
		if ev.Type == "message.created.v1" && ev.Origin.Mine {
			t.Errorf("a message of the contact is marked as the application's: %+v", ev)
		}
	}
	// a rolled-back transaction leaves no fact
	before := len(evs)
	tx, err := e.seed.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES(gen_random_uuid(),$1,$2,'inbound','text','x','received')`, a, internalConv); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(e.ctx)
	conv2, _ := e.conv(a, convOpts{})
	tx, _ = e.seed.Begin(e.ctx)
	if _, err := tx.Exec(e.ctx, `INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES(gen_random_uuid(),$1,$2,'inbound','text','x','received')`, a, conv2); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(e.ctx)
	e.sequence()
	if after := len(e.allEvents(app.Credential.Token, 0)); after != before+1 { // +1: conversation.created of conv2 (committed); the rolled-back message left nothing
		t.Errorf("facts %d -> %d: a rolled-back message left a fact, or the committed conversation did not", before, after)
	}
	// once the installation is revoked nothing more is recorded
	if code, _ := e.admin(admin, a, http.MethodPost, "/"+app.Installation.ID.String()+"/revoke", nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	var total0 int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1`, a).Scan(&total0)
	e.message(a, conv2, "inbound", "depois da revogação", "received", 0, nil)
	var total1 int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1`, a).Scan(&total1)
	if total1 != total0 {
		t.Errorf("a fact was recorded after the last installation was revoked")
	}
	var cEvents int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1`, c).Scan(&cEvents)
	if cEvents != 0 {
		t.Errorf("a company without an installation has %d facts", cEvents)
	}
}

func newClaim(e *env, tenant, conv, inst uuid.UUID) (conversationcontrol.State, bool, error) {
	var st conversationcontrol.State
	var changed bool
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, tenant, func(ctx context.Context) error {
		var err error
		st, changed, err = conversationcontrol.Service{}.ClaimByApplication(ctx, platformdb.QuerierFromContext(ctx, e.app), tenant, conv, inst, nil,
			conversationcontrol.Actor{Kind: conversationcontrol.ActorCRMApplication, ID: inst})
		return err
	})
	return st, changed, err
}

// The sequencer never numbers a fact below a transaction that is still open: no fact can appear under a number already read.
func TestSequencerNeverNumbersBelowAnOpenTransaction(t *testing.T) {
	e := newEnv(t)
	e.eventsAPI()
	a := e.tenant()
	app := e.install(e.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	conv, _ := e.conv(a, convOpts{})
	e.sequence() // the conversation.created fact
	base := e.allEvents(app.Credential.Token, 0)

	// T1 begins (and keeps its transaction id), then T2 begins later and commits first
	t1, err := e.seed.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = t1.Rollback(context.Background()) }) // a failing assertion must not leave the transaction open
	m1 := uuid.New()
	if _, err := t1.Exec(e.ctx, `INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'inbound','text','um','received')`, m1, a, conv); err != nil {
		t.Fatal(err)
	}
	m2 := e.message(a, conv, "inbound", "dois", "received", 0, nil) // committed while T1 is open
	if n := e.sequence(); n != 0 {
		t.Fatalf("numbered %d facts while an earlier transaction was open: the later one must wait", n)
	}
	if got := e.allEvents(app.Credential.Token, 0); len(got) != len(base) {
		t.Fatalf("a reader saw %d facts, want %d: an unnumbered fact must be invisible", len(got), len(base))
	}
	if err := t1.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.sequence(); n != 2 {
		t.Fatalf("after T1 committed numbered %d facts, want 2", n)
	}
	var s1, s2 int64
	_ = e.seed.QueryRow(e.ctx, `SELECT seq FROM domain_events WHERE aggregate_id=$1 AND event_type='message.created.v1'`, m1).Scan(&s1)
	_ = e.seed.QueryRow(e.ctx, `SELECT seq FROM domain_events WHERE aggregate_id=$1 AND event_type='message.created.v1'`, m2).Scan(&s2)
	if s1 == 0 || s2 == 0 || s1 >= s2 {
		t.Fatalf("the earlier transaction's fact must come first: seq %d vs %d", s1, s2)
	}

	// several sequencers at once, in small batches over many facts: each fact is numbered exactly once, with no gap and no number given twice
	for i := 0; i < 300; i++ {
		e.message(a, conv, "inbound", fmt.Sprintf("m%d", i), "received", 0, nil)
	}
	var wg sync.WaitGroup
	var errs atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for round := 0; round < 40; round++ {
				var n int
				if err := e.app.QueryRow(e.ctx, `SELECT domain_events_sequence(10)`).Scan(&n); err != nil {
					errs.Add(1)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	e.sequence()
	var maxSeq, cnt, distinct, waiting int64
	_ = e.seed.QueryRow(e.ctx, `SELECT COALESCE(max(seq),0), count(seq), count(DISTINCT seq), count(*) FILTER (WHERE seq IS NULL) FROM domain_events WHERE tenant_id=$1`, a).Scan(&maxSeq, &cnt, &distinct, &waiting)
	if errs.Load() != 0 || waiting != 0 || maxSeq != cnt || cnt != distinct || cnt < 300 {
		t.Fatalf("concurrent sequencers: errors=%d waiting=%d max=%d count=%d distinct=%d", errs.Load(), waiting, maxSeq, cnt, distinct)
	}
	var cursor int64
	_ = e.seed.QueryRow(e.ctx, `SELECT last_seq FROM domain_event_cursors WHERE tenant_id=$1`, a).Scan(&cursor)
	if cursor != maxSeq {
		t.Fatalf("the cursor says %d but the highest number given is %d", cursor, maxSeq)
	}
}

// Writers on several companies while the sequencer runs: at every sample the numbered facts of a company are dense (count == max), and in the end every fact has a
// number, in transaction order.
func TestJournalStaysDenseAndOrderedUnderConcurrentWriters(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	e.install(e.member(a, "tenant_admin"), a, "CRM A", journalScopes...)
	e.install(e.member(b, "tenant_admin"), b, "CRM B", journalScopes...)
	convs := map[uuid.UUID]uuid.UUID{}
	for _, tn := range []uuid.UUID{a, b} {
		c, _ := e.conv(tn, convOpts{})
		convs[tn] = c
	}
	e.sequence()
	stop := make(chan struct{})
	var seqWG, wg sync.WaitGroup
	var violations atomic.Int32
	seqWG.Add(2)
	go func() { // the sequencer loop
		defer seqWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = e.app.Exec(e.ctx, `SELECT domain_events_sequence(100)`)
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()
	go func() { // the reader: density at every sample
		defer seqWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				rows, err := e.seed.Query(e.ctx, `SELECT tenant_id, count(seq), COALESCE(max(seq),0) FROM domain_events WHERE seq IS NOT NULL GROUP BY tenant_id`)
				if err != nil {
					continue
				}
				for rows.Next() {
					var tn uuid.UUID
					var c, m int64
					_ = rows.Scan(&tn, &c, &m)
					if c != m {
						violations.Add(1)
					}
				}
				rows.Close()
			}
		}
	}()
	const writers, each = 8, 25
	for w := 0; w < writers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			tn := a
			if w%2 == 1 {
				tn = b
			}
			for i := 0; i < each; i++ {
				_, err := e.seed.Exec(e.ctx, `INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES(gen_random_uuid(),$1,$2,'inbound','text','x','received')`, tn, convs[tn])
				if err != nil {
					t.Errorf("insert: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE seq IS NULL`).Scan(&waiting)
		if waiting == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	seqWG.Wait()
	if violations.Load() != 0 {
		t.Fatalf("a reader saw a gap in the numbered facts %d times", violations.Load())
	}
	for _, tn := range []uuid.UUID{a, b} {
		var total, numbered, maxSeq int64
		_ = e.seed.QueryRow(e.ctx, `SELECT count(*), count(seq), COALESCE(max(seq),0) FROM domain_events WHERE tenant_id=$1`, tn).Scan(&total, &numbered, &maxSeq)
		if total != numbered || numbered != maxSeq || total < writers/2*each {
			t.Errorf("company %s: total=%d numbered=%d max=%d", tn, total, numbered, maxSeq)
		}
		var inversions int
		_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM (SELECT xid, lag(xid) OVER (ORDER BY seq) AS prev FROM domain_events WHERE tenant_id=$1) x WHERE prev IS NOT NULL AND xid < prev`, tn).Scan(&inversions)
		if inversions != 0 {
			t.Errorf("company %s: %d facts are numbered out of transaction order", tn, inversions)
		}
	}
}

// The producers take no lock of their own: a mix of the writers that hold the project's locks in different orders (control claims, round robin, contact
// reclassification, messages) with the journal on never deadlocks.
func TestProducersAddNoLockSoNothingDeadlocks(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	first := e.install(e.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	inst := first.Installation.ID
	agent := e.member(a, "tenant_agent")
	rr := e.queue(a)
	e.exec(`UPDATE queues SET mode='round_robin' WHERE id=$1`, rr)
	e.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,true,true,50)`, a, rr, agent)
	var convs, contacts []uuid.UUID
	for i := 0; i < 6; i++ {
		c, ct := e.conv(a, convOpts{queue: &rr})
		convs, contacts = append(convs, c), append(contacts, ct)
	}
	stop := make(chan struct{})
	var deadlocks, other atomic.Int32
	note := func(err error) {
		if err == nil {
			return
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "40P01" {
			deadlocks.Add(1)
			return
		}
		var open interface{ Error() string }
		_ = open
		if !errors.Is(err, conversationcontrol.ErrHeldByOther) && !errors.Is(err, conversationcontrol.ErrNotHolder) && !errors.Is(err, conversationcontrol.ErrStaleVersion) {
			var pe2 *pgconn.PgError
			if errors.As(err, &pe2) && (pe2.Code == "OM001" || pe2.Code == "23514") {
				return
			}
			other.Add(1)
			t.Logf("unexpected: %v", err)
		}
	}
	var wg sync.WaitGroup
	loop := func(fn func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					fn(i)
				}
			}
		}()
	}
	svc := conversationcontrol.Service{}
	rrRepo := routingadapters.NewPostgresAssignmentRepository(e.app, nil)
	loop(func(i int) { // claim / release by the application
		c := convs[i%len(convs)]
		note(platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
			q := platformdb.QuerierFromContext(ctx, e.app)
			st, _, err := svc.ClaimByApplication(ctx, q, a, c, inst, nil, conversationcontrol.Actor{Kind: conversationcontrol.ActorCRMApplication, ID: inst})
			if err != nil {
				return err
			}
			_, err = svc.ReleaseByApplication(ctx, q, a, c, inst, st.Version, conversationcontrol.Actor{Kind: conversationcontrol.ActorCRMApplication, ID: inst})
			return err
		}))
	})
	loop(func(i int) { // the legacy round robin
		c := convs[(i+3)%len(convs)]
		note(platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
			_, _, err := rrRepo.AssignRoundRobin(ctx, c, "round_robin")
			return err
		}))
		_, _ = e.seed.Exec(e.ctx, `UPDATE conversations SET assigned_to_user_id=NULL WHERE id=$1 AND control_kind='tenant_user'`, c)
	})
	loop(func(i int) { // contact reclassification (contact -> conversations)
		ct := contacts[i%len(contacts)]
		_, err := e.seed.Exec(e.ctx, `UPDATE contacts SET kind = CASE kind WHEN 'other' THEN 'unclassified' ELSE 'other' END, display_name = 'Cliente '||$2::int WHERE id=$1`, ct, i)
		note(err)
	})
	loop(func(i int) { // messages and receipts
		c := convs[i%len(convs)]
		id := uuid.New()
		_, err := e.seed.Exec(e.ctx, `INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'outbound','text','x','queued')`, id, a, c)
		note(err)
		_, err = e.seed.Exec(e.ctx, `UPDATE messages SET status='sent' WHERE id=$1`, id)
		note(err)
	})
	loop(func(i int) { _, _ = e.app.Exec(e.ctx, `SELECT domain_events_sequence(200)`) })
	time.Sleep(4 * time.Second)
	close(stop)
	wg.Wait()
	if deadlocks.Load() != 0 || other.Load() != 0 {
		t.Fatalf("deadlocks=%d unexpected errors=%d", deadlocks.Load(), other.Load())
	}
}

// The events API: bootstrap, replay, visibility at READ time, 410, validation, scopes, isolation.
func TestEventsAPICheckpointReplayVisibilityAndResync(t *testing.T) {
	e := newEnv(t)
	e.readAPI(nil)
	e.eventsAPI()
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a, "tenant_admin"), e.member(b, "tenant_admin")
	full := e.install(adminA, a, "full", journalScopes...)
	noEvents := e.install(adminA, a, "noevents", readScopes...)
	other := e.install(adminB, b, "other", journalScopes...)
	q1, q2 := e.queue(a), e.queue(a)
	code, body := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "q1-only", "scopes": journalScopes, "queue_ids": []string{q1.String()}})
	if code != http.StatusCreated {
		t.Fatalf("create limited = %d %s", code, body)
	}
	var limited created
	_ = json.Unmarshal(body, &limited)

	// checkpoint FIRST
	code, body, _ = e.callExt(http.MethodGet, "/api/v1/external/sync/checkpoint", full.Credential.Token, nil)
	var cp struct {
		SeqHead   int64 `json:"seq_head"`
		MinSeq    int64 `json:"min_seq"`
		Retention int   `json:"retention_days"`
	}
	_ = json.Unmarshal(body, &cp)
	if code != 200 || cp.SeqHead != 0 || cp.Retention != 30 {
		t.Fatalf("checkpoint = %d %s", code, body)
	}
	c1, ct1 := e.conv(a, convOpts{queue: &q1})
	c2, _ := e.conv(a, convOpts{queue: &q2})
	m1 := e.message(a, c1, "inbound", "q1", "received", 0, nil)
	e.message(a, c2, "inbound", "q2", "received", 0, nil)
	e.exec(`UPDATE contacts SET alias='Apelido' WHERE id=$1`, ct1)
	e.exec(`UPDATE messages SET status='read' WHERE id=$1`, m1)
	// the other company has its own facts (and its own numbering, which overlaps with this one's)
	cB, _ := e.conv(b, convOpts{})
	e.message(b, cB, "inbound", "da outra empresa", "received", 0, nil)
	e.sequence()

	evs := e.allEvents(full.Credential.Token, cp.SeqHead)
	if len(evs) != 6 {
		t.Fatalf("full installation sees %d facts: %v", len(evs), types(evs))
	}
	lim := e.allEvents(limited.Credential.Token, cp.SeqHead)
	for _, ev := range lim {
		if ev.ConversationID != nil && *ev.ConversationID == c2 {
			t.Errorf("a fact of another queue reached the limited installation: %+v", ev)
		}
	}
	if len(lim) != 4 { // conversation.created, message.created, contact.updated, message.status_changed for c1
		t.Errorf("limited installation sees %d facts: %v, want 4", len(lim), types(lim))
	}
	gotB := e.allEvents(other.Credential.Token, 0)
	if len(gotB) != 2 {
		t.Errorf("the other company's installation sees %d facts, want its own 2", len(gotB))
	}
	for _, ev := range gotB {
		if ev.ConversationID == nil || *ev.ConversationID != cB {
			t.Errorf("the other company's installation received a fact that is not its own: %+v", ev)
		}
	}
	for _, ev := range evs {
		if ev.ConversationID != nil && *ev.ConversationID == cB {
			t.Errorf("a fact of another company reached this company's installation: %+v", ev)
		}
	}
	// pagination: next_cursor advances, even across a window the installation may not see
	_, p, _ := e.events(limited.Credential.Token, cp.SeqHead, "&limit=2")
	if len(p.Items) > 2 || p.NextCursor == "" || (p.HasMore && p.NextCursor == fmt.Sprint(cp.SeqHead)) {
		t.Errorf("page = %+v", p)
	}
	var cur int64
	for rounds := 0; rounds < 20; rounds++ {
		_, p, _ = e.events(limited.Credential.Token, cur, "&limit=1")
		fmt.Sscan(p.NextCursor, &cur)
		if !p.HasMore {
			break
		}
	}
	if p.HasMore {
		t.Error("paging one raw fact at a time never reached the head")
	}
	// validation and scopes
	for _, q := range []string{"", "&limit=0", "&limit=201", "&limit=x"} {
		path := "/api/v1/external/events?after=0" + q
		if q == "" {
			path = "/api/v1/external/events"
		}
		if code, _, _ := e.callExt(http.MethodGet, path, full.Credential.Token, nil); code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", path, code)
		}
	}
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/events?after=-1", full.Credential.Token, nil); code != http.StatusBadRequest {
		t.Errorf("negative after = %d", code)
	}
	for _, path := range []string{"/api/v1/external/events?after=0", "/api/v1/external/sync/checkpoint"} {
		if code, _, _ := e.callExt(http.MethodGet, path, noEvents.Credential.Token, nil); code != http.StatusForbidden {
			t.Errorf("%s without events.read = %d, want 403", path, code)
		}
	}
	// 410: a number that was never issued (the head of ANOTHER company could be larger) and a number older than what is kept
	head := evs[len(evs)-1].Seq
	if code, _, body := e.events(full.Credential.Token, head+1000, ""); code != http.StatusGone || !strings.Contains(string(body), "resync_required") {
		t.Errorf("a cursor from the future = %d %s, want 410 resync_required", code, body)
	}
	if code, p, _ := e.events(full.Credential.Token, head, ""); code != 200 || len(p.Items) != 0 || p.HasMore {
		t.Errorf("at the head: %d %+v", code, p)
	}
	// retention: old facts leave, min_seq says exactly what is left, an older cursor must resynchronize
	// facts 1..3 are old; 5 is old too but 4 is recent (numbers follow the transaction id, not the clock): only the prefix 1..3 may go, never 5 and never a hole
	e.exec(`UPDATE domain_events SET occurred_at = now() - interval '40 days' WHERE tenant_id=$1 AND seq IN (1,2,3,5)`, a)
	var purged int64
	if err := e.app.QueryRow(e.ctx, `SELECT domain_events_purge(interval '30 days')`).Scan(&purged); err != nil || purged != 3 {
		t.Fatalf("purge removed %d (%v), want 3", purged, err)
	}
	var holes, kept int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1 AND seq IN (1,2,3)`, a).Scan(&holes)
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1 AND seq IN (4,5)`, a).Scan(&kept)
	if holes != 0 || kept != 2 {
		t.Fatalf("after the purge: %d of the prefix remain, %d of facts 4,5 remain (want 0 and 2): retention cuts a prefix of EXPIRED facts, never a recent one, never a hole", holes, kept)
	}
	if _, err := e.app.Exec(e.ctx, `SELECT domain_events_purge(interval '10 minutes')`); err == nil {
		t.Error("a retention shorter than an hour must be refused")
	}
	if code, _, _ := e.events(full.Credential.Token, 0, ""); code != http.StatusGone {
		t.Errorf("a cursor older than the kept facts = %d, want 410", code)
	}
	if code, p, _ := e.events(full.Credential.Token, 3, ""); code != 200 || len(p.Items) != int(head-3) || p.Items[0].Seq != 4 {
		t.Errorf("the first kept fact: %d %+v", code, p)
	}
	code, body, _ = e.callExt(http.MethodGet, "/api/v1/external/sync/checkpoint", full.Credential.Token, nil)
	_ = json.Unmarshal(body, &cp)
	if cp.MinSeq != 4 || cp.SeqHead != head {
		t.Errorf("checkpoint after the purge: %s", body)
	}
	// the tenant cannot be chosen
	if code, _, _ := e.callExt(http.MethodGet, "/api/v1/external/events?after=0&tenant_id="+b.String(), full.Credential.Token, nil); code != http.StatusBadRequest {
		t.Errorf("tenant_id on events = %d, want 400", code)
	}
}

// A client that bootstraps while the company keeps changing rebuilds exactly the final state: checkpoint, snapshot, then replay.
func TestBootstrapWhileTheDataMovesRebuildsTheFinalState(t *testing.T) {
	e := newEnv(t)
	e.readAPI(nil)
	e.eventsAPI()
	a := e.tenant()
	adminA := e.member(a, "tenant_admin")
	code, body := e.admin(adminA, a, http.MethodPost, "", map[string]any{"name": "CRM", "scopes": journalScopes, "rate_limit_per_min": 6000})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var app created
	_ = json.Unmarshal(body, &app)
	inst := app.Installation.ID
	var convs []uuid.UUID
	for i := 0; i < 30; i++ {
		c, _ := e.conv(a, convOpts{createdAgo: time.Duration(i+1) * time.Minute})
		convs = append(convs, c)
	}
	e.sequence()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // changes: claims, releases, closes, new conversations
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			c := convs[i%len(convs)]
			st, _, err := newClaim(e, a, c, inst)
			if err == nil && i%3 == 0 {
				_ = platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
					_, err := conversationcontrol.Service{}.ReleaseByApplication(ctx, platformdb.QuerierFromContext(ctx, e.app), a, c, inst, st.Version, conversationcontrol.Actor{Kind: conversationcontrol.ActorCRMApplication, ID: inst})
					return err
				})
			}
			if i%7 == 0 {
				nc, _ := e.conv(a, convOpts{})
				convs = append(convs[:len(convs):len(convs)], nc)
			}
			time.Sleep(3 * time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = e.app.Exec(e.ctx, `SELECT domain_events_sequence(200)`)
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// the client: checkpoint, then the snapshot (all pages, control versions), then the events after the checkpoint
	_, body, _ = e.callExt(http.MethodGet, "/api/v1/external/sync/checkpoint", app.Credential.Token, nil)
	var cp struct {
		SeqHead int64 `json:"seq_head"`
	}
	_ = json.Unmarshal(body, &cp)
	state := map[uuid.UUID]int64{} // conversation -> control version known to the client
	cursor := ""
	for {
		q := "?status=all&limit=10"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		_, l := e.list(app.Credential.Token, q)
		for _, it := range l.Items {
			state[it.ID] = it.Control.Version
		}
		time.Sleep(15 * time.Millisecond) // the data moves between pages
		if !l.HasMore {
			break
		}
		cursor = l.NextCursor
	}
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	e.sequence()
	for _, ev := range e.allEvents(app.Credential.Token, cp.SeqHead) {
		if ev.ConversationID == nil {
			continue
		}
		switch ev.Type {
		case "conversation.created.v1":
			if _, known := state[*ev.ConversationID]; !known {
				state[*ev.ConversationID] = 0
			}
		case "conversation.control_changed.v1":
			if ev.AggregateVer != nil && *ev.AggregateVer > state[*ev.ConversationID] {
				state[*ev.ConversationID] = *ev.AggregateVer
			}
		}
	}
	rows, err := e.seed.Query(e.ctx, `SELECT id, control_version FROM conversations WHERE tenant_id=$1`, a)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id uuid.UUID
		var v int64
		_ = rows.Scan(&id, &v)
		n++
		if got, ok := state[id]; !ok || got != v {
			t.Errorf("conversation %s: the client rebuilt version %d (known=%v), the database has %d", id, got, ok, v)
		}
	}
	if n < 30 {
		t.Fatalf("only %d conversations compared", n)
	}
}

// A change of holder made by a LEGACY writer (a person assigned in the Workspace, the router, a release) names only assigned_to_user_id; the guard moves the control
// version. The journal must still say so, with the origin, or an external application would never learn that a person took the conversation.
func TestJournalSaysWhenALegacyWriterChangesWhoHoldsTheConversation(t *testing.T) {
	e := newEnv(t)
	e.eventsAPI()
	a := e.tenant()
	app := e.install(e.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	agent := e.member(a, "tenant_agent")
	conv, _ := e.conv(a, convOpts{})
	e.sequence()
	cp := e.allEvents(app.Credential.Token, 0)
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2, assigned_at=now() WHERE id=$1`, conv, agent) // exactly what the Workspace's assign does
	e.sequence()
	evs := e.allEvents(app.Credential.Token, int64(len(cp)))
	if len(evs) != 1 || evs[0].Type != "conversation.control_changed.v1" || evs[0].AggregateVer == nil || *evs[0].AggregateVer != 1 || evs[0].Origin.Kind != "workspace" {
		t.Fatalf("a person taking the conversation produced %+v", evs)
	}
	if evs[0].Data["control"].(map[string]any)["kind"] != "tenant_user" {
		t.Errorf("data = %v", evs[0].Data)
	}
	e.exec(`UPDATE conversations SET assigned_to_user_id=NULL WHERE id=$1`, conv) // and the release
	e.sequence()
	evs = e.allEvents(app.Credential.Token, int64(len(cp)))
	if len(evs) != 2 || evs[1].AggregateVer == nil || *evs[1].AggregateVer != 2 || evs[1].Data["control"].(map[string]any)["state"] != "unassigned" {
		t.Fatalf("the release produced %+v", evs)
	}
	// a plain update that moves nothing says nothing
	e.exec(`UPDATE conversations SET updated_at=now() WHERE id=$1`, conv)
	e.sequence()
	if more := e.allEvents(app.Credential.Token, int64(len(cp))); len(more) != 2 {
		t.Errorf("an update that changed no control produced a fact: %d", len(more))
	}
}
