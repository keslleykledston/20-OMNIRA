package adapters

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// Build assembles the module on one pool: the service (sessions), the company's catalogs and the HTTP handler.
// Options are the deployment choices of the module.
type Options struct {
	// ExternalOrders mounts the routes that ask the ERP to do something (OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED).
	ExternalOrders bool
	// Gateways resolves the adapter of a company's ERP integration. nil = none is configured (every ERP call answers 503 not_configured, never a fake success).
	Gateways ports.GatewayResolver
	// TxLockTimeout bounds the lock waits of the independent transactions of an ERP request (default 5 s).
	TxLockTimeout time.Duration
}

func Build(pool *pgxpool.Pool, opts Options) (*application.Service, *application.Catalog, *Handler) {
	repo := NewPostgresRepository(pool)
	repo.SetTxLockTimeout(opts.TxLockTimeout)
	authz := NewAuthorizer(pool)
	svc := application.NewService(repo, authz, NewAuditor(pool)).WithEvents(repo).WithTimeline(repo).WithCases(repo).WithTasks(repo).WithContext(repo).WithLinkCounter(repo).WithCatalog(repo).WithExternal(repo, opts.Gateways)
	catalog := application.NewCatalog(repo, authz, svc)
	return svc, catalog, NewHandler(svc, catalog).WithExternalOrders(opts.ExternalOrders)
}

// FinalizeHook is the attendance finalization's way into the module (ADR-0049 §3). It runs inside the finalize transaction under a SAVEPOINT: if the
// module fails (a missing key, a bug), the savepoint is rolled back, the failure is logged with ids only, and the attendance still ends. Finalizing a
// conversation never waits on the optional module.
func FinalizeHook(pool *pgxpool.Pool, svc *application.Service) attendanceapplication.SessionCloser {
	return func(ctx context.Context, conversationID uuid.UUID, reason, source string, actor uuid.UUID) error {
		err := platformdb.WithSavepoint(ctx, pool, func(ctx context.Context) error {
			return svc.CloseForFinalize(ctx, conversationID, reason, source, actor)
		})
		if err != nil {
			log.Printf("service-context: closing the session of conversation %s on finalize failed: %v", conversationID, err)
		}
		return nil
	}
}
