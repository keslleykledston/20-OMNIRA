package adapters_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// A light load check on a real PostgreSQL (not a benchmark of the host): many people opening protocols for different conversations at once must get a gap-free,
// duplicate-free sequence, and the reads an attendant waits on (the protocol of a conversation, the cases of a protocol, the history) must stay fast with that many
// protocols standing. The bounds are deliberately generous (a loaded CI box) - they catch an accidental table scan or a lock held across requests, not a 20 % slowdown.
func TestManyProtocolsOpenedAtOnceStayUniqueAndTheReadsStayFast(t *testing.T) {
	if testing.Short() {
		t.Skip("load check")
	}
	s := newStack(t)
	const total, workers = 150, 12
	convs := make([]uuid.UUID, total)
	for i := range convs {
		convs[i], _ = s.conversation()
		s.assign(convs[i], s.agent)
	}

	var mu sync.Mutex
	var openLat []time.Duration
	jobs := make(chan uuid.UUID)
	errs := make(chan error, total)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				c := c
				t0 := time.Now()
				_, err := s.open(s.agent, domain.OpenInput{ConversationID: &c})
				mu.Lock()
				openLat = append(openLat, time.Since(t0))
				mu.Unlock()
				errs <- err
			}
		}()
	}
	for _, c := range convs {
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("open under load: %v", err)
		}
	}
	elapsed := time.Since(start)

	if n := s.count(`SELECT count(DISTINCT protocol_sequence) FROM service_sessions WHERE tenant_id=$1`, s.env.TenantA); n != total {
		t.Fatalf("distinct protocol numbers: %d, want %d", n, total)
	}
	if lo, hi := s.count(`SELECT min(protocol_sequence) FROM service_sessions WHERE tenant_id=$1`, s.env.TenantA), s.count(`SELECT max(protocol_sequence) FROM service_sessions WHERE tenant_id=$1`, s.env.TenantA); lo != 1001 || hi != 1000+total {
		t.Fatalf("the sequence must be gap-free from 1001: %d..%d", lo, hi)
	}
	sort.Slice(openLat, func(i, j int) bool { return openLat[i] < openLat[j] })
	p95 := openLat[len(openLat)*95/100]
	t.Logf("%d protocols opened by %d workers in %s (%.0f/s), open latency p50=%s p95=%s max=%s",
		total, workers, elapsed.Round(time.Millisecond), float64(total)/elapsed.Seconds(), openLat[len(openLat)/2], p95, openLat[len(openLat)-1])
	if p95 > 3*time.Second {
		t.Errorf("open p95 %s under %d concurrent openers", p95, workers)
	}

	// the reads an attendant waits on, with that many protocols standing
	read := func(name string, fn func(ctx context.Context)) {
		var lat []time.Duration
		for i := 0; i < 40; i++ {
			t0 := time.Now()
			s.as(s.agent, s.env.TenantA, fn)
			lat = append(lat, time.Since(t0))
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		p95 := lat[len(lat)*95/100]
		t.Logf("read %-28s p50=%s p95=%s", name, lat[len(lat)/2], p95)
		if p95 > time.Second {
			t.Errorf("%s: p95 %s with %d protocols standing", name, p95, total)
		}
	}
	first := convs[0]
	read("protocol of a conversation", func(ctx context.Context) {
		if _, _, err := s.svc.ForConversation(ctx, first); err != nil {
			t.Fatal(err)
		}
	})
	var sess uuid.UUID
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		got, _, err := s.svc.ForConversation(ctx, first)
		if err != nil || got == nil {
			t.Fatalf("protocol of the first conversation: %v", err)
		}
		sess = got.ID
	})
	read("history of a protocol", func(ctx context.Context) {
		if _, _, err := s.svc.SessionTimeline(ctx, sess, "", 50, false); err != nil {
			t.Fatal(err)
		}
	})
}
