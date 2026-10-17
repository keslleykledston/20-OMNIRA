package adapters_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Regressions for the Codex review of H4: lock order, fencing, pause, purged events, the limit of five and a slow receiver.

func TestWebhookPausedAfterTheClaimSendsNothingAndResumesLater(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	app := h.install(h.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	w := h.worker(rcv.client())
	w.AfterClaim = func() { h.exec(`UPDATE webhook_subscriptions SET status='paused' WHERE id=$1`, hk.id) } // the pause lands between the claim and the call
	h.conv(a, convOpts{})
	h.step(w)
	if len(rcv.snapshot()) != 0 {
		t.Fatalf("a call was made to a subscription paused after the claim")
	}
	w.AfterClaim = nil
	h.makeDue(hk)
	h.step(w)
	if len(rcv.snapshot()) != 0 || h.deliveries(hk, "pending") != 1 {
		t.Fatalf("while paused nothing may go: calls=%d pending=%d", len(rcv.snapshot()), h.deliveries(hk, "pending"))
	}
	h.exec(`UPDATE webhook_subscriptions SET status='active' WHERE id=$1`, hk.id)
	h.makeDue(hk)
	h.step(w)
	if h.deliveries(hk, "delivered") != 1 || len(rcv.snapshot()) != 1 {
		t.Fatalf("after the resume the held delivery must go out: delivered=%d calls=%d", h.deliveries(hk, "delivered"), len(rcv.snapshot()))
	}
}

// Only the attempt that was claimed may record its outcome.
func TestWebhookLateCompletionOfAnOlderAttemptIsIgnored(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	app := h.install(h.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	h.conv(a, convOpts{})
	h.sequence()
	if _, err := h.app.Exec(h.ctx, `SELECT webhook_fanout(10)`); err != nil {
		t.Fatal(err)
	}
	claim := func() (uuid.UUID, int) {
		var id uuid.UUID
		var attempts int
		if err := h.app.QueryRow(h.ctx, `SELECT delivery_id, attempts FROM webhook_claim(10, interval '1 millisecond')`).Scan(&id, &attempts); err != nil {
			t.Fatal(err)
		}
		return id, attempts
	}
	id, first := claim() // worker A
	time.Sleep(20 * time.Millisecond)
	id2, second := claim() // the lease ran out: worker B claims the same delivery
	if id != id2 || first != 1 || second != 2 {
		t.Fatalf("claims: %s/%d then %s/%d", id, first, id2, second)
	}
	complete := func(attempt int, ok bool) {
		if _, err := h.app.Exec(h.ctx, `SELECT webhook_complete($1,$2,$3,$4,$5,$6,$7,$8)`, id, a, attempt, ok, 204, "", nil, false); err != nil {
			t.Fatal(err)
		}
	}
	complete(1, true) // A finishes late
	if h.deliveries(hk, "delivered") != 0 || h.deliveries(hk, "pending") != 1 {
		t.Fatalf("a late completion of attempt 1 changed attempt 2's delivery")
	}
	complete(2, true)
	if h.deliveries(hk, "delivered") != 1 {
		t.Fatalf("the current attempt could not complete")
	}
}

// A fact purged while its delivery still waits ends the delivery instead of keeping it pending for ever.
func TestWebhookDeliveryOfAPurgedEventEnds(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	app := h.install(h.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	rcv.status.Store(http.StatusServiceUnavailable)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	w := h.worker(rcv.client())
	h.conv(a, convOpts{})
	h.step(w)
	if h.deliveries(hk, "pending") != 1 {
		t.Fatalf("setup: pending=%d", h.deliveries(hk, "pending"))
	}
	h.exec(`UPDATE domain_events SET occurred_at = now() - interval '40 days' WHERE tenant_id=$1`, a)
	var gone int64
	if err := h.app.QueryRow(h.ctx, `SELECT domain_events_purge(interval '30 days')`).Scan(&gone); err != nil || gone != 1 {
		t.Fatalf("purge removed %d (%v)", gone, err)
	}
	h.makeDue(hk)
	h.step(w)
	var class string
	_ = h.seed.QueryRow(h.ctx, `SELECT last_error_class FROM webhook_deliveries WHERE subscription_id=$1`, hk.id).Scan(&class)
	if h.deliveries(hk, "pending") != 0 || h.deliveries(hk, "cancelled") != 1 || class != "expired" {
		t.Fatalf("pending=%d cancelled=%d class=%q: the delivery of a purged event must end as expired", h.deliveries(hk, "pending"), h.deliveries(hk, "cancelled"), class)
	}
}

// The fan-out and the completion of an attempt take the subscription first: running them against the same subscription and deliveries never deadlocks.
func TestWebhookFanoutAndCompletionNeverDeadlock(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	app := h.install(h.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	conv, _ := h.conv(a, convOpts{})
	stop := make(chan struct{})
	var deadlocks, other atomic.Int32
	note := func(err error) {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "40P01" {
			deadlocks.Add(1)
		} else if err != nil && !errors.Is(err, context.Canceled) {
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
	loop(func(i int) { // producers
		_, err := h.seed.Exec(h.ctx, `INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES(gen_random_uuid(),$1,$2,'inbound','text','x','received')`, a, conv)
		note(err)
	})
	loop(func(i int) { _, err := h.app.Exec(h.ctx, `SELECT domain_events_sequence(50)`); note(err) })
	loop(func(i int) { _, err := h.app.Exec(h.ctx, `SELECT webhook_fanout(10)`); note(err) })
	for k := 0; k < 3; k++ {
		loop(func(i int) { // claim and complete (alternating failure and success)
			rows, err := h.app.Query(h.ctx, `SELECT delivery_id, attempts FROM webhook_claim(20, interval '1 millisecond')`)
			if err != nil {
				note(err)
				return
			}
			type dl struct {
				id       uuid.UUID
				attempts int
			}
			var got []dl
			for rows.Next() {
				var d dl
				_ = rows.Scan(&d.id, &d.attempts)
				got = append(got, d)
			}
			rows.Close()
			for j, d := range got {
				_, err := h.app.Exec(h.ctx, `SELECT webhook_complete($1,$2,$3,$4,$5,$6,$7,$8)`, d.id, a, d.attempts, j%2 == 0, 204, "", nil, false)
				note(err)
			}
		})
	}
	time.Sleep(4 * time.Second)
	close(stop)
	wg.Wait()
	if deadlocks.Load() != 0 || other.Load() != 0 {
		t.Fatalf("deadlocks=%d unexpected errors=%d", deadlocks.Load(), other.Load())
	}
	if h.deliveries(hk, "delivered")+h.deliveries(hk, "pending") == 0 {
		t.Fatal("nothing was processed: the stress did not exercise the functions")
	}
}

func TestWebhookSubscriptionLimitHoldsUnderConcurrentCreation(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	app := h.install(admin, a, "CRM", journalScopes...)
	base := "/" + app.Installation.ID.String() + "/webhooks"
	body := map[string]any{"url": "https://crm.example.com/hook", "event_types": []string{"message.created.v1"}}
	var created, conflict atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, _ := h.admin(admin, a, http.MethodPost, base, body)
			switch code {
			case http.StatusCreated:
				created.Add(1)
			case http.StatusConflict:
				conflict.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	var n int
	_ = h.seed.QueryRow(h.ctx, `SELECT count(*) FROM webhook_subscriptions WHERE installation_id=$1`, app.Installation.ID).Scan(&n)
	if n != 5 || created.Load() != 5 || conflict.Load() != 5 {
		t.Fatalf("subscriptions=%d created=%d conflict=%d, want 5/5/5", n, created.Load(), conflict.Load())
	}
}

// One receiver that does not answer must not hold back the others.
func TestWebhookSlowReceiverDoesNotBlockAHealthyOne(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	admin := h.member(a, "tenant_admin")
	slowApp := h.install(admin, a, "lento", journalScopes...)
	fastApp := h.install(admin, a, "rapido", journalScopes...)
	slow, fast := newReceiver(t), newReceiver(t)
	slow.block = make(chan struct{})
	h.subscribe(slowApp, a, slow.srv.URL+"/s", true)
	hFast := h.subscribe(fastApp, a, fast.srv.URL+"/f", true)
	w := h.worker(clientFor(slow, fast))
	h.conv(a, convOpts{})
	h.sequence()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = w.Step(h.ctx) }()
	select {
	case <-slow.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the slow receiver was never called")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && h.deliveries(hFast, "delivered") == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if h.deliveries(hFast, "delivered") != 1 {
		close(slow.block)
		<-done
		t.Fatal("the healthy receiver waited for the slow one")
	}
	close(slow.block)
	<-done
}

// The lock order is a rule, not luck: a completion waiting for the subscription holds NOTHING on the delivery (the fan-out takes the subscription first).
func TestWebhookCompletionTakesTheSubscriptionBeforeTheDelivery(t *testing.T) {
	h := newHookEnv(t)
	a := h.tenant()
	app := h.install(h.member(a, "tenant_admin"), a, "CRM", journalScopes...)
	rcv := newReceiver(t)
	hk := h.subscribe(app, a, rcv.srv.URL+"/hook", true)
	h.conv(a, convOpts{})
	h.sequence()
	if _, err := h.app.Exec(h.ctx, `SELECT webhook_fanout(10)`); err != nil {
		t.Fatal(err)
	}
	var did uuid.UUID
	var attempts int
	if err := h.app.QueryRow(h.ctx, `SELECT delivery_id, attempts FROM webhook_claim(10, interval '1 minute')`).Scan(&did, &attempts); err != nil {
		t.Fatal(err)
	}
	holder, err := h.seed.Begin(h.ctx) // someone (the fan-out) holds the subscription
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if _, err := holder.Exec(h.ctx, `SELECT 1 FROM webhook_subscriptions WHERE id=$1 FOR UPDATE`, hk.id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.app.Exec(h.ctx, `SELECT webhook_complete($1,$2,$3,$4,$5,$6,$7,$8)`, did, a, attempts, true, 204, "", nil, false)
		done <- err
	}()
	time.Sleep(500 * time.Millisecond) // the completion is now waiting
	select {
	case err := <-done:
		t.Fatalf("the completion did not wait for the subscription (%v)", err)
	default:
	}
	probe, err := h.seed.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, lockErr := probe.Exec(h.ctx, `SELECT 1 FROM webhook_deliveries WHERE id=$1 FOR UPDATE NOWAIT`, did)
	_ = probe.Rollback(h.ctx)
	if lockErr != nil {
		t.Fatalf("a completion that waits for the subscription already holds the delivery (%v): the lock order is delivery -> subscription, the reverse of the fan-out", lockErr)
	}
	if err := holder.Commit(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h.deliveries(hk, "delivered") != 1 {
		t.Fatal("the completion did not finish after the subscription was released")
	}
}
