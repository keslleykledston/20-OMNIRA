package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/rbac/domain"
	"github.com/omnira/omnira/internal/rbac/ports"
)

// RBACService — gerencia roles e permissões
type RBACService struct {
	roleRepo ports.RoleRepository
}

// NewRBACService — cria novo RBAC service
func NewRBACService(roleRepo ports.RoleRepository) *RBACService {
	return &RBACService{
		roleRepo: roleRepo,
	}
}

// GetRole — obtém role por ID
func (s *RBACService) GetRole(ctx context.Context, roleID uuid.UUID) (*domain.Role, error) {
	return s.roleRepo.FindByID(ctx, roleID)
}

// GetSystemRole — obtém role de sistema por nome
func (s *RBACService) GetSystemRole(ctx context.Context, roleName string) (*domain.Role, error) {
	if role, ok := domain.SystemRoles[roleName]; ok {
		return role, nil
	}
	return nil, fmt.Errorf("system role not found: %s", roleName)
}

// GetTenantRole — obtém role customizado de tenant
func (s *RBACService) GetTenantRole(ctx context.Context, tenantID uuid.UUID, roleName string) (*domain.Role, error) {
	return s.roleRepo.FindByTenantAndName(ctx, tenantID, roleName)
}

// ListTenantRoles — lista todos os roles de um tenant
func (s *RBACService) ListTenantRoles(ctx context.Context, tenantID uuid.UUID) ([]*domain.Role, error) {
	return s.roleRepo.FindByTenant(ctx, tenantID)
}

// CreateTenantRole — cria novo role customizado
func (s *RBACService) CreateTenantRole(
	ctx context.Context,
	tenantID uuid.UUID,
	name string,
	description string,
	permissions []*domain.Permission,
) (*domain.Role, error) {
	// Validar nome único dentro do tenant
	existing, _ := s.roleRepo.FindByTenantAndName(ctx, tenantID, name)
	if existing != nil {
		return nil, fmt.Errorf("role name already exists: %s", name)
	}

	role := domain.NewTenantRole(tenantID, name, description, permissions)

	if err := s.roleRepo.Store(ctx, role); err != nil {
		return nil, fmt.Errorf("failed to create role: %w", err)
	}

	return role, nil
}

// UpdateRolePermissions — atualiza permissões de um role
func (s *RBACService) UpdateRolePermissions(
	ctx context.Context,
	roleID uuid.UUID,
	permissions []*domain.Permission,
) (*domain.Role, error) {
	role, err := s.roleRepo.FindByID(ctx, roleID)
	if err != nil {
		return nil, fmt.Errorf("role not found: %w", err)
	}

	if role.IsSystem {
		return nil, fmt.Errorf("cannot modify system roles")
	}

	role.Permissions = permissions

	if err := s.roleRepo.Update(ctx, role); err != nil {
		return nil, fmt.Errorf("failed to update role: %w", err)
	}

	return role, nil
}

// DeleteTenantRole — deleta um role customizado
func (s *RBACService) DeleteTenantRole(ctx context.Context, roleID uuid.UUID) error {
	role, err := s.roleRepo.FindByID(ctx, roleID)
	if err != nil {
		return fmt.Errorf("role not found: %w", err)
	}

	if role.IsSystem {
		return fmt.Errorf("cannot delete system roles")
	}

	if err := s.roleRepo.Delete(ctx, roleID); err != nil {
		return fmt.Errorf("failed to delete role: %w", err)
	}

	return nil
}

// CheckPermission — verifica se user (via role) tem permissão
func (s *RBACService) CheckPermission(
	role *domain.Role,
	resource domain.PermissionResource,
	action domain.PermissionAction,
) bool {
	if role == nil {
		return false
	}

	return role.HasPermission(resource, action)
}

// CanRead — shorthand check for read permission
func (s *RBACService) CanRead(role *domain.Role, resource domain.PermissionResource) bool {
	return s.CheckPermission(role, resource, domain.ActionRead)
}

// CanWrite — shorthand check for write permission
func (s *RBACService) CanWrite(role *domain.Role, resource domain.PermissionResource) bool {
	return s.CheckPermission(role, resource, domain.ActionWrite)
}

// CanAdmin — shorthand check for admin permission
func (s *RBACService) CanAdmin(role *domain.Role, resource domain.PermissionResource) bool {
	return s.CheckPermission(role, resource, domain.ActionAdmin)
}
