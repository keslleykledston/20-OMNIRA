package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	inboxapp "github.com/omnira/omnira/internal/inbox/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestPostgresInboundStoreIsTenantSafeAndIdempotent(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
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
	var roleID uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	tenantA, tenantB, userA := uuid.New(), uuid.New(), uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, userA, userA, userA.String()+"@invalid"); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []uuid.UUID{tenantA, tenantB} {
		if _, err := seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantA, userA, roleID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userA) })
	connectionID := uuid.New()
	store := NewPostgresInboundStore(app)
	svc := inboxapp.NewInboundService(store, store, store, TicketStore{store})
	connection := channeldomain.ChannelConnection{ID: connectionID, TenantID: tenantA}
	inbound := channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "provider-message-1", FromE164: "+5511999999999", Text: "oi"}
	if err := platformdb.WithTenantSession(ctx, app, userA, false, func(sc context.Context) error {
		tc, e := tenancydomain.NewTenantContext(tenantA, userA, tenancydomain.AccessSourceDirect)
		if e != nil {
			return e
		}
		_, e = svc.Ingest(tenancydomain.WithTenantContext(sc, tc), connection, inbound)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := platformdb.WithTenantSession(ctx, app, userA, false, func(sc context.Context) error {
		tc, e := tenancydomain.NewTenantContext(tenantA, userA, tenancydomain.AccessSourceDirect)
		if e != nil {
			return e
		}
		result, e := svc.Ingest(tenancydomain.WithTenantContext(sc, tc), connection, inbound)
		if e != nil {
			return e
		}
		if !result.Duplicate {
			t.Fatal("redelivery created a second message")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var messages, tickets int
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM messages WHERE tenant_id=$1`, tenantA).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tenant_id=$1`, tenantA).Scan(&tickets); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || tickets != 1 {
		t.Fatalf("got messages=%d tickets=%d", messages, tickets)
	}
	if err := platformdb.WithTenantSession(ctx, app, userA, false, func(sc context.Context) error {
		tc, e := tenancydomain.NewTenantContext(tenantB, userA, tenancydomain.AccessSourceDirect)
		if e != nil {
			return e
		}
		_, e = svc.Ingest(tenancydomain.WithTenantContext(sc, tc), connection, inbound)
		if e == nil {
			t.Fatal("tenant B accepted tenant A connection")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
