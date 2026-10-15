package adapters

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/application"
)

// Build assembles the module on one pool: the service (sessions), the company's catalogs and the HTTP handler.
func Build(pool *pgxpool.Pool) (*application.Service, *application.Catalog, *Handler) {
	repo := NewPostgresRepository(pool)
	authz := NewAuthorizer(pool)
	svc := application.NewService(repo, authz, NewAuditor(pool)).WithEvents(repo).WithTimeline(repo).WithCases(repo).WithTasks(repo).WithContext(repo).WithLinkCounter(repo)
	catalog := application.NewCatalog(repo, authz, svc)
	return svc, catalog, NewHandler(svc, catalog)
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
