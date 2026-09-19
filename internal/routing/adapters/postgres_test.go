package adapters

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestAtomicClaimHasExactlyOneWinner(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("database URLs required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	tenantID, userA, userB, contactID, conversationID, protectedConversationID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{userA, userB} {
		if _, err := seed.Exec(ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenantID, "M04 claim"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{userA, userB} {
		if _, err := seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantID, u, roleID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.Exec(ctx, `INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Contato','+5511999999999')`, contactID, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(ctx, `INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conversationID, tenantID, contactID); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(ctx, `INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, protectedConversationID, tenantID, contactID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenantID)
		_, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, userA, userB)
	})
	svc := application.NewService(NewPostgresAssignmentRepository(app))
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, actor := range []uuid.UUID{userA, userB} {
		wg.Add(1)
		go func(actor uuid.UUID) {
			defer wg.Done()
			err := platformdb.WithTenantSession(ctx, app, actor, false, func(sc context.Context) error {
				tc, e := tenancydomain.NewTenantContext(tenantID, actor, tenancydomain.AccessSourceDirect)
				if e != nil {
					return e
				}
				return svc.ClaimOwn(tenancydomain.WithTenantContext(sc, tc), conversationID)
			})
			results <- err
		}(actor)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, application.ErrAlreadyAssigned) {
			conflicts++
		} else {
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	var eventCount int
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM assignment_events WHERE tenant_id=$1 AND conversation_id=$2`, tenantID, conversationID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("assignment history count=%d", eventCount)
	}
	foreignTenant := uuid.New()
	if err := platformdb.WithTenantSession(ctx, app, userA, false, func(sc context.Context) error {
		tc, e := tenancydomain.NewTenantContext(foreignTenant, userA, tenancydomain.AccessSourceDirect)
		if e != nil {
			return e
		}
		err := svc.ClaimOwn(tenancydomain.WithTenantContext(sc, tc), protectedConversationID)
		if !errors.Is(err, application.ErrAlreadyAssigned) {
			t.Fatalf("cross-tenant claim leaked result: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var owner *uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT assigned_to_user_id FROM conversations WHERE id=$1`, protectedConversationID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		t.Fatal("cross-tenant claim changed owner")
	}
}
