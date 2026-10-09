package adapters

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/ratelimit"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// Delegated serving (ADR-0040 phase 02). A request that declares `X-Omnira-Acting-As: hub:<id>` is admitted here, with ONE source of
// authorization: the Hub's relationship to the instance, proven by the database. The person's membership (if any) is not consulted and
// contributes nothing; a request without the header (or with `member`) never reaches this file. The whole path is off until the
// process switch is turned on, because no row-level policy accepts the delegated context yet: until phase 03 a delegated request is
// admitted but the data layer still answers only what it already did for Hub people.
var delegatedServing atomic.Bool

// EnableDelegatedServing — the process-wide switch (OMNIRA_HUB_SERVE_ENABLED), read at request time.
func EnableDelegatedServing(on bool) { delegatedServing.Store(on) }

// DelegatedServingEnabled — whether the switch is on (the Hub inbox only advertises the full context when it is).
func DelegatedServingEnabled() bool { return delegatedServing.Load() }

var (
	errServeDenied  = errors.New("delegated serving: not found")
	errServeHandled = errors.New("delegated serving: handled")
	errServeForbid  = errors.New("delegated serving: permission not held")
)

type delegableKey struct{}

// Delegable — marks a route as one a Hub agent may reach in the delegated context, and names the permission key it needs. It wraps the
// OUTERMOST handler of the route (before authentication), so the middleware can read it. A route that is not marked is NOT reachable in
// the delegated context at all (404), whatever its own authorization says: the module-local checks of routes that were not migrated still look at
// memberships, and without this a person who is both a member and a delegate would reach them with their membership while acting for a hub.
// Members' requests are not affected in any way.
func Delegable(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), delegableKey{}, permission)))
	})
}

// resolveServeContext — lock_served_tenant validates the whole chain (account, hub, instance, contract, hub membership, individual
// grant, at least one delegable permission), pins those rows for the transaction and sets the acting hub for it. Only then are the ids of
// the contract and the grant read, for the audit trail.
func resolveServeContext(ctx context.Context, pool *pgxpool.Pool, hubID, tenantID, actor uuid.UUID) (*domain.TenantContext, error) {
	q := platformdb.QuerierFromContext(ctx, pool)
	var locked *bool
	if err := q.QueryRow(ctx, `SELECT lock_served_tenant($1, $2, $3)`, tenantID, actor, hubID).Scan(&locked); err != nil {
		return nil, err
	}
	if locked == nil || !*locked {
		return nil, errServeDenied
	}
	var contract, grant uuid.UUID
	var permissions []string
	err := q.QueryRow(ctx, `
		SELECT c.id, g.id, delegated_permissions($2, $3, $1)
		FROM hub_tenant_service_contracts c
		JOIN effective_access_grants g ON g.service_contract_id = c.id AND g.hub_id = c.hub_id AND g.tenant_id = c.tenant_id AND g.user_id = $3
		WHERE c.hub_id = $1 AND c.tenant_id = $2`, hubID, tenantID, actor).Scan(&contract, &grant, &permissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errServeDenied
	}
	if err != nil {
		return nil, err
	}
	return domain.NewHubServeTenantContext(tenantID, actor, hubID, contract, grant, permissions, "")
}

// serveDelegated — the delegated branch of AuthorizationMiddleware.
func serveDelegated(w http.ResponseWriter, r *http.Request, next http.Handler, pool *pgxpool.Pool, principal *authn.Principal, tenantID, hubID uuid.UUID) {
	if !delegatedServing.Load() {
		http.Error(w, "delegated context is not enabled", http.StatusForbidden)
		return
	}
	permission, delegable := r.Context().Value(delegableKey{}).(string)
	if !delegable {
		log.Printf("tenancy: delegated context refused: route %q is not delegable (tenant=%s hub=%s actor=%s)", r.Pattern, tenantID, hubID, principal.UserID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	tw := &trackedResponseWriter{ResponseWriter: w}
	serr := platformdb.WithTenantSession(r.Context(), pool, principal.UserID, false, func(ctx context.Context) error {
		tc, err := resolveServeContext(ctx, pool, hubID, tenantID, principal.UserID)
		if err != nil {
			return err
		}
		if !ratelimit.EnforceTenantUser(tw, r, tc.TenantID, tc.ActorID) {
			return errServeHandled
		}
		// the key THIS route needs, asked of the database in this very transaction (live), in the delegated context
		held, err := ActorHasPermission(ctx, platformdb.QuerierFromContext(ctx, pool), tc.TenantID, tc.ActorID, permission)
		if err != nil {
			return err
		}
		if !held {
			return errServeForbid
		}
		next.ServeHTTP(tw, r.WithContext(domain.WithTenantContext(ctx, tc)))
		return nil
	})
	switch {
	case serr == nil, errors.Is(serr, errServeHandled):
	case errors.Is(serr, errServeForbid):
		http.Error(tw, "forbidden", http.StatusForbidden)
	case errors.Is(serr, errServeDenied):
		// the same answer for a stranger, a revoked grant and an instance that does not exist; ids only in the log, never a reason to the client
		log.Printf("tenancy: delegated context refused (tenant=%s hub=%s actor=%s)", tenantID, hubID, principal.UserID)
		http.Error(tw, "not found", http.StatusNotFound)
	default:
		if tw.wrote {
			log.Printf("tenancy: delegated serving: error after the response was written: %v", serr)
			return
		}
		log.Printf("tenancy: delegated serving: %v", serr)
		http.Error(tw, "internal server error", http.StatusInternalServerError)
	}
}

// ActorHasPermission — "may this person do <permission> in this instance, in the context THIS request acts in?", answered by the
// database (actor_has_permission): the member's role permissions when not acting for a hub, only the delegated ones when acting for one,
// never both. The modules still ask their own SQL today; each one moves to this call in its own phase.
func ActorHasPermission(ctx context.Context, q platformdb.Querier, tenantID, userID uuid.UUID, permission string) (bool, error) {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT actor_has_permission($1, $2, $3)`, tenantID, userID, permission).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// DelegatedPath — the same delegated admission for a route whose client cannot send the acting header (an <img>, <audio> or <video> loads
// its source without custom headers): the hub is named in the PATH instead, `/api/v1/hubs/{hub_id}/serve/{tenant_id}/...`. It is the same
// declaration of context, validated by the same door (lock_served_tenant); the path is a target, never authority. Wrap the handler with
// Delegable(...) and register it under that path.
func DelegatedPath(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := authn.FromContext(r.Context())
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			hubID, herr := uuid.Parse(r.PathValue("hub_id"))
			tenantID, terr := uuid.Parse(r.PathValue("tenant_id"))
			if herr != nil || terr != nil || hubID == uuid.Nil || tenantID == uuid.Nil {
				http.Error(w, "invalid acting context", http.StatusBadRequest)
				return
			}
			serveDelegated(w, r, next, pool, principal, tenantID, hubID)
		})
	}
}
