package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/rbac/domain"
)

// RoleRepository — persistência de roles
type RoleRepository interface {
	// FindByID — busca role por ID
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Role, error)

	// FindByTenantAndName — busca role customizado por tenant + nome
	FindByTenantAndName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Role, error)

	// FindByTenant — lista todos os roles de um tenant
	FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.Role, error)

	// Store — armazena novo role
	Store(ctx context.Context, role *domain.Role) error

	// Update — atualiza role existente
	Update(ctx context.Context, role *domain.Role) error

	// Delete — deleta role
	Delete(ctx context.Context, id uuid.UUID) error
}
