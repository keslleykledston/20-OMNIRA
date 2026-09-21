package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/rbac/domain"
)

// MockRoleRepository — mock para testes
type MockRoleRepository struct {
	roles map[uuid.UUID]*domain.Role
}

func NewMockRoleRepository() *MockRoleRepository {
	return &MockRoleRepository{
		roles: make(map[uuid.UUID]*domain.Role),
	}
}

func (m *MockRoleRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Role, error) {
	return m.roles[id], nil
}

func (m *MockRoleRepository) FindByTenantAndName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Role, error) {
	for _, role := range m.roles {
		if role.TenantID == tenantID && role.Name == name {
			return role, nil
		}
	}
	return nil, nil
}

func (m *MockRoleRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.Role, error) {
	var result []*domain.Role
	for _, role := range m.roles {
		if role.TenantID == tenantID {
			result = append(result, role)
		}
	}
	return result, nil
}

func (m *MockRoleRepository) Store(ctx context.Context, role *domain.Role) error {
	m.roles[role.ID] = role
	return nil
}

func (m *MockRoleRepository) Update(ctx context.Context, role *domain.Role) error {
	m.roles[role.ID] = role
	return nil
}

func (m *MockRoleRepository) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.roles, id)
	return nil
}

func TestGetSystemRole(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	role, err := svc.GetSystemRole(context.Background(), "tenant_admin")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if role.Name != "tenant_admin" {
		t.Errorf("expected role name 'tenant_admin', got %s", role.Name)
	}

	if !role.IsSystem {
		t.Errorf("expected system role")
	}
}

func TestGetSystemRole_NotFound(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	_, err := svc.GetSystemRole(context.Background(), "nonexistent")
	if err == nil {
		t.Errorf("expected error for nonexistent role")
	}
}

func TestCreateTenantRole(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	tenantID := uuid.New()
	perms := []*domain.Permission{
		{Resource: domain.ResourceTenant, Action: domain.ActionRead},
	}

	role, err := svc.CreateTenantRole(context.Background(), tenantID, "custom", "Custom role", perms)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if role.Name != "custom" {
		t.Errorf("expected role name 'custom'")
	}

	if role.IsSystem {
		t.Errorf("tenant role should not be marked as system")
	}
}

func TestCreateTenantRole_Duplicate(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	tenantID := uuid.New()
	perms := []*domain.Permission{}

	// Create first role
	_, err := svc.CreateTenantRole(context.Background(), tenantID, "custom", "Custom role", perms)
	if err != nil {
		t.Fatalf("expected no error on first create, got %v", err)
	}

	// Try to create duplicate
	_, err = svc.CreateTenantRole(context.Background(), tenantID, "custom", "Another role", perms)
	if err == nil {
		t.Errorf("expected error for duplicate role name")
	}
}

func TestCheckPermission(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	adminRole := domain.SystemRoles["tenant_admin"]

	if !svc.CheckPermission(adminRole, domain.ResourceMembership, domain.ActionAdmin) {
		t.Errorf("admin should have admin permission on membership")
	}

	viewerRole := domain.SystemRoles["tenant_agent"]
	if svc.CheckPermission(viewerRole, domain.ResourceMembership, domain.ActionWrite) {
		t.Errorf("viewer should not have write permission")
	}
}

func TestCanRead_CanWrite_CanAdmin(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	adminRole := domain.SystemRoles["tenant_admin"]

	if !svc.CanRead(adminRole, domain.ResourceTenant) {
		t.Errorf("admin should be able to read tenant")
	}

	if !svc.CanAdmin(adminRole, domain.ResourceMembership) {
		t.Errorf("admin should be able to admin membership")
	}

	emptyRole := &domain.Role{}
	if svc.CanRead(emptyRole, domain.ResourceTenant) {
		t.Errorf("role without permissions should not be able to read tenant")
	}
}

func TestUpdateRolePermissions(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	tenantID := uuid.New()
	perms := []*domain.Permission{
		{Resource: domain.ResourceTenant, Action: domain.ActionRead},
	}

	role, _ := svc.CreateTenantRole(context.Background(), tenantID, "custom", "Custom", perms)

	newPerms := []*domain.Permission{
		{Resource: domain.ResourceTenant, Action: domain.ActionAdmin},
		{Resource: domain.ResourceMembership, Action: domain.ActionRead},
	}

	updated, err := svc.UpdateRolePermissions(context.Background(), role.ID, newPerms)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !svc.CanAdmin(updated, domain.ResourceTenant) {
		t.Errorf("updated role should have admin permission on tenant")
	}
}

func TestUpdateRolePermissions_SystemRole(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	adminRole := domain.SystemRoles["tenant_admin"]
	newPerms := []*domain.Permission{}

	// Store admin role so it can be found
	mockRepo.Store(context.Background(), adminRole)

	_, err := svc.UpdateRolePermissions(context.Background(), adminRole.ID, newPerms)
	if err == nil {
		t.Errorf("expected error when updating system role")
	}
}

func TestDeleteTenantRole(t *testing.T) {
	mockRepo := NewMockRoleRepository()
	svc := NewRBACService(mockRepo)

	tenantID := uuid.New()
	role, _ := svc.CreateTenantRole(context.Background(), tenantID, "custom", "Custom", []*domain.Permission{})

	err := svc.DeleteTenantRole(context.Background(), role.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify deletion
	retrieved, _ := mockRepo.FindByID(context.Background(), role.ID)
	if retrieved != nil {
		t.Errorf("expected role to be deleted")
	}
}
