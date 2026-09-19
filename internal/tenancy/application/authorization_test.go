package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// MockMembershipRepo — mock para testes.
type MockMembershipRepo struct {
	memberships map[uuid.UUID]*domain.Membership
}

func (m *MockMembershipRepo) Store(ctx context.Context, membership *domain.Membership) error {
	m.memberships[membership.ID] = membership
	return nil
}

func (m *MockMembershipRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Membership, error) {
	return m.memberships[id], nil
}

func (m *MockMembershipRepo) FindByTenantAndUser(ctx context.Context, tenantID, userID uuid.UUID) ([]*domain.Membership, error) {
	var result []*domain.Membership
	for _, mem := range m.memberships {
		if mem.TenantID == tenantID && mem.UserID == userID {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *MockMembershipRepo) FindByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Membership, error) {
	var result []*domain.Membership
	for _, mem := range m.memberships {
		if mem.UserID == userID && mem.IsActive() {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *MockMembershipRepo) FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.Membership, error) {
	var result []*domain.Membership
	for _, mem := range m.memberships {
		if mem.TenantID == tenantID {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *MockMembershipRepo) Update(ctx context.Context, membership *domain.Membership) error {
	m.memberships[membership.ID] = membership
	return nil
}

func (m *MockMembershipRepo) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.memberships, id)
	return nil
}

// MockTenantRepo — mock para testes.
type MockTenantRepo struct {
	tenants map[uuid.UUID]*domain.Tenant
}

func (m *MockTenantRepo) Store(ctx context.Context, tenant *domain.Tenant) error {
	m.tenants[tenant.ID] = tenant
	return nil
}

func (m *MockTenantRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	return m.tenants[id], nil
}

func (m *MockTenantRepo) FindAll(ctx context.Context, limit, offset int) ([]*domain.Tenant, error) {
	var result []*domain.Tenant
	for _, t := range m.tenants {
		result = append(result, t)
	}
	return result, nil
}

func (m *MockTenantRepo) Update(ctx context.Context, tenant *domain.Tenant) error {
	m.tenants[tenant.ID] = tenant
	return nil
}

func TestAuthorizeAccessToTenant_Success(t *testing.T) {
	ctx := context.Background()
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	authzSvc := NewAuthorizationService(memberRepo, tenantRepo)

	// Setup: create tenant and membership
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()

	tenant, _ := domain.NewTenant("Company A", domain.IsolationSharedStrong)
	tenant.ID = tenantID
	tenantRepo.Store(ctx, tenant)

	membership, _ := domain.NewMembership(tenantID, userID, roleID)
	memberRepo.Store(ctx, membership)

	// Test: authorized access
	tenantContext, err := authzSvc.AuthorizeAccessToTenant(ctx, tenantID, userID)
	if err != nil {
		t.Fatalf("expected authorized access, got error: %v", err)
	}
	if tenantContext.TenantID != tenantID {
		t.Errorf("expected tenant_id %s, got %s", tenantID, tenantContext.TenantID)
	}
	if tenantContext.ActorID != userID {
		t.Errorf("expected actor_id %s, got %s", userID, tenantContext.ActorID)
	}
}

func TestAuthorizeAccessToTenant_NoMembership(t *testing.T) {
	ctx := context.Background()
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	authzSvc := NewAuthorizationService(memberRepo, tenantRepo)

	// Setup: create tenant but NO membership
	tenantID := uuid.New()
	userID := uuid.New()

	tenant, _ := domain.NewTenant("Company A", domain.IsolationSharedStrong)
	tenant.ID = tenantID
	tenantRepo.Store(ctx, tenant)

	// Test: denied access (no membership)
	_, err := authzSvc.AuthorizeAccessToTenant(ctx, tenantID, userID)
	if err == nil {
		t.Error("expected error for user without membership")
	}
}

func TestAuthorizeAccessToTenant_InactiveMembership(t *testing.T) {
	ctx := context.Background()
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	authzSvc := NewAuthorizationService(memberRepo, tenantRepo)

	// Setup: create tenant and INACTIVE membership
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()

	tenant, _ := domain.NewTenant("Company A", domain.IsolationSharedStrong)
	tenant.ID = tenantID
	tenantRepo.Store(ctx, tenant)

	membership, _ := domain.NewMembership(tenantID, userID, roleID)
	membership.Deactivate()
	memberRepo.Store(ctx, membership)

	// Test: denied access (membership inactive)
	_, err := authzSvc.AuthorizeAccessToTenant(ctx, tenantID, userID)
	if err == nil {
		t.Error("expected error for user with inactive membership")
	}
}

func TestAuthorizeAccessToTenant_InactiveTenant(t *testing.T) {
	ctx := context.Background()
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	authzSvc := NewAuthorizationService(memberRepo, tenantRepo)

	// Setup: create INACTIVE tenant and membership
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()

	tenant, _ := domain.NewTenant("Company A", domain.IsolationSharedStrong)
	tenant.ID = tenantID
	tenant.Deactivate()
	tenantRepo.Store(ctx, tenant)

	membership, _ := domain.NewMembership(tenantID, userID, roleID)
	memberRepo.Store(ctx, membership)

	// Test: denied access (tenant inactive)
	_, err := authzSvc.AuthorizeAccessToTenant(ctx, tenantID, userID)
	if err == nil {
		t.Error("expected error for inactive tenant")
	}
}

func TestIsAuthorized(t *testing.T) {
	ctx := context.Background()
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	authzSvc := NewAuthorizationService(memberRepo, tenantRepo)

	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()

	tenant, _ := domain.NewTenant("Company A", domain.IsolationSharedStrong)
	tenant.ID = tenantID
	tenantRepo.Store(ctx, tenant)

	membership, _ := domain.NewMembership(tenantID, userID, roleID)
	memberRepo.Store(ctx, membership)

	// Test: IsAuthorized returns true
	if !authzSvc.IsAuthorized(ctx, tenantID, userID) {
		t.Error("expected IsAuthorized to return true")
	}

	// Test: IsAuthorized returns false for different user
	if authzSvc.IsAuthorized(ctx, tenantID, uuid.New()) {
		t.Error("expected IsAuthorized to return false for different user")
	}
}
