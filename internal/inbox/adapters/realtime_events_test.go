package adapters_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Row changes must enqueue reference-only Outbox events in the same transaction, whatever
// role/session performs the change (tenant session, or owner).
func TestRealtimeTriggersEnqueueReferenceOnlyEvents(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	tenant, user, agent := uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{user, agent} {
		exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	}
	exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String())
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id=$1`, tenant)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id IN ($1,$2)`, user, agent)
	})
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, user, role)
	contact, conv, msg := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Segredo',$3)`, contact, tenant, fmt.Sprintf("+5511%09d", contact.ID()%1000000000))

	// Listen as the application role (proves NOTIFY/LISTEN need no extra privilege).
	listener, err := pgx.Connect(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(context.Background())
	if _, err := listener.Exec(ctx, "LISTEN omnira_inbox_events"); err != nil {
		t.Fatal(err)
	}
	drain := func(n int) (out []map[string]any) {
		t.Helper()
		for len(out) < n {
			wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
			note, err := listener.WaitForNotification(wctx)
			wcancel()
			if err != nil {
				t.Fatalf("expected %d notifications, got %d (%v)", n, len(out), err)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(note.Payload), &m); err != nil {
				t.Fatal(err)
			}
			if m["tenant_id"] == tenant.String() { // other tests may run concurrently on the same DB
				out = append(out, m)
			}
		}
		return
	}

	// Everything below is done through a real tenant session as omnira_app.
	err = platformdb.WithTenantSession(ctx, app, user, false, func(sc context.Context) error {
		q := platformdb.QuerierFromContext(sc, app)
		if _, err := q.Exec(sc, `INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv, tenant, contact); err != nil {
			return err
		}
		if _, err := q.Exec(sc, `INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'inbound','text','texto sensível','received')`, msg, tenant, conv); err != nil {
			return err
		}
		if _, err := q.Exec(sc, `UPDATE conversations SET assigned_to_user_id=$2, updated_at=now() WHERE id=$1`, conv, user); err != nil {
			return err
		}
		if _, err := q.Exec(sc, `UPDATE messages SET status='read' WHERE id=$1`, msg); err != nil {
			return err
		}
		// Updates that change nothing relevant emit nothing.
		_, err := q.Exec(sc, `UPDATE conversations SET title='x', updated_at=now() WHERE id=$1`, conv)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(4)
	// NOTIFY order follows statement order within the transaction.
	wantTypes := []string{"conversation_updated", "message_received", "conversation_updated", "message_status"}
	for i, w := range wantTypes {
		if got[i]["type"] != w || got[i]["conversation_id"] != conv.String() {
			t.Fatalf("notification %d = %v, want %s on %s", i, got[i], w, conv)
		}
	}
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"texto sensível", "Segredo", "+5511"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("realtime events leak content %q: %s", secret, raw)
		}
	}
	data := func(i int) map[string]any { return got[i]["data"].(map[string]any) }
	if data(0)["reason"] != "created" || data(1)["message_id"] != msg.String() || data(1)["direction"] != "inbound" ||
		data(2)["assigned_to_user_id"] != user.String() || data(3)["status"] != "read" {
		t.Fatalf("payload data: %s", raw)
	}

	// A rolled-back change must emit nothing (NOTIFY is transactional), and the durable Outbox stays untouched.
	_ = platformdb.WithTenantSession(ctx, app, user, false, func(sc context.Context) error {
		q := platformdb.QuerierFromContext(sc, app)
		if _, err := q.Exec(sc, `INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'inbound','text','x','received')`, uuid.New(), tenant, conv); err != nil {
			return err
		}
		return fmt.Errorf("force rollback")
	})
	wctx, wcancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer wcancel()
	for {
		note, err := listener.WaitForNotification(wctx)
		if err != nil {
			break // timeout: nothing leaked
		}
		if strings.Contains(note.Payload, tenant.String()) {
			t.Fatalf("rolled-back change notified: %s", note.Payload)
		}
	}
	var outbox int
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type <> 'job.inbox.message_persisted.v1'`, tenant).Scan(&outbox); err != nil || outbox != 0 {
		t.Fatalf("realtime must not touch the durable outbox: %d %v", outbox, err)
	}
	// The one legitimate emitter is the persisted-message event (ADR-0017 Wave 4): exactly one for the committed inbound
	// message, and none for the rolled-back one, because it is written in the same transaction as the message.
	var persisted int
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type = 'job.inbox.message_persisted.v1'`, tenant).Scan(&persisted); err != nil || persisted != 1 {
		t.Fatalf("persisted-message events = %d (%v), want exactly 1: the rolled-back message must not emit", persisted, err)
	}
}
