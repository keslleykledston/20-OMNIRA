package realtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/omnira/omnira/internal/testhelpers"
	"github.com/omnira/omnira/internal/worker/realtime"
)

// Postgres NOTIFY -> bridge -> NATS, including recovery after the LISTEN connection is killed.
func TestBridgeForwardsNotificationsAndRecovers(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	natsURL := natsCfg.URL
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	appName := "bridge_test_" + uuid.NewString()[:8]
	bridgePool, err := pgxpool.New(ctx, appURL+"&application_name="+appName)
	if err != nil {
		t.Fatal(err)
	}
	defer bridgePool.Close()
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	tenant, conv := uuid.New(), uuid.New()
	events := make(chan map[string]any, 8)
	sub, err := nc.Subscribe(realtime.Subject(tenant, conv), func(m *nats.Msg) {
		var e map[string]any
		if json.Unmarshal(m.Data, &e) == nil {
			events <- e
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	// A different tenant's subject must never receive this tenant's events.
	foreign := make(chan struct{}, 1)
	fsub, _ := nc.Subscribe(realtime.Subject(uuid.New(), conv), func(*nats.Msg) { foreign <- struct{}{} })
	defer fsub.Unsubscribe()
	_ = nc.Flush()

	bridge := realtime.NewBridge(bridgePool, nc)
	bridge.MinBackoff, bridge.MaxBackoff = 50*time.Millisecond, 200*time.Millisecond
	bctx, bcancel := context.WithCancel(ctx)
	defer bcancel()
	go bridge.Run(bctx)

	notify := func(typ string) {
		payload := fmt.Sprintf(`{"tenant_id":%q,"conversation_id":%q,"type":%q,"data":{"message_id":"m1"}}`, tenant, conv, typ)
		if _, err := seed.Exec(ctx, `SELECT pg_notify('omnira_inbox_events', $1)`, payload); err != nil {
			t.Fatal(err)
		}
	}
	// Wait for the LISTEN to be established, then forward.
	deadline := time.Now().Add(5 * time.Second)
	for {
		notify("message_received")
		select {
		case e := <-events:
			if e["type"] != "message_received" {
				t.Fatalf("event: %v", e)
			}
			goto listening
		case <-time.After(200 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("bridge never started forwarding")
			}
		}
	}
listening:
	// Malformed notifications are dropped, not fatal.
	if _, err := seed.Exec(ctx, `SELECT pg_notify('omnira_inbox_events', 'not json')`); err != nil {
		t.Fatal(err)
	}
	// Kill the bridge's LISTEN backend: it must reconnect and keep forwarding.
	if _, err := seed.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name=$1`, appName); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		notify("message_status")
		select {
		case e := <-events:
			if e["type"] == "message_status" {
				goto recovered
			}
		case <-time.After(300 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("bridge did not recover after the LISTEN connection was terminated")
			}
		}
	}
recovered:
	select {
	case <-foreign:
		t.Fatal("event leaked to another tenant's subject")
	default:
	}
}
