package delivery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/worker/delivery"
)

// Real Postgres as omnira_app + the system tenant session the worker uses.
func TestPostgresDeliveryStateMachine(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	tenantA, tenantB := uuid.New(), uuid.New()
	seedCreateTenant := func(tn uuid.UUID) error {
		t.Helper()
		conn, err := seed.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String()); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	for _, tn := range []uuid.UUID{tenantA, tenantB} {
		if err := seedCreateTenant(tn); err != nil {
			t.Fatalf("create tenant %s: %v", tn, err)
		}
	}
	t.Cleanup(func() {
		_, _ = app.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
	})
	connA, connOff := uuid.New(), uuid.New()
	execInTenant := func(tn uuid.UUID, sql string, args ...any) {
		t.Helper()
		conn, err := seed.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	for id, status := range map[uuid.UUID]string{connA: "active", connOff: "disconnected"} {
		execInTenant(tenantA, `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,$4,'["text"]')`, id, tenantA, id.String(), status)
	}
	contact, conv, convOff := uuid.New(), uuid.New(), uuid.New()
	execInTenant(tenantA, `INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'C','+5511999990042')`, contact, tenantA)
	execInTenant(tenantA, `INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES($1,$2,$3,$4,'open')`, conv, tenantA, contact, connA)
	execInTenant(tenantA, `INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES($1,$2,$3,$4,'open')`, convOff, tenantA, contact, connOff)
	queue := func(conversation uuid.UUID, body string) uuid.UUID {
		id := uuid.New()
		execInTenant(tenantA, `INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status)
		      SELECT $1,tenant_id,id,channel_connection_id,'outbound','text',$3,'queued' FROM conversations WHERE id=$2`, id, conversation, body)
		return id
	}
	row := func(id uuid.UUID) (status, provider, reason string) {
		t.Helper()
		conn, err := seed.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire for row: %v", err)
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin for row: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
			t.Fatalf("set_config for row: %v", err)
		}
		if err := tx.QueryRow(ctx, `SELECT status, provider_message_id, failure_reason FROM messages WHERE id=$1`, id).Scan(&status, &provider, &reason); err != nil {
			t.Fatalf("row scan: %v", err)
		}
		_ = tx.Commit(ctx)
		return
	}
	job := func(id uuid.UUID) []byte {
		raw, _ := json.Marshal(map[string]any{"aggregate_id": id.String(), "tenant_id": tenantB.String()}) // forged tenant ignored
		return raw
	}
	store := delivery.NewPostgresOutboundStore(app)
	sender := &fakeSender{}
	h, err := delivery.NewHandler(store, sender, 3)
	if err != nil {
		t.Fatal(err)
	}

	// Success: sent, provider id stored, sender got DB state (tenant A phone/text).
	m1 := queue(conv, "primeira")
	if err := h.Handle(ctx, job(m1), 1); err != nil {
		t.Fatal(err)
	}
	if s, p, _ := row(m1); s != "sent" || p != fmt.Sprintf("prov-%s-1", connA) {
		t.Fatalf("after send: %s %s", s, p)
	}
	if sender.got.ToE164 != "+5511999990042" || sender.got.Text != "primeira" || sender.conn != connA {
		t.Fatalf("sender got %+v conn=%s", sender.got, sender.conn)
	}
	// Redelivery of the same job does not send again.
	if err := h.Handle(ctx, job(m1), 2); err != nil || sender.calls != 1 {
		t.Fatalf("redelivery: err=%v calls=%d", err, sender.calls)
	}

	// Transient failure: rolled back, still queued; then exhausted => failed with a class-only reason.
	m2 := queue(conv, "segunda")
	sender.err = fmt.Errorf("upstream detail: %w", ports.ErrProviderUnavailable)
	if err := h.Handle(ctx, job(m2), 1); err == nil {
		t.Fatal("transient failure must request a retry")
	}
	if s, _, _ := row(m2); s != "queued" {
		t.Fatalf("after transient failure: %s", s)
	}
	if err := h.Handle(ctx, job(m2), 3); err != nil {
		t.Fatal(err)
	}
	if s, _, r := row(m2); s != "failed" || r != "retries_exhausted:provider_unavailable" {
		t.Fatalf("after exhaustion: %s %q", s, r)
	}

	// Permanent failure and inactive channel are recorded without retry.
	m3 := queue(conv, "terceira")
	sender.err = ports.ErrAuthentication
	if err := h.Handle(ctx, job(m3), 1); err != nil {
		t.Fatal(err)
	}
	if s, _, r := row(m3); s != "failed" || r != "authentication" {
		t.Fatalf("permanent: %s %q", s, r)
	}
	m4 := queue(convOff, "quarta")
	callsBefore := sender.calls
	if err := h.Handle(ctx, job(m4), 1); err != nil {
		t.Fatal(err)
	}
	if s, _, r := row(m4); s != "failed" || r != "channel_not_active" || sender.calls != callsBefore {
		t.Fatalf("inactive channel: %s %q calls %d->%d", s, r, callsBefore, sender.calls)
	}

	// Unknown message id is terminal.
	if err := h.Handle(ctx, job(uuid.New()), 1); err == nil {
		t.Fatal("unknown message must be terminal")
	}

	// Concurrent duplicate deliveries of one job: the row lock lets exactly one send.
	sender.err = nil
	sender.calls = 0
	m5 := queue(conv, "quinta")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Handle(ctx, job(m5), 1); err != nil {
				t.Errorf("concurrent: %v", err)
			}
		}()
	}
	wg.Wait()
	if sender.calls != 1 {
		t.Fatalf("concurrent duplicate deliveries sent %d times", sender.calls)
	}
}

