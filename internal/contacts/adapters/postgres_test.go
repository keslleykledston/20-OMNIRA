package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	contactdomain "github.com/omnira/omnira/internal/contacts/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestPostgresContactTenantIsolationAndUpsert(t *testing.T) {
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
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { seed.Close(); app.Close() })

	roleID := uuid.New()
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	userA, userB, tenantA, tenantB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, user := range []uuid.UUID{userA, userB} {
		if _, err := seed.Exec(ctx, `INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, user, user, user.String()+"@invalid"); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		id   uuid.UUID
		name string
	}{{tenantA, "M02 A"}, {tenantB, "M02 B"}} {
		if _, err := seed.Exec(ctx, `INSERT INTO tenants(id, legal_name, status) VALUES($1,$2,'active')`, item.id, item.name); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ tenant, user uuid.UUID }{{tenantA, userA}, {tenantB, userB}} {
		if _, err := seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, item.tenant, item.user, roleID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, userA, userB) })

	repo := NewPostgresContactRepository(app)
	contactA, _ := contactdomain.NewContact(tenantA, "+5511999999999", "Ana")
	contactB, _ := contactdomain.NewContact(tenantB, "+5511888888888", "Bia")
	for _, item := range []struct {
		user    uuid.UUID
		tenant  uuid.UUID
		contact *contactdomain.Contact
	}{{userA, tenantA, contactA}, {userB, tenantB, contactB}} {
		if err := platformdb.WithTenantSession(ctx, app, item.user, false, func(sc context.Context) error {
			scoped, err := contactContext(sc, item.tenant, item.user)
			if err != nil {
				return err
			}
			return repo.Store(scoped, item.contact)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := platformdb.WithTenantSession(ctx, app, userA, false, func(sc context.Context) error {
		scoped, err := contactContext(sc, tenantA, userA)
		if err != nil {
			return err
		}
		got, err := repo.FindByID(scoped, contactA.ID)
		if err != nil || got == nil || got.ID != contactA.ID {
			t.Fatalf("tenant A cannot read A: %v", err)
		}
		other, err := repo.FindByID(scoped, contactB.ID)
		if err != nil {
			return err
		}
		if other != nil {
			t.Fatal("tenant A read tenant B contact")
		}
		replacement, err := contactdomain.NewContact(tenantA, "+5511999999999", "Ana Silva")
		if err != nil {
			return err
		}
		upserted, err := repo.UpsertByPhone(scoped, replacement)
		if err != nil || upserted.ID != contactA.ID || upserted.DisplayName != "Ana Silva" {
			t.Fatalf("upsert did not converge: %+v %v", upserted, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func contactContext(ctx context.Context, tenantID, userID uuid.UUID) (context.Context, error) {
	tc, err := tenancydomain.NewTenantContext(tenantID, userID, tenancydomain.AccessSourceDirect)
	if err != nil {
		return nil, err
	}
	return tenancydomain.WithTenantContext(ctx, tc), nil
}
