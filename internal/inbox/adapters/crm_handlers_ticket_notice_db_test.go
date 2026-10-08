package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
)

type sqlFailingNotifier struct{ pool *pgxpool.Pool }

func (f sqlFailingNotifier) NotifyTicketOpened(ctx context.Context, _, _ uuid.UUID, _ string) error {
	_, err := platformdb.QuerierFromContext(ctx, f.pool).Exec(ctx, `SELECT 1/0`)
	return err
}

// The ticket is written in the request's transaction, and the tenant middleware commits it even though the notice
// failed. A database error inside the notice aborts a transaction, so without the savepoint the commit would roll the
// ticket back: this proves the ticket's own work survives, and the transaction is still usable afterwards.
func TestNotifyTicketOpenedDatabaseErrorDoesNotAbortTheTicketTransaction(t *testing.T) {
	_, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	h := NewCRMHandlers(pool)
	h.SetTicketOpenNotifier(sqlFailingNotifier{pool})
	tenantID, actor := uuid.New(), uuid.New()
	tc, err := tenancydomain.NewTenantContext(tenantID, actor, tenancydomain.AccessSourceDirect)
	if err != nil {
		t.Fatal(err)
	}
	err = platformdb.WithTenantSession(ctx, pool, actor, false, func(sctx context.Context) error {
		sctx = tenancydomain.WithTenantContext(sctx, tc)
		q := platformdb.QuerierFromContext(sctx, pool)
		if _, err := q.Exec(sctx, `CREATE TEMP TABLE ticket_work(n int) ON COMMIT DROP`); err != nil {
			return err
		}
		if _, err := q.Exec(sctx, `INSERT INTO ticket_work VALUES (1)`); err != nil {
			return err
		}
		h.notifyTicketOpened(sctx, tenantID, uuid.New(), &ticketsapplication.Result{
			Outcome: ticketsapplication.OutcomeCreated, ExternalTicketID: "28180", LocalTicketID: uuid.New(),
		})
		var n int
		if err := q.QueryRow(sctx, `SELECT count(*) FROM ticket_work`).Scan(&n); err != nil {
			t.Errorf("the transaction was left aborted by the failed notice: %v", err)
			return nil
		}
		if n != 1 {
			t.Errorf("ticket work lost: %d", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the request transaction could not commit: %v", err)
	}
}
