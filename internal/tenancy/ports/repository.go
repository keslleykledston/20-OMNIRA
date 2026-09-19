package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// TenantRepository — persistência de Tenants.
type TenantRepository interface {
	// Store — salva um novo Tenant.
	Store(ctx context.Context, tenant *domain.Tenant) error

	// FindByID — busca um Tenant pelo ID.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)

	// FindAll — lista todos os Tenants (com paginação).
	FindAll(ctx context.Context, limit, offset int) ([]*domain.Tenant, error)

	// Update — atualiza um Tenant existente.
	Update(ctx context.Context, tenant *domain.Tenant) error
}

// MembershipRepository — persistência de Memberships.
type MembershipRepository interface {
	// Store — salva uma nova Membership.
	Store(ctx context.Context, membership *domain.Membership) error

	// FindByID — busca uma Membership pelo ID.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Membership, error)

	// FindByTenantAndUser — busca memberships de um usuário num tenant.
	FindByTenantAndUser(ctx context.Context, tenantID, userID uuid.UUID) ([]*domain.Membership, error)

	// FindByTenant — lista todas as memberships de um tenant.
	FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.Membership, error)

	// Update — atualiza uma Membership existente.
	Update(ctx context.Context, membership *domain.Membership) error

	// Delete — remove uma Membership (hard delete).
	Delete(ctx context.Context, id uuid.UUID) error
}

// RoleRepository — persistência de Roles.
type RoleRepository interface {
	// FindByKey — busca uma Role pelo key (tenant_admin, etc).
	FindByKey(ctx context.Context, key string) (*domain.Role, error)

	// FindByID — busca uma Role pelo ID.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Role, error)

	// FindAll — lista todas as Roles (system + tenant-scoped).
	FindAll(ctx context.Context) ([]*domain.Role, error)
}