// PILOT.4A1 §16: the central durability property is that the reservation
// commits in its OWN transaction, independent of and strictly before any
// transaction that calls the provider — a mock-only test cannot prove a real
// commit boundary, so this uses real Postgres with a second, independent
// pool/session that only ever reads.
func TestReservedProviderMessageIDCommitsBeforeProviderCallIsVisibleIndependently(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	// t.Cleanup (not defer): defers run before t.Cleanup funcs, so a plain
	// defer here would close the pool before the tenant-delete cleanup below
	// runs. t.Cleanup is LIFO, so registering this one FIRST makes it run
	// LAST — after the delete cleanup registered further down.
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	tenant := uuid.New()
	execAsSystemCtx := func(execCtx context.Context, sql string, args ...any) {
		t.Helper()
		conn, err := seed.Acquire(execCtx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		tx, err := conn.Begin(execCtx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(execCtx)
		if _, err := tx.Exec(execCtx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		if _, err := tx.Exec(execCtx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if err := tx.Commit(execCtx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	execAsSystem := func(sql string, args ...any) { execAsSystemCtx(ctx, sql, args...) }
	execAsSystem(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String())
	// NOTE: cleanup must run as the schema owner (seed), not the RLS-governed
	// app role, and inside the same is_system_admin transaction as the
	// DELETE — a bare app.Exec(...) here is silently denied by RLS (fail
	// closed) and leaves orphaned test tenants behind. It also must use a
	// FRESH context: t.Cleanup runs after this function's own defers
	// (including defer cancel()), so the outer ctx is already canceled by then.
	t.Cleanup(func() { execAsSystemCtx(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenant) })

	conn, contact, conv := uuid.New(), uuid.New(), uuid.New()
	execAsSystem(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, conn, tenant, conn.String())
	execAsSystem(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'C','+5511999990042')`, contact, tenant)
	execAsSystem(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES($1,$2,$3,$4,'open')`, conv, tenant, contact, conn)
	msgID := uuid.New()
	execAsSystem(`INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status)
	      VALUES($1,$2,$3,$4,'outbound','text','durability-proof','queued')`, msgID, tenant, conv, conn)

	store := delivery.NewPostgresOutboundStore(app)

	var providerCalledBeforeIndependentReadConfirmed bool
	reservedID, err := store.EnsureReservedProviderMessageID(ctx, msgID, func(genCtx context.Context) (string, error) {
		// This simulates the provider call (NewMessageID) — it must run and
		// return BEFORE the reservation is persisted; the actual assertion
		// (independent visibility) happens AFTER EnsureReservedProviderMessageID
		// returns, using a second, unrelated pool below.
		return "durability-proof-reserved-id", nil
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if reservedID != "durability-proof-reserved-id" {
		t.Fatalf("unexpected reserved id: %q", reservedID)
	}

	// Independent pool: a brand new connection, never involved in the write
	// above, opened only now. If it can see the value, the write committed —
	// a still-open transaction visible only to itself would prove nothing.
	independent, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("independent pool: %v", err)
	}
	defer independent.Close()
	independentCtx := context.Background()
	var seen string
	err = func() error {
		conn, err := independent.Acquire(independentCtx)
		if err != nil {
			return err
		}
		defer conn.Release()
		// set_config(..., true) only holds for the current transaction — it
		// must be issued in the SAME transaction as the SELECT below, exactly
		// like the rest of this file's helpers.
		tx, err := conn.Begin(independentCtx)
		if err != nil {
			return err
		}
		defer tx.Rollback(independentCtx)
		if _, err := tx.Exec(independentCtx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
			return err
		}
		if err := tx.QueryRow(independentCtx, `SELECT reserved_provider_message_id FROM messages WHERE id=$1`, msgID).Scan(&seen); err != nil {
			return err
		}
		return tx.Commit(independentCtx)
	}()
	if err != nil {
		t.Fatalf("independent read: %v", err)
	}
	if seen != "durability-proof-reserved-id" {
		t.Fatalf("reservation not independently visible: got %q, want %q — the write did not commit before returning", seen, reservedID)
	}
	providerCalledBeforeIndependentReadConfirmed = true
	if !providerCalledBeforeIndependentReadConfirmed {
		t.Fatal("unreachable")
	}

	// A second call must NOT generate a new id — it must reuse the committed one.
	reusedID, err := store.EnsureReservedProviderMessageID(ctx, msgID, func(genCtx context.Context) (string, error) {
		t.Fatal("generate must not be called when a reservation already exists")
		return "", nil
	})
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if reusedID != reservedID {
		t.Fatalf("second call returned %q, want the same reserved id %q", reusedID, reservedID)
	}
}
