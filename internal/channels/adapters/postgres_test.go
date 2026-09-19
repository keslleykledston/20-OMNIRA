package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
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
		_, err = seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, item.id, item.name)
		if err != nil {
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
