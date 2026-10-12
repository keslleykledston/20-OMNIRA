package adapters_test

// Load proof of the Hub inbox over HTTP (phase 14 of the mission): many Hub agents reading a large inbox AT THE SAME TIME through the real handler, the real
// connection pool, the application role under FORCE RLS and the signed session context (mode enforce in the gate). It proves latency and the absence of errors
// under concurrency; it does not replace the single-statement cost proof of scripts/bench-hub-policies.sh.
//
// Off by default (it seeds tens of thousands of rows): OMNIRA_LOAD_TEST=1 turns it on. Knobs: LOAD_ROWS (20000), LOAD_CLIENTS (16), LOAD_SECONDS (8),
// LOAD_P95_MS (1000, lists and opening), LOAD_COUNT_P95_MS (3000, the counters: they count every visible item) and LOAD_P99_MS (4000). Run it with scripts/test-hub-load.sh (a throwaway database; never production).

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func TestHubInboxUnderLoad(t *testing.T) {
	if os.Getenv("OMNIRA_LOAD_TEST") != "1" {
		t.Skip("load proof is off; OMNIRA_LOAD_TEST=1 (scripts/test-hub-load.sh) turns it on")
	}
	rows, clients, seconds := envInt("LOAD_ROWS", 20000), envInt("LOAD_CLIENTS", 16), envInt("LOAD_SECONDS", 8)
	p95Max, p99Max := time.Duration(envInt("LOAD_P95_MS", 1000))*time.Millisecond, time.Duration(envInt("LOAD_P99_MS", 4000))*time.Millisecond
	countP95Max := time.Duration(envInt("LOAD_COUNT_P95_MS", 3000)) * time.Millisecond

	w := newWorld(t)
	api := newHubAPI(t, w)
	agents := make([]uuid.UUID, 0, 4)
	for i := 0; i < 4; i++ {
		a := w.hubAgent(fmt.Sprintf("load-agent-%d", i))
		g := w.grant(a, "A")
		_ = g
		agents = append(agents, a)
	}
	// rows of company A: a third finalized, a quarter held by somebody, five queues' worth of spread is not needed (the contract is unrestricted)
	w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164)
	        SELECT md5('lc' || g)::uuid, $1, 'Cliente ' || g, '+55' || lpad((3000000000 + g)::text, 11, '0') FROM generate_series(1, $2) g`, w.tenant["A"], rows)
	w.exec(`INSERT INTO conversations (id, tenant_id, contact_id, queue_id, status, assigned_to_user_id, created_at)
	        SELECT md5('lv' || g)::uuid, $1, md5('lc' || g)::uuid, $2, CASE WHEN g % 3 = 0 THEN 'closed' ELSE 'open' END,
	               CASE WHEN g % 4 = 0 THEN $4::uuid END, now() - g * interval '1 minute' FROM generate_series(1, $3) g`, w.tenant["A"], w.queue1["A"], rows, agents[0])
	w.exec(`INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id, queue_id, customer_name, status, assigned_user_id, last_activity_at)
	        SELECT $1, c.tenant_id, c.id, c.queue_id, ct.display_name, c.status, c.assigned_to_user_id, c.created_at
	        FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE c.tenant_id = $2 AND c.created_at < now() - interval '30 seconds'
	        ON CONFLICT DO NOTHING`, w.hub, w.tenant["A"])
	w.exec(`ANALYZE`)
	var item uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM hub_inbox_items WHERE tenant_id = $1 AND status <> 'closed' ORDER BY last_activity_at DESC LIMIT 1`, w.tenant["A"]).Scan(&item))

	type call struct {
		name string
		path func(i int) string
	}
	base := fmt.Sprintf("/api/v1/hubs/%s/inbox", w.hub)
	calls := []call{
		{"list waiting", func(int) string { return base + "?segment=waiting&limit=30" }},
		{"list all + counters", func(int) string { return base + "?segment=open&limit=30&include=counts" }},
		{"list history", func(int) string { return base + "?segment=closed&limit=30" }},
		{"search", func(i int) string { return base + "?segment=open&limit=30&q=" + strconv.Itoa(100+i%800) }},
		{"companies counters", func(int) string { return base + "?segment=open&limit=1&include=company_counts" }},
		{"open one", func(int) string { return base + "/" + item.String() }},
	}
	var mu sync.Mutex
	lat := map[string][]time.Duration{}
	var failures, total atomic.Int64
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	var wg sync.WaitGroup
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				cl := calls[(c+i)%len(calls)]
				start := time.Now()
				code, _, _ := api.doQuiet("GET", cl.path(i), agents[c%len(agents)])
				d := time.Since(start)
				total.Add(1)
				if code != 200 {
					failures.Add(1)
				}
				mu.Lock()
				lat[cl.name] = append(lat[cl.name], d)
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()

	pct := func(ds []time.Duration, p float64) time.Duration {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[int(float64(len(ds)-1)*p)]
	}
	names := make([]string, 0, len(lat))
	for n := range lat {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("rows=%d clients=%d seconds=%d requests=%d failures=%d (%.1f req/s)", rows, clients, seconds, total.Load(), failures.Load(), float64(total.Load())/float64(seconds))
	for _, n := range names {
		ds := lat[n]
		p50, p95, p99 := pct(ds, .50), pct(ds, .95), pct(ds, .99)
		t.Logf("%-22s n=%-5d p50=%-8s p95=%-8s p99=%s", n, len(ds), p50.Round(time.Millisecond), p95.Round(time.Millisecond), p99.Round(time.Millisecond))
		limit := p95Max
		if strings.Contains(n, "counters") {
			limit = countP95Max // they count every visible item (opt-in on the server; the Hub page asks once per poll)
		}
		if p95 > limit {
			t.Errorf("%s: p95 %s over the limit %s", n, p95, limit)
		}
		if p99 > p99Max {
			t.Errorf("%s: p99 %s over the limit %s", n, p99, p99Max)
		}
	}
	if failures.Load() != 0 {
		t.Errorf("%d of %d requests did not answer 200", failures.Load(), total.Load())
	}
}
