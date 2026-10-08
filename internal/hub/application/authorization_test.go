package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/domain"
	"github.com/omnira/omnira/internal/hub/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// fakeRepo implements only what the authorization service reads; any other call panics (nil embedded interface).
type fakeRepo struct {
	ports.HubRepository
	hub        *domain.ServiceHub
	membership *domain.HubMembership
	grant      *domain.EffectiveAccessGrant
	contract   *domain.ServiceContract
	calls      int
}

func (f *fakeRepo) GetServiceHubByID(context.Context, uuid.UUID) (*domain.ServiceHub, error) {
	f.calls++
	return f.hub, nil
}
func (f *fakeRepo) GetHubMembership(context.Context, uuid.UUID, uuid.UUID) (*domain.HubMembership, error) {
	f.calls++
	return f.membership, nil
}
func (f *fakeRepo) GetEffectiveGrant(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.EffectiveAccessGrant, error) {
	f.calls++
	return f.grant, nil
}
func (f *fakeRepo) GetServiceContract(context.Context, uuid.UUID) (*domain.ServiceContract, error) {
	f.calls++
	return f.contract, nil
}

type scenario struct {
	actor, hub, tenant, contract, grant uuid.UUID
	repo                                *fakeRepo
	now                                 time.Time
}

func happy() scenario {
	s := scenario{actor: uuid.New(), hub: uuid.New(), tenant: uuid.New(), contract: uuid.New(), grant: uuid.New(), now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	past := s.now.Add(-24 * time.Hour)
	pool := uuid.New()
	s.repo = &fakeRepo{
		hub:        &domain.ServiceHub{ID: s.hub, Status: "active"},
		membership: &domain.HubMembership{HubID: s.hub, UserID: s.actor},
		grant: &domain.EffectiveAccessGrant{ID: s.grant, HubID: s.hub, UserID: s.actor, TenantID: s.tenant, ServiceContractID: s.contract,
			WorkPoolID: &pool, Status: "active", ValidFrom: past},
		contract: &domain.ServiceContract{ID: s.contract, HubID: s.hub, TenantID: s.tenant, Status: "active", ValidFrom: past, ServiceScope: map[string]interface{}{}},
	}
	return s
}

func (s scenario) resolve(queue *uuid.UUID) (*tenancydomain.TenantContext, error) {
	svc := NewHubAuthorizationService(s.repo)
	svc.now = func() time.Time { return s.now }
	return svc.ResolveHubAccess(context.Background(), HubAccessRequest{ActorID: s.actor, HubID: s.hub, TenantID: s.tenant, QueueID: queue, CorrelationID: "corr-1"})
}

func TestResolveHubAccess_AllowedBuildsHubContext(t *testing.T) {
	s := happy()
	tc, err := s.resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Source != tenancydomain.AccessSourceHub || tc.TenantID != s.tenant || tc.ActorID != s.actor {
		t.Fatalf("wrong context: %+v", tc)
	}
	if tc.HubID == nil || *tc.HubID != s.hub || tc.ServiceContractID == nil || *tc.ServiceContractID != s.contract ||
		tc.EffectiveGrantID == nil || *tc.EffectiveGrantID != s.grant || tc.WorkPoolID == nil || tc.CorrelationID != "corr-1" {
		t.Fatalf("context lost its delegation metadata: %+v", tc)
	}
}

func TestResolveHubAccess_Denials(t *testing.T) {
	q := uuid.New()
	other := uuid.New()
	future := func(s scenario) *time.Time { v := s.now.Add(time.Hour); return &v }
	past := func(s scenario) *time.Time { v := s.now.Add(-time.Hour); return &v }
	cases := []struct {
		name   string
		mutate func(*scenario)
		queue  *uuid.UUID
	}{
		{"hub not visible (forged hub id)", func(s *scenario) { s.repo.hub = nil }, nil},
		{"hub suspended", func(s *scenario) { s.repo.hub.Status = "suspended" }, nil},
		{"not a hub member", func(s *scenario) { s.repo.membership = nil }, nil},
		{"no grant", func(s *scenario) { s.repo.grant = nil }, nil},
		{"grant revoked", func(s *scenario) { s.repo.grant.Status = "revoked" }, nil},
		{"grant suspended", func(s *scenario) { s.repo.grant.Status = "suspended" }, nil},
		{"grant expired", func(s *scenario) { s.repo.grant.ValidUntil = past(*s) }, nil},
		{"grant not started", func(s *scenario) { s.repo.grant.ValidFrom = *future(*s) }, nil},
		{"contract not visible", func(s *scenario) { s.repo.contract = nil }, nil},
		{"contract of another tenant", func(s *scenario) { s.repo.contract.TenantID = uuid.New() }, nil},
		{"contract of another hub", func(s *scenario) { s.repo.contract.HubID = uuid.New() }, nil},
		{"contract revoked", func(s *scenario) { s.repo.contract.Status = "revoked" }, nil},
		{"contract suspended", func(s *scenario) { s.repo.contract.Status = "suspended" }, nil},
		{"contract expired", func(s *scenario) { s.repo.contract.ValidUntil = past(*s) }, nil},
		{"contract not started", func(s *scenario) { s.repo.contract.ValidFrom = *future(*s) }, nil},
		{"queue not on allowlist", func(s *scenario) {
			s.repo.contract.ServiceScope = map[string]interface{}{"queue_ids": []interface{}{other.String()}}
		}, &q},
		{"empty allowlist", func(s *scenario) { s.repo.contract.ServiceScope = map[string]interface{}{"queue_ids": []interface{}{}} }, &q},
		{"restricted contract, resource without queue", func(s *scenario) {
			s.repo.contract.ServiceScope = map[string]interface{}{"queue_ids": []interface{}{q.String()}}
		}, nil},
		{"malformed scope (string)", func(s *scenario) { s.repo.contract.ServiceScope = map[string]interface{}{"queue_ids": "all"} }, &q},
		{"nil scope map (JSON null scanned from the database)", func(s *scenario) { s.repo.contract.ServiceScope = nil }, nil},
		{"malformed scope (null)", func(s *scenario) { s.repo.contract.ServiceScope = map[string]interface{}{"queue_ids": nil} }, &q},
		{"malformed scope (object)", func(s *scenario) {
			s.repo.contract.ServiceScope = map[string]interface{}{"queue_ids": map[string]interface{}{}}
		}, &q},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := happy()
			c.mutate(&s)
			tc, err := s.resolve(c.queue)
			if err == nil || tc != nil {
				t.Fatalf("expected denial, got ctx=%+v err=%v", tc, err)
			}
			if !errors.Is(err, ErrAccessDenied) {
				t.Fatalf("denial must satisfy errors.Is(ErrAccessDenied): %v", err)
			}
			if DenialReason(err) == "" {
				t.Fatal("denial lost its audit reason")
			}
			if err.Error() != "access denied" {
				t.Fatalf("the external message leaks detail: %q", err.Error())
			}
		})
	}
	t.Run("queue on the allowlist is allowed", func(t *testing.T) {
		s := happy()
		s.repo.contract.ServiceScope = map[string]interface{}{"queue_ids": []interface{}{other.String(), q.String()}}
		if _, err := s.resolve(&q); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("grant ending exactly now is expired", func(t *testing.T) {
		s := happy()
		end := s.now
		s.repo.grant.ValidUntil = &end
		if _, err := s.resolve(nil); err == nil {
			t.Fatal("a grant whose valid_until == now must be expired (matches SQL valid_until > now())")
		}
	})
}

func TestResolveHubAccess_RejectsNilIdentifiers(t *testing.T) {
	svc := NewHubAuthorizationService(&fakeRepo{})
	for name, r := range map[string]HubAccessRequest{
		"nil actor":  {HubID: uuid.New(), TenantID: uuid.New()},
		"nil hub":    {ActorID: uuid.New(), TenantID: uuid.New()},
		"nil tenant": {ActorID: uuid.New(), HubID: uuid.New()},
	} {
		if _, err := svc.ResolveHubAccess(context.Background(), r); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: want ErrInvalidRequest, got %v", name, err)
		}
	}
}

