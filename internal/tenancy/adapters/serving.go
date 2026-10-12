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
	"github.com/omnira/omnira/internal/platform/opsmetrics"
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
	errServeDenied   = errors.New("delegated serving: not found")
	errServeHandled  = errors.New("delegated serving: handled")
	errServeInternal = errors.New("delegated serving: internal conversation")
	errServeForbid   = errors.New("delegated serving: permission not held")
)

type delegableKey struct{}

// Delegable — marks a route as one a Hub agent may reach in the delegated context, and names the permission key it needs. It wraps the
// OUTERMOST handler of the route (before authentication), so the middleware can read it. A route that is not marked is NOT reachable in
// the delegated context at all (404), whatever its own authorization says: the module-local checks of routes that were not migrated still look at
// memberships, and without this a person who is both a member and a delegate would reach them with their membership while acting for a hub.
// Members' requests are not affected in any way.
func Delegable(permission string, next http.Handler) http.Handler {
	return DelegableAny(next, permission)
}

// DelegableAny — the same marking for a route that serves more than one purpose (the ERP company directory is read to open a ticket AND to classify a
// contact): it is reachable when the person holds ANY of the keys. Still default-deny, still asked of the database in the request's own transaction.
func DelegableAny(next http.Handler, permissions ...string) http.Handler {
	keys := append([]string(nil), permissions...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), delegableKey{}, keys)))
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

// delegationMetric counts how requests acting for a Hub ended (closed set: never an id). Refusals and forbidden keys are the signals worth an alert when they jump;
// the people and instances involved are in the log lines next to them.
var delegationMetric = opsmetrics.NewCounter("omnira_hub_delegation_requests_total", "Requests with X-Omnira-Acting-As by outcome", "outcome",
	"served", "rate_limited", "disabled", "not_delegable", "refused", "forbidden_key", "internal_conversation", "error")

