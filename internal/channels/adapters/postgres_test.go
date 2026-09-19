package adapters

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	testhelper "github.com/omnira/omnira/internal/testhelpers"
)

type channelIsolationFixture struct {
	seed, app                      *pgxpool.Pool
	userA, userB, tenantA, tenantB uuid.UUID
}

func newChannelIsolationFixture(t *testing.T) channelIsolationFixture {
	t.Helper()
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if err := seed.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	f := channelIsolationFixture{seed: seed, app: app, userA: uuid.New(), userB: uuid.New(), tenantA: uuid.New(), tenantB: uuid.New()}
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{f.userA, f.userB} {
		_, err = seed.Exec(ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u.String(), u.String()+"@invalid")
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		id   uuid.UUID
		name string
	}{{f.tenantA, "D31 A"}, {f.tenantB, "D31 B"}} {
		if err := testhelper.CreateTenantWithRLS(ctx, seed, item.id, item.name); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ tenant, user uuid.UUID }{{f.tenantA, f.userA}, {f.tenantB, f.userB}} {
		_, err = seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, item.tenant, item.user, role)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, f.userA, f.userB)
		seed.Close()
		app.Close()
	})
	return f
}

func channelConn(tenant uuid.UUID, id uuid.UUID, number string) *domain.ChannelConnection {
	now := time.Now().UTC()
	return &domain.ChannelConnection{ID: id, TenantID: tenant, Channel: domain.ChannelWhatsApp, Provider: domain.ProviderMetaCloud, ProviderKind: domain.ProviderKindOfficial, ExternalNumberID: number, Status: domain.ConnectionStatusActive, Capabilities: []domain.Capability{domain.CapabilityText}, CreatedAt: now, UpdatedAt: now}
}