type fakeDirect struct {
	calls int
	err   error
}

func (f *fakeDirect) AuthorizeAccessToTenant(_ context.Context, tenantID, actorID uuid.UUID) (*tenancydomain.TenantContext, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return tenancydomain.NewTenantContext(tenantID, actorID, tenancydomain.AccessSourceDirect)
}

func TestEffectiveAccessResolver_NoImplicitFallback(t *testing.T) {
	membershipErr := errors.New("no active membership")

	t.Run("source is mandatory", func(t *testing.T) {
		r := NewEffectiveAccessResolver(&fakeDirect{}, NewHubAuthorizationService(&fakeRepo{}))
		if _, err := r.Resolve(context.Background(), AccessRequest{ActorID: uuid.New(), TenantID: uuid.New()}); !errors.Is(err, ErrAccessSourceRequired) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("direct failure does NOT fall back to the Hub, even when a valid grant exists", func(t *testing.T) {
		s := happy()
		direct := &fakeDirect{err: membershipErr}
		r := NewEffectiveAccessResolver(direct, NewHubAuthorizationService(s.repo))
		_, err := r.Resolve(context.Background(), AccessRequest{Source: tenancydomain.AccessSourceDirect, ActorID: s.actor, TenantID: s.tenant})
		if !errors.Is(err, membershipErr) {
			t.Fatalf("direct error must be returned untouched, got %v", err)
		}
		if s.repo.calls != 0 {
			t.Fatalf("the Hub repository was consulted %d time(s) for a direct request", s.repo.calls)
		}
	})
	t.Run("hub request never consults direct membership", func(t *testing.T) {
		s := happy()
		s.repo.grant = nil // no grant: must be denied even though direct would have been consulted if it fell back
		direct := &fakeDirect{}
		r := NewEffectiveAccessResolver(direct, NewHubAuthorizationService(s.repo))
		_, err := r.Resolve(context.Background(), AccessRequest{Source: tenancydomain.AccessSourceHub, ActorID: s.actor, HubID: s.hub, TenantID: s.tenant})
		if !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("got %v", err)
		}
		if direct.calls != 0 {
			t.Fatal("direct membership was consulted for a hub request")
		}
	})
	t.Run("direct request must not carry a hub id", func(t *testing.T) {
		r := NewEffectiveAccessResolver(&fakeDirect{}, NewHubAuthorizationService(&fakeRepo{}))
		_, err := r.Resolve(context.Background(), AccessRequest{Source: tenancydomain.AccessSourceDirect, ActorID: uuid.New(), TenantID: uuid.New(), HubID: uuid.New()})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("system source is not resolvable for a human request", func(t *testing.T) {
		r := NewEffectiveAccessResolver(&fakeDirect{}, NewHubAuthorizationService(&fakeRepo{}))
		for _, src := range []tenancydomain.AccessSource{tenancydomain.AccessSourceSystem, "root", "admin"} {
			if _, err := r.Resolve(context.Background(), AccessRequest{Source: src, ActorID: uuid.New(), TenantID: uuid.New()}); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("source %q: got %v", src, err)
			}
		}
	})
	t.Run("a successful direct request keeps Source=direct and no hub metadata", func(t *testing.T) {
		r := NewEffectiveAccessResolver(&fakeDirect{}, NewHubAuthorizationService(&fakeRepo{}))
		tc, err := r.Resolve(context.Background(), AccessRequest{Source: tenancydomain.AccessSourceDirect, ActorID: uuid.New(), TenantID: uuid.New(), CorrelationID: "c"})
		if err != nil || tc.Source != tenancydomain.AccessSourceDirect || tc.HubID != nil || tc.EffectiveGrantID != nil || tc.CorrelationID != "c" {
			t.Fatalf("ctx=%+v err=%v", tc, err)
		}
	})
}
