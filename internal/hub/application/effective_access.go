package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// DirectAuthorizer is the existing tenancy AuthorizationService (active membership in the tenant).
type DirectAuthorizer interface {
	AuthorizeAccessToTenant(ctx context.Context, tenantID, actorID uuid.UUID) (*tenancydomain.TenantContext, error)
}

// AccessRequest says HOW the caller wants to act. Source is mandatory.
type AccessRequest struct {
	Source        tenancydomain.AccessSource // AccessSourceDirect or AccessSourceHub
	ActorID       uuid.UUID
	TenantID      uuid.UUID
	HubID         uuid.UUID // only for hub access
	QueueID       *uuid.UUID
	CorrelationID string
}

// EffectiveAccessResolver is the single place that turns a request into an EffectiveTenantContext.
//
//	Direct -> tenant membership only. It NEVER tries the Hub if membership fails.
//	Hub    -> hub membership + grant + contract + scope only. It NEVER tries direct membership.
//	System -> not resolvable here; non-human work uses platformdb.WithSystemTenantSession explicitly.
type EffectiveAccessResolver struct {
	direct DirectAuthorizer
	hub    *HubAuthorizationService
}

func NewEffectiveAccessResolver(direct DirectAuthorizer, hub *HubAuthorizationService) *EffectiveAccessResolver {
	return &EffectiveAccessResolver{direct: direct, hub: hub}
}

func (r *EffectiveAccessResolver) Resolve(ctx context.Context, req AccessRequest) (*tenancydomain.TenantContext, error) {
	switch req.Source {
	case "":
		return nil, ErrAccessSourceRequired
	case tenancydomain.AccessSourceDirect:
		if req.HubID != uuid.Nil {
			return nil, fmt.Errorf("%w: direct access must not carry a hub id", ErrInvalidRequest)
		}
		tc, err := r.direct.AuthorizeAccessToTenant(ctx, req.TenantID, req.ActorID)
		if err != nil {
			return nil, err // no fallback to the Hub, ever
		}
		tc.CorrelationID = req.CorrelationID
		return tc, nil
	case tenancydomain.AccessSourceHub:
		return r.hub.ResolveHubAccess(ctx, HubAccessRequest{
			ActorID: req.ActorID, HubID: req.HubID, TenantID: req.TenantID, QueueID: req.QueueID, CorrelationID: req.CorrelationID,
		})
	default:
		return nil, fmt.Errorf("%w: access source %q cannot be resolved for a human request", ErrInvalidRequest, req.Source)
	}
}