// serveDelegated — the delegated branch of AuthorizationMiddleware.
func serveDelegated(w http.ResponseWriter, r *http.Request, next http.Handler, pool *pgxpool.Pool, principal *authn.Principal, tenantID, hubID uuid.UUID) {
	if !delegatedServing.Load() {
		delegationMetric.Inc("disabled")
		http.Error(w, "delegated context is not enabled", http.StatusForbidden)
		return
	}
	permissions, delegable := r.Context().Value(delegableKey{}).([]string)
	if !delegable || len(permissions) == 0 {
		delegationMetric.Inc("not_delegable")
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
		// the key THIS route needs (any of them, for a route that serves more than one purpose), asked of the database in this very transaction
		// (live), in the delegated context
		held := false
		for _, permission := range permissions {
			ok, err := ActorHasPermission(ctx, platformdb.QuerierFromContext(ctx, pool), tc.TenantID, tc.ActorID, permission)
			if err != nil {
				return err
			}
			if ok {
				held = true
				break
			}
		}
		if !held {
			return errServeForbid
		}
		// a Hub agent never reads the conversations of an internal contact (the key is not delegable); same answer as a missing one
		if refused, rerr := RefusesInternalConversation(ctx, platformdb.QuerierFromContext(ctx, pool), r, tc); rerr != nil {
			return rerr
		} else if refused {
			return errServeInternal
		}
		next.ServeHTTP(tw, r.WithContext(domain.WithTenantContext(ctx, tc)))
		return nil
	})
	switch {
	case serr == nil:
		delegationMetric.Inc("served")
	case errors.Is(serr, errServeHandled):
		delegationMetric.Inc("rate_limited")
	case errors.Is(serr, errServeInternal):
		delegationMetric.Inc("internal_conversation")
		WriteInternalRefusal(tw, r)
	case errors.Is(serr, errServeForbid):
		delegationMetric.Inc("forbidden_key")
		http.Error(tw, "forbidden", http.StatusForbidden)
	case errors.Is(serr, errServeDenied):
		delegationMetric.Inc("refused")
		// the same answer for a stranger, a revoked grant and an instance that does not exist; ids only in the log, never a reason to the client
		log.Printf("tenancy: delegated context refused (tenant=%s hub=%s actor=%s)", tenantID, hubID, principal.UserID)
		http.Error(tw, "not found", http.StatusNotFound)
	default:
		delegationMetric.Inc("error")
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

// ErrServeDenied — the same answer for a stranger, a revoked grant and an instance that does not exist.
var ErrServeDenied = errServeDenied

// DelegableKeys — the keys the route in this request's context declared (empty when the route is not delegable). A stream middleware, which cannot run the
// handler inside the admission transaction, reads them to ask the same question AuthorizationMiddleware asks.
func DelegableKeys(ctx context.Context) []string {
	keys, _ := ctx.Value(delegableKey{}).([]string)
	return append([]string(nil), keys...)
}

// AuthorizeServing — the delegated admission of a long-lived stream (ADR-0040 phase 05): the SAME door as a request (lock_served_tenant, then the key asked
// live in the delegated context), but in a short transaction that ends before the stream starts, so no transaction or connection is held while streaming.
// It is asked again every few seconds; a revoked grant, a suspended instance or a lost key ends the stream there. Refused (ErrServeDenied) when the process
// switch is off, when no key is declared, or when none of them is held.
func AuthorizeServing(ctx context.Context, pool *pgxpool.Pool, userID, tenantID, hubID uuid.UUID, keys []string) (*domain.TenantContext, error) {
	if !delegatedServing.Load() || len(keys) == 0 {
		return nil, errServeDenied
	}
	var tc *domain.TenantContext
	err := platformdb.WithTenantSession(ctx, pool, userID, false, func(c context.Context) error {
		resolved, err := resolveServeContext(c, pool, hubID, tenantID, userID)
		if err != nil {
			return err
		}
		for _, key := range keys {
			ok, err := ActorHasPermission(c, platformdb.QuerierFromContext(c, pool), tenantID, userID, key)
			if err != nil {
				return err
			}
			if ok {
				tc = resolved
				return nil
			}
		}
		return errServeForbid
	})
	if errors.Is(err, errServeForbid) {
		return nil, errServeDenied
	}
	return tc, err
}

// ServingConversationVisible — whether the conversation is visible to the person in the delegated context (queue scope of the contract included, by RLS),
// in a short delegated transaction. Used to refuse a conversation stream and to filter the events of the instance stream.
func ServingConversationVisible(ctx context.Context, pool *pgxpool.Pool, userID, tenantID, hubID, conversationID uuid.UUID) (bool, error) {
	if !delegatedServing.Load() {
		return false, nil
	}
	var visible bool
	err := platformdb.WithTenantSession(ctx, pool, userID, false, func(c context.Context) error {
		if _, err := resolveServeContext(c, pool, hubID, tenantID, userID); err != nil {
			return err
		}
		held, err := ActorHasPermission(c, platformdb.QuerierFromContext(c, pool), tenantID, userID, "conversation.read")
		if err != nil || !held {
			return err
		}
		// never a conversation of an internal contact: conversation.read_internal is not delegable (ADR-0018 addendum)
		return platformdb.QuerierFromContext(c, pool).QueryRow(c, `SELECT EXISTS(SELECT 1 FROM conversations cv WHERE cv.tenant_id = $1 AND cv.id = $2
		  AND (NOT EXISTS (SELECT 1 FROM contacts co WHERE co.tenant_id = cv.tenant_id AND co.id = cv.contact_id AND co.kind = 'internal')
		       OR actor_has_permission($1, $3, 'conversation.read_internal')))`, tenantID, conversationID, userID).Scan(&visible)
	})
	if errors.Is(err, errServeDenied) {
		return false, nil
	}
	return visible, err
}

// ServingScopeUnrestricted — whether the contract names no queue list for this person's relationship (every queue of the instance is allowed), read from
// acting_scope() in a short delegated transaction. An instance stream skips its per-event filter only then.
func ServingScopeUnrestricted(ctx context.Context, pool *pgxpool.Pool, userID, tenantID, hubID uuid.UUID) (bool, error) {
	if !delegatedServing.Load() {
		return false, nil
	}
	var open bool
	err := platformdb.WithTenantSession(ctx, pool, userID, false, func(c context.Context) error {
		if _, err := resolveServeContext(c, pool, hubID, tenantID, userID); err != nil {
			return err
		}
		// without the key nothing is unrestricted: the stream filters every event (and its own recheck is about to end it)
		held, err := ActorHasPermission(c, platformdb.QuerierFromContext(c, pool), tenantID, userID, "conversation.read")
		if err != nil || !held {
			return err
		}
		return platformdb.QuerierFromContext(c, pool).QueryRow(c, `SELECT unrestricted FROM acting_scope()`).Scan(&open)
	})
	if errors.Is(err, errServeDenied) {
		return false, nil
	}
	return open, err
}
