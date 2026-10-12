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

	// a real conversation of a real tenant: the bridge verifies the pair against the database before publishing
	tenant, conv, contact := uuid.New(), uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, []any{tenant, "Bridge " + tenant.String()[:8]}},
		{`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, 'Cliente', $3)`, []any{contact, tenant, "+5511" + fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)}},
		{`INSERT INTO conversations (id, tenant_id, contact_id) VALUES ($1, $2, $3)`, []any{conv, tenant, contact}},
	} {
		if _, err := seed.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	otherTenant := uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, otherTenant, "Bridge other "+otherTenant.String()[:8]); err != nil {
		t.Fatal(err)
	}
	// the forged pair (another tenant's id, this tenant's conversation) must reach NOBODY, not even the subject it names
	forgedSeen := make(chan struct{}, 1)
	forgedSub, _ := nc.Subscribe(realtime.Subject(otherTenant, conv), func(*nats.Msg) { forgedSeen <- struct{}{} })
	defer forgedSub.Unsubscribe()
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
	// A forged notification (the tenant of the subject is not the conversation's tenant) is dropped.
	forged := fmt.Sprintf(`{"tenant_id":%q,"conversation_id":%q,"type":"message_received","data":{"message_id":"forged"}}`, otherTenant, conv)
	if _, err := seed.Exec(ctx, `SELECT pg_notify('omnira_inbox_events', $1)`, forged); err != nil {
		t.Fatal(err)
	}
	select {
	case <-forgedSeen:
		t.Fatal("a forged (tenant, conversation) pair was published")
	case <-time.After(500 * time.Millisecond):
	}
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