func TestPostgresChannelIsolationAndCredentials(t *testing.T) {
	f := newChannelIsolationFixture(t)
	ctx := context.Background()
	cipher, err := channelcrypto.NewAESGCM([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresChannelConnectionRepository(f.app)
	store := NewPostgresCredentialStore(f.app, cipher)
	connA, connB := channelConn(f.tenantA, uuid.New(), "d31-a-"+uuid.New().String()), channelConn(f.tenantB, uuid.New(), "d31-b-"+uuid.New().String())
	for _, item := range []struct {
		user uuid.UUID
		conn *domain.ChannelConnection
	}{{f.userA, connA}, {f.userB, connB}} {
		err = platformdb.WithTenantSession(ctx, f.app, item.user, false, func(sc context.Context) error { return repo.Store(sc, item.conn) })
		if err != nil {
			t.Fatal(err)
		}
	}
	var refA string
	err = platformdb.WithTenantSession(ctx, f.app, f.userA, false, func(sc context.Context) error {
		refA, err = store.Store(sc, connA.ID, ports.Credential{Fields: map[string]string{"access_token": "secret-a"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = platformdb.WithTenantSession(ctx, f.app, f.userA, false, func(sc context.Context) error {
		got, e := repo.FindByID(sc, connA.ID)
		if e != nil || got == nil {
			t.Fatalf("A cannot read A: %v", e)
		}
		if _, e = repo.FindByID(sc, connB.ID); e != nil {
			t.Fatalf("cross-tenant read should be hidden, not error: %v", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = platformdb.WithTenantSession(ctx, f.app, f.userA, false, func(sc context.Context) error {
		got, e := store.Resolve(sc, refA)
		if e != nil {
			return e
		}
		if got.Fields["access_token"] != "secret-a" {
			t.Fatal("credential A did not resolve in A")
		}
		_, e = store.Resolve(sc, "00000000-0000-0000-0000-000000000002")
		if e == nil {
			t.Fatal("unknown/cross-tenant secret unexpectedly resolved")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := f.seed.QueryRow(ctx, `SELECT ciphertext FROM channel_credentials WHERE id=$1`, refA).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == `{"access_token":"secret-a"}` || string(ciphertext) == "secret-a" {
		t.Fatal("plaintext credential stored")
	}
	wrong, _ := channelcrypto.NewAESGCM([]byte("abcdefghijklmnopqrstuvwxyz123456"))
	if _, err := wrong.Decrypt(ciphertext); err == nil {
		t.Fatal("wrong key decrypted credential")
	}
	_ = connA
}

func TestPostgresWebhookEventStoreReservesOnce(t *testing.T) {
	f := newChannelIsolationFixture(t)
	ctx := context.Background()
	repo := NewPostgresChannelConnectionRepository(f.app)
	store := NewPostgresWebhookEventStore(f.app)
	conn := channelConn(f.tenantA, uuid.New(), "waha-a-"+uuid.New().String())
	conn.Provider = domain.ProviderWAHA
	conn.ProviderKind = domain.ProviderKindUnofficial
	if err := platformdb.WithTenantSession(ctx, f.app, f.userA, false, func(sc context.Context) error {
		return repo.Store(sc, conn)
	}); err != nil {
		t.Fatal(err)
	}
	var first, second bool
	if err := platformdb.WithTenantSession(ctx, f.app, uuid.Nil, true, func(sc context.Context) error {
		var err error
		first, err = store.MarkReceived(sc, *conn, "msg-a", "message.any", "digest-a")
		if err != nil {
			return err
		}
		second, err = store.MarkReceived(sc, *conn, "msg-a", "message.any", "digest-a")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if first || !second {
		t.Fatalf("expected first delivery new and second duplicate: first=%v second=%v", first, second)
	}
}

func TestPostgresMetaResolverAndWebhookDedupe(t *testing.T) {
	f := newChannelIsolationFixture(t)
	ctx := context.Background()
	repo := NewPostgresChannelConnectionRepository(f.app)
	store := NewPostgresWebhookEventStore(f.app)
	resolver := NewMetaWebhookConnectionResolver(f.app, repo)
	numA, numB := "meta-a-"+uuid.New().String(), "meta-b-"+uuid.New().String()
	connA, connB := channelConn(f.tenantA, uuid.New(), numA), channelConn(f.tenantB, uuid.New(), numB)
	for _, item := range []struct {
		user uuid.UUID
		conn *domain.ChannelConnection
	}{{f.userA, connA}, {f.userB, connB}} {
		if err := platformdb.WithTenantSession(ctx, f.app, item.user, false, func(sc context.Context) error { return repo.Store(sc, item.conn) }); err != nil {
			t.Fatal(err)
		}
	}
	// Each phone_number_id resolves to its own tenant, never the other's.
	gotA, err := resolver.ResolveInboundConnection(ctx, domain.ProviderMetaCloud, numA)
	if err != nil || gotA.TenantID != f.tenantA || gotA.ID != connA.ID {
		t.Fatalf("A: %+v err=%v", gotA, err)
	}
	gotB, err := resolver.ResolveInboundConnection(ctx, domain.ProviderMetaCloud, numB)
	if err != nil || gotB.TenantID != f.tenantB {
		t.Fatalf("B: %+v err=%v", gotB, err)
	}
	if _, err := resolver.ResolveInboundConnection(ctx, domain.ProviderMetaCloud, "unknown-"+uuid.New().String()); err == nil {
		t.Fatal("unknown phone_number_id must not resolve")
	}
	if _, err := resolver.ResolveInboundConnection(ctx, domain.ProviderWAHA, numA); err == nil {
		t.Fatal("wrong provider must be rejected")
	}
	// Dedupe works for Meta connections (regression: was hardcoded to WAHA).
	var first, second bool
	if err := platformdb.WithTenantSession(ctx, f.app, uuid.Nil, true, func(sc context.Context) error {
		var e error
		if first, e = store.MarkReceived(sc, *gotA, "wamid.x", "message", "d"); e != nil {
			return e
		}
		second, e = store.MarkReceived(sc, *gotA, "wamid.x", "message", "d")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if first || !second {
		t.Fatalf("first=%v second=%v", first, second)
	}
}

func TestPostgresCredentialRotateAndConnectionUpdate(t *testing.T) {
	f := newChannelIsolationFixture(t)
	ctx := context.Background()
	cipher, err := channelcrypto.NewAESGCM([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresChannelConnectionRepository(f.app)
	store := NewPostgresCredentialStore(f.app, cipher)
	connA, connB := channelConn(f.tenantA, uuid.New(), "rot-a-"+uuid.New().String()), channelConn(f.tenantB, uuid.New(), "rot-b-"+uuid.New().String())
	for _, item := range []struct {
		user uuid.UUID
		conn *domain.ChannelConnection
	}{{f.userA, connA}, {f.userB, connB}} {
		if err := platformdb.WithTenantSession(ctx, f.app, item.user, false, func(sc context.Context) error { return repo.Store(sc, item.conn) }); err != nil {
			t.Fatal(err)
		}
	}
	var refA, refB string
	if err := platformdb.WithTenantSession(ctx, f.app, f.userA, false, func(sc context.Context) error {
		var e error
		refA, e = store.Store(sc, connA.ID, ports.Credential{Fields: map[string]string{"access_token": "old-a"}})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := platformdb.WithTenantSession(ctx, f.app, f.userB, false, func(sc context.Context) error {
		var e error
		refB, e = store.Store(sc, connB.ID, ports.Credential{Fields: map[string]string{"access_token": "old-b"}})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	// Rotate keeps the opaque secret ref and replaces the value.
	if err := platformdb.WithTenantSession(ctx, f.app, f.userA, false, func(sc context.Context) error {
		if e := store.Rotate(sc, refA, ports.Credential{Fields: map[string]string{"access_token": "new-a"}}); e != nil {
			return e
		}
		got, e := store.Resolve(sc, refA)
		if e != nil || got.Fields["access_token"] != "new-a" {
			t.Fatalf("rotated value not resolved: %v %v", got, e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Tenant A cannot rotate or read tenant B's credential even knowing its ref (RLS).
	if err := platformdb.WithTenantSession(ctx, f.app, f.userA, false, func(sc context.Context) error {
		if e := store.Rotate(sc, refB, ports.Credential{Fields: map[string]string{"access_token": "evil"}}); e == nil {
			t.Fatal("cross-tenant rotate succeeded")
		}
		if _, e := store.Resolve(sc, refB); e == nil {
			t.Fatal("cross-tenant resolve succeeded")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := platformdb.WithTenantSession(ctx, f.app, f.userB, false, func(sc context.Context) error {
		got, e := store.Resolve(sc, refB)
		if e != nil || got.Fields["access_token"] != "old-b" {
			t.Fatalf("B credential altered: %v %v", got, e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Connection update persists status changes.
	if err := platformdb.WithTenantSession(ctx, f.app, f.userA, false, func(sc context.Context) error {
		connA.Status = domain.ConnectionStatusDisconnected
		if e := repo.Update(sc, connA); e != nil {
			return e
		}
		got, e := repo.FindByID(sc, connA.ID)
		if e != nil || got == nil || got.Status != domain.ConnectionStatusDisconnected {
			t.Fatalf("status not updated: %+v %v", got, e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialIsRedactedWhenFormatted(t *testing.T) {
	c := ports.Credential{Fields: map[string]string{"access_token": "super_secret", "device_id": "device_123"}}
	for _, s := range []string{c.String(), c.GoString()} {
		if s == "" || strings.Contains(s, "super_secret") || strings.Contains(s, "device_123") {
			t.Fatalf("credential leaked or empty: %q", s)
		}
	}
}
