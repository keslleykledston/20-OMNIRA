package adapters_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/crmsim"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"net/http/httptest"
)

// Benchmark of the headless surface at the scale of the plan (30 000 conversations in one company). It is a measurement, not a unit test: it is skipped unless
// OMNIRA_BENCH_CONVERSATIONS is set, prints "BENCH key=value" lines for docs/delivery/HEADLESS-BENCHMARK.md and fails only on a regression far beyond any reasonable
// budget (so it also works as a smoke test of the indexes).

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[int(float64(len(d)-1)*p)]
}

func TestBenchmarkHeadlessAtScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("OMNIRA_BENCH_CONVERSATIONS"))
	if n <= 0 {
		t.Skip("set OMNIRA_BENCH_CONVERSATIONS (for example 30000) to run the benchmark")
	}
	e := newEnv(t)
	tn, _, app, _, conn := e.writeEnv() // the installation exists BEFORE the data: the journal producers are on, as in production
	line := func(k string, v any) { t.Logf("BENCH %s=%v", k, v) }
	var dead0 int64
	_ = e.seed.QueryRow(e.ctx, `SELECT deadlocks FROM pg_stat_database WHERE datname=current_database()`).Scan(&dead0)

	// 1. volume: n conversations, 3 messages each, with the producers writing facts in the same transactions
	t0 := time.Now()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) SELECT gen_random_uuid(), $1, 'Bench '||g, '+5592'||lpad(g::text, 9, '0') FROM generate_series(1,$2) g`, tn, n)
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status,created_at,updated_at)
	        SELECT gen_random_uuid(), $1, c.id, $3, 'open', now() - (random()*interval '30 days'), now() - (random()*interval '5 days') FROM contacts c WHERE c.tenant_id=$1 AND c.display_name LIKE 'Bench %' LIMIT $2`, tn, n, conn)
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status,created_at)
	        SELECT gen_random_uuid(), $1, c.id, CASE WHEN k=2 THEN 'outbound' ELSE 'inbound' END, 'text', 'mensagem '||k, CASE WHEN k=2 THEN 'sent' ELSE 'received' END, c.created_at + k*interval '1 minute'
	          FROM conversations c, generate_series(1,3) k WHERE c.tenant_id=$1`, tn)
	insert := time.Since(t0)
	// what autovacuum would have done in production before a client arrives: the planner must know the tables are not tiny
	e.exec(`ANALYZE contacts, conversations, messages, domain_events`)
	var events int64
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM domain_events WHERE tenant_id=$1`, tn).Scan(&events)
	line("conversations", n)
	line("messages", n*3)
	line("insert_seconds", insert.Seconds())
	line("facts_written_in_the_same_transactions", events)
	line("lock_free_producers_ok", events >= int64(n*4))

	// 2. the sequencer numbers the backlog; lag is how long it takes to drain what the load produced
	t0 = time.Now()
	total := 0
	for {
		var k int
		if err := e.app.QueryRow(e.ctx, `SELECT domain_events_sequence(2000)`).Scan(&k); err != nil {
			t.Fatal(err)
		}
		total += k
		if k == 0 {
			break
		}
	}
	e.exec(`ANALYZE domain_events`) // numbering updated every row: what autovacuum does before a client arrives
	line("sequencer_seconds", time.Since(t0).Seconds())
	line("sequencer_facts_per_second", float64(total)/time.Since(t0).Seconds())

	explain := func(label, sql string, args ...any) {
		// as the application really runs: the application role, a system session of the company (so RLS is evaluated, not bypassed)
		_ = platformdb.WithSystemTenantSession(e.ctx, e.app, tn, func(ctx context.Context) error {
			q := platformdb.QuerierFromContext(ctx, e.app)
			rows, err := q.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "+sql, args...)
			if err != nil {
				t.Logf("PLAN %s: %v", label, err)
				return nil
			}
			for rows.Next() {
				var l string
				_ = rows.Scan(&l)
				t.Logf("PLAN %s | %s", label, l)
			}
			rows.Close()
			return nil
		})
	}
	vis := `c.tenant_id = $1 AND c.internal_user_id IS NULL AND c.conversation_kind <> 'internal' AND EXISTS (SELECT 1 FROM contacts vc WHERE vc.tenant_id = c.tenant_id AND vc.id = c.contact_id AND vc.kind NOT IN ('internal','spam'))`
	explain("list", `SELECT c.id, (SELECT max(m.created_at) FROM messages m WHERE m.tenant_id = c.tenant_id AND m.conversation_id = c.id) FROM conversations c LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id WHERE `+vis+` AND c.status='open' ORDER BY c.created_at DESC, c.id DESC LIMIT 101`, tn)
	var anyID uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM conversations WHERE tenant_id=$1 LIMIT 1`, tn).Scan(&anyID)
	explain("get", `SELECT c.id, (SELECT max(m.created_at) FROM messages m WHERE m.tenant_id = c.tenant_id AND m.conversation_id = c.id) FROM conversations c LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id WHERE `+vis+` AND c.id=$2`, tn, anyID)
	explain("events", `WITH w AS (SELECT e.* FROM domain_events e WHERE e.tenant_id = $1 AND e.seq IS NOT NULL AND e.seq > $2 ORDER BY e.seq LIMIT 200)
	  SELECT w.seq, w.id, CASE WHEN w.conversation_id IS NOT NULL THEN EXISTS (SELECT 1 FROM conversations c WHERE c.id = w.conversation_id AND `+vis+`) ELSE false END AS visible FROM w ORDER BY w.seq`, tn, int64(32600))
	tList := time.Now()
	_, _, _ = e.callExt(http.MethodGet, "/api/v1/external/conversations?status=open&limit=100", app.Credential.Token, nil)
	line("first_list_call_ms_after_explain", float64(time.Since(tList).Microseconds())/1000)
	srv := httptest.NewServer(e.ext)
	t.Cleanup(srv.Close)
	cli := crmsim.NewClient(srv.URL+"/api/v1/external", app.Credential.Token, &http.Client{Timeout: 120 * time.Second})

	// 3. reads: the first page and a walk through the pages (the cursor must stay fast at depth), single conversations, messages
	var first, deep, one, msgs []time.Duration
	cursorPages := 0
	cursor := ""
	for cursorPages < 150 {
		t1 := time.Now()
		code, body, _ := e.callExt(http.MethodGet, "/api/v1/external/conversations?status=open&limit=100"+map[bool]string{true: "&cursor=" + cursor, false: ""}[cursor != ""], app.Credential.Token, nil)
		d := time.Since(t1)
		if code != 200 {
			t.Fatalf("list = %d %s", code, body)
		}
		if cursorPages == 0 {
			first = append(first, d)
		} else {
			deep = append(deep, d)
		}
		var p struct {
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
			Items      []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		mustJSON(t, body, &p)
		cursorPages++
		if len(p.Items) > 0 && cursorPages%10 == 0 {
			t2 := time.Now()
			if _, err := cli.Conversation(e.ctx, p.Items[0].ID); err != nil {
				t.Fatalf("get after %d pages: %v (took %v)", cursorPages, err, time.Since(t2))
			}
			one = append(one, time.Since(t2))
			t3 := time.Now()
			if _, err := cli.Messages(e.ctx, p.Items[0].ID); err != nil {
				t.Fatal(err)
			}
			msgs = append(msgs, time.Since(t3))
		}
		if !p.HasMore {
			break
		}
		cursor = p.NextCursor
	}
	line("list_first_page_ms", float64(pct(first, 0.5).Microseconds())/1000)
	line("list_page_p50_ms", float64(pct(deep, 0.5).Microseconds())/1000)
	line("list_page_p95_ms", float64(pct(deep, 0.95).Microseconds())/1000)
	line("get_conversation_p50_ms", float64(pct(one, 0.5).Microseconds())/1000)
	line("get_conversation_p95_ms", float64(pct(one, 0.95).Microseconds())/1000)
	line("messages_p95_ms", float64(pct(msgs, 0.95).Microseconds())/1000)

	// 4. the journal read in pages (what a CRM does to bootstrap or to recover from downtime)
	t0 = time.Now()
	var after int64
	pages, read := 0, 0
	for {
		tp := time.Now()
		evs, next, more, err := cli.Events(e.ctx, after)
		if err != nil {
			t.Fatalf("events after %d: %v (took %v)", after, err, time.Since(tp))
		}
		if d := time.Since(tp); d > time.Second {
			t.Logf("SLOW events page after=%d took %v", after, d)
		}
		read += len(evs)
		after = next
		pages++
		if !more || pages > 4000 {
			break
		}
	}
	line("events_read", read)
	line("events_read_seconds", time.Since(t0).Seconds())
	line("events_per_second", float64(read)/time.Since(t0).Seconds())

	// 5. commands: 8 clients, each claims its own conversations (distinct rows) then sends; throughput and tail latency
	rows, err := e.seed.Query(e.ctx, `SELECT id FROM conversations WHERE tenant_id=$1 ORDER BY id LIMIT 800`, tn)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	var mu sync.Mutex
	var lat []time.Duration
	var wg sync.WaitGroup
	t0 = time.Now()
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < len(ids); i += 8 {
				t1 := time.Now()
				r, err := cli.Claim(e.ctx, ids[i].String(), crmsim.NewKey("bench"), 0)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if _, err := cli.Send(e.ctx, ids[i].String(), crmsim.NewKey("bench"), r.Control.Version, "olá do benchmark"); err != nil {
					t.Errorf("send: %v", err)
					return
				}
				mu.Lock()
				lat = append(lat, time.Since(t1))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	cmds := time.Since(t0)
	line("claim_and_send_pairs", len(lat))
	line("claim_and_send_pairs_per_second", float64(len(lat))/cmds.Seconds())
	line("claim_and_send_p95_ms", float64(pct(lat, 0.95).Microseconds())/1000)

	// 6. webhook fan-out: one subscription over the whole journal
	rcv := newReceiver(t)
	h := &hookEnv{env: e}
	_ = rcv
	_ = h
	var deliveries int64
	hk := uuid.New()
	e.exec(`INSERT INTO webhook_subscriptions(id,tenant_id,installation_id,url,event_types,key_id,secret_ciphertext,enqueued_through_seq) VALUES($1,$2,$3,'https://crm.example.com/h',$4,'whk_bench0000001','\x00',0)`,
		hk, tn, app.Installation.ID, []string{"conversation.created.v1", "message.created.v1", "conversation.control_changed.v1", "message.status_changed.v1"})
	e.sequence()
	t0 = time.Now()
	for {
		var k int
		if err := e.app.QueryRow(e.ctx, `SELECT webhook_fanout(2000)`).Scan(&k); err != nil {
			t.Fatal(err)
		}
		deliveries += int64(k)
		if k == 0 {
			break
		}
	}
	line("fanout_deliveries", deliveries)
	line("fanout_seconds", time.Since(t0).Seconds())
	line("fanout_deliveries_per_second", float64(deliveries)/time.Since(t0).Seconds())

	// 7. no deadlock happened anywhere in the run, and the plan of the list query uses an index
	var dead1 int64
	_ = e.seed.QueryRow(e.ctx, `SELECT deadlocks FROM pg_stat_database WHERE datname=current_database()`).Scan(&dead1)
	line("deadlocks_during_run", dead1-dead0)
	if dead1 != dead0 {
		t.Errorf("%d deadlocks during the benchmark", dead1-dead0)
	}
	plan, err := e.seed.Query(e.ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) SELECT c.id FROM conversations c WHERE c.tenant_id=$1 AND c.status='open' AND c.internal_user_id IS NULL ORDER BY c.created_at DESC, c.id DESC LIMIT 100`, tn)
	if err == nil {
		for plan.Next() {
			var l string
			_ = plan.Scan(&l)
			t.Logf("PLAN %s", l)
		}
		plan.Close()
	}
	// a far-from-reasonable budget: this fails only on a missing index, not on a slow laptop
	if p95 := pct(deep, 0.95); p95 > 3*time.Second {
		t.Errorf("list page p95 = %v at %d conversations", p95, n)
	}
	if float64(pct(first, 0.5).Milliseconds()) > 3000 {
		t.Errorf("first page too slow")
	}
	_ = fmt.Sprint
}
