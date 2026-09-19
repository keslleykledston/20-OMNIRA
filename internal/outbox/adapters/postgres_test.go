package adapters

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/outbox/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

func TestStoreUsesInjectedTransaction(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("database URLs required")
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

	tenantID := uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,'Outbox transaction','active')`, tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenantID) })

	repo := NewPostgresOutboxRepository(app)
	event, err := domain.NewOutboxEvent(tenantID, domain.JobRoutingAssign, domain.AggregateConversation, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("force rollback")
	err = platformdb.WithSystemTenantSession(ctx, app, tenantID, func(scoped context.Context) error {
		if err := repo.Store(scoped, event); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction error=%v", err)
	}
	var count int
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE id=$1`, event.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("outbox event escaped rolled-back transaction")
	}
	committed, err := domain.NewOutboxEvent(tenantID, domain.JobRoutingAssign, domain.AggregateConversation, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := platformdb.WithSystemTenantSession(ctx, app, tenantID, func(scoped context.Context) error {
		return repo.Store(scoped, committed)
	}); err != nil {
		t.Fatal(err)
	}
	if err := platformdb.WithTenantSession(ctx, app, uuid.Nil, true, func(scoped context.Context) error {
		events, err := repo.FindUnpublished(scoped, 10)
		if err != nil {
			return err
		}
		for _, got := range events {
			if got.ID == committed.ID {
				return nil
			}
		}
		return errors.New("runtime system session could not read committed outbox event")
	}); err != nil {
		t.Fatal(err)
	}
}
