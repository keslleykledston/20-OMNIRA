package adapters

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/ratelimit"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ManageSession is the middleware of the Hub-delegated MANAGEMENT routes (ADR-0038 phase 3):
//
//	/api/v1/hubs/{hub_id}/instances/{tenant_id}/...
//
// It opens the caller's OWN database session (RLS stays on: the policies of migration 103 admit exactly what the contract and the
// person's role/grant delegate), holds the company active for the whole request (a suspension waits for it, like every other
// write path), proves the person may manage that instance through THAT hub, and only then builds a TenantContext with the
// dedicated source `hub_manage`. The tenant and the hub in the path are claims, never authority. Anything that does not hold -
// unknown hub, not a member, no delegated scope, no manage right, suspended company, another hub's company - is one uniform 404.
//
// The scope itself (channels, integrations...) is not decided here: each permission asks the database again, live, in the same
// transaction (channeladapters.PostgresPermissionChecker).
func ManageSession(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := authn.FromContext(r.Context())
			if err != nil || principal.UserID == uuid.Nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			hubID, herr := uuid.Parse(r.PathValue("hub_id"))
			tenantID, terr := uuid.Parse(r.PathValue("tenant_id"))
			if herr != nil || terr != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			tw := &trackedWriter{ResponseWriter: w}
			serr := platformdb.WithTenantSession(r.Context(), pool, principal.UserID, false, func(ctx context.Context) error {
				tc, err := resolveManageContext(ctx, pool, hubID, tenantID, principal.UserID)
				if err != nil {
					return err
				}
				if !ratelimit.EnforceTenantUser(tw, r, tc.TenantID, tc.ActorID) {
					return errManageHandled
				}
				next.ServeHTTP(tw, r.WithContext(tenancydomain.WithTenantContext(ctx, tc)))
				return nil
			})
			switch {
			case serr == nil, errors.Is(serr, errManageHandled):
			case errors.Is(serr, errManageDenied):
				http.Error(tw, "not found", http.StatusNotFound)
			default:
				if tw.wrote {
					log.Printf("hub manage: error after the response was written: %v", serr)
					return
				}
				log.Printf("hub manage: %v", serr)
				http.Error(tw, "internal server error", http.StatusInternalServerError)
			}
		})
	}
}

var (
	errManageDenied  = errors.New("hub manage: not found")
	errManageHandled = errors.New("hub manage: handled")
)

type trackedWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackedWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }
func (t *trackedWriter) WriteHeader(code int)        { t.wrote = true; t.ResponseWriter.WriteHeader(code) }
func (t *trackedWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

func resolveManageContext(ctx context.Context, pool *pgxpool.Pool, hubID, tenantID, actor uuid.UUID) (*tenancydomain.TenantContext, error) {
	q := platformdb.QuerierFromContext(ctx, pool)
	var locked *bool
	if err := q.QueryRow(ctx, `SELECT lock_managed_tenant($1, $2)`, tenantID, actor).Scan(&locked); err != nil {
		return nil, err
	}
	if locked == nil || !*locked {
		return nil, errManageDenied
	}
	// the contract and (when there is one) the grant that make this person a manager of THIS instance through THIS hub
	var contract uuid.UUID
	var grant *uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT c.id,
		       (SELECT g.id FROM effective_access_grants g
		         WHERE g.service_contract_id = c.id AND g.user_id = $3 AND g.can_manage AND g.status = 'active'
		           AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
		         ORDER BY g.created_at LIMIT 1)
		FROM hub_tenant_service_contracts c
		WHERE c.hub_id = $1 AND c.tenant_id = $2 AND has_hub_manage_access($2, $3, NULL, $1)`, hubID, tenantID, actor).Scan(&contract, &grant)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errManageDenied
	}
	if err != nil {
		return nil, err
	}
	return tenancydomain.NewHubManageTenantContext(tenantID, actor, hubID, contract, grant, "")
}
