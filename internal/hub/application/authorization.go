package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

var (
	// ErrAccessDenied is the ONLY denial a caller should map to an HTTP response. Every internal reason
	// wraps it, so a forged hub id, a missing grant and an expired contract are indistinguishable outside.
	ErrAccessDenied = errors.New("access denied")
	// ErrInvalidRequest is a malformed request (nil ids, direct request carrying a hub id, ...).
	ErrInvalidRequest = errors.New("invalid access request")
	// ErrAccessSourceRequired: the caller must say whether this is direct or hub access. It is never guessed.
	ErrAccessSourceRequired = errors.New("access source is required (direct or hub)")
)

// Denial is an ErrAccessDenied that remembers why, for audit logs only.
type Denial struct{ reason string }

func (d *Denial) Error() string        { return ErrAccessDenied.Error() }
func (d *Denial) Is(target error) bool { return target == ErrAccessDenied }

// Reason is for server-side logging/audit. Never put it in a response.
func (d *Denial) Reason() string { return d.reason }

func deny(reason string) error { return &Denial{reason: reason} }

// DenialReason extracts the audit reason from an error returned by this package ("" if it is not a denial).
func DenialReason(err error) string {
	var d *Denial
	if errors.As(err, &d) {
		return d.reason
	}
	return ""
}

// HubAuthorizationService is the application barrier for delegated (Hub) access. PostgreSQL RLS is the
// second, independent barrier (migrations 094/095): both must agree, and neither trusts the request.
//
// Its repository reads MUST run inside the actor's database session (platformdb.WithTenantSession with
// the real actor id, isSystemAdmin=false). Then RLS filters what the actor may even look up: a hub, a
// grant or a contract they cannot see simply does not exist for them.
type HubAuthorizationService struct {
	repo ports.HubRepository
	now  func() time.Time
}

func NewHubAuthorizationService(repo ports.HubRepository) *HubAuthorizationService {
	return &HubAuthorizationService{repo: repo, now: time.Now}
}

// HubAccessRequest carries identifiers that are CLAIMS, not authority: tenant and hub come from the
// request path/body and are checked against persisted grants.
type HubAccessRequest struct {
	ActorID       uuid.UUID
	HubID         uuid.UUID
	TenantID      uuid.UUID
	QueueID       *uuid.UUID // queue of the resource being opened, when it has one
	CorrelationID string
}

// ResolveHubAccess validates hub status, hub membership, grant, contract and queue scope, and only then
// builds the EffectiveTenantContext (Source=hub). It never falls back to system admin or direct membership.
func (s *HubAuthorizationService) ResolveHubAccess(ctx context.Context, req HubAccessRequest) (*tenancydomain.TenantContext, error) {
	if req.ActorID == uuid.Nil || req.HubID == uuid.Nil || req.TenantID == uuid.Nil {
		return nil, fmt.Errorf("%w: actor, hub and tenant ids are required", ErrInvalidRequest)
	}
	now := s.now()

	hub, err := s.repo.GetServiceHubByID(ctx, req.HubID)
	if err != nil {
		return nil, fmt.Errorf("load hub: %w", err)
	}
	if hub == nil {
		return nil, deny("hub not visible to actor")
	}
	if hub.Status != "active" {
		return nil, deny("hub is " + hub.Status)
	}

	membership, err := s.repo.GetHubMembership(ctx, req.HubID, req.ActorID)
	if err != nil {
		return nil, fmt.Errorf("load hub membership: %w", err)
	}
	if membership == nil {
		return nil, deny("actor is not a member of the hub")
	}

	grant, err := s.repo.GetEffectiveGrant(ctx, req.HubID, req.ActorID, req.TenantID)
	if err != nil {
		return nil, fmt.Errorf("load grant: %w", err)
	}
	if grant == nil {
		return nil, deny("no active grant for this tenant")
	}
	if grant.Status != "active" || now.Before(grant.ValidFrom) || (grant.ValidUntil != nil && !now.Before(*grant.ValidUntil)) {
		return nil, deny("grant outside its validity window")
	}

	contract, err := s.repo.GetServiceContract(ctx, grant.ServiceContractID)
	if err != nil {
		return nil, fmt.Errorf("load contract: %w", err)
	}
	if contract == nil {
		return nil, deny("contract not visible to actor")
	}
	if contract.HubID != req.HubID || contract.TenantID != req.TenantID {
		return nil, deny("contract does not belong to this hub and tenant")
	}
	if contract.Status != "active" || now.Before(contract.ValidFrom) || (contract.ValidUntil != nil && !now.Before(*contract.ValidUntil)) {
		return nil, deny("contract inactive or outside its validity window")
	}
	if !queueAllowed(contract.ServiceScope, req.QueueID) {
		return nil, deny("queue outside the contract scope")
	}

	tc, err := tenancydomain.NewHubTenantContext(req.TenantID, req.ActorID, req.HubID, contract.ID, grant.ID, req.CorrelationID)
	if err != nil {
		return nil, fmt.Errorf("build effective context: %w", err)
	}
	tc.WorkPoolID = grant.WorkPoolID
	return tc, nil
}

// queueAllowed mirrors has_active_hub_access() in SQL exactly: no queue_ids key = every queue; a present
// key is an allowlist (empty = none); a resource without a queue is not covered by a restricted contract;
// a malformed value denies.
func queueAllowed(scope map[string]interface{}, queue *uuid.UUID) bool {
	if scope == nil { // JSON null, or a row that was not loaded: never read as "unrestricted"
		return false
	}
	raw, present := scope["queue_ids"]
	if !present {
		return true
	}
	list, ok := raw.([]interface{}) // JSON null, string, number and object all land here and deny
	if !ok {
		return false
	}
	if queue == nil {
		return false
	}
	for _, v := range list {
		if s, ok := v.(string); ok && s == queue.String() {
			return true
		}
	}
	return false
}

// AuthorizeHubMember checks only that the actor belongs to an active hub (enough to LIST a hub-level
// resource such as the inbox). What the actor may see inside it is decided per tenant by RLS and by
// ResolveHubAccess when a specific item is opened.
func (s *HubAuthorizationService) AuthorizeHubMember(ctx context.Context, actorID, hubID uuid.UUID) error {
	if actorID == uuid.Nil || hubID == uuid.Nil {
		return fmt.Errorf("%w: actor and hub ids are required", ErrInvalidRequest)
	}
	hub, err := s.repo.GetServiceHubByID(ctx, hubID)
	if err != nil {
		return fmt.Errorf("load hub: %w", err)
	}
	if hub == nil {
		return deny("hub not visible to actor")
	}
	if hub.Status != "active" {
		return deny("hub is " + hub.Status)
	}
	membership, err := s.repo.GetHubMembership(ctx, hubID, actorID)
	if err != nil {
		return fmt.Errorf("load hub membership: %w", err)
	}
	if membership == nil {
		return deny("actor is not a member of the hub")
	}
	return nil
}
