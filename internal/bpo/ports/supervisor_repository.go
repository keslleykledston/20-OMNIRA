package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
)

// SupervisorRoleRepository — interface para operações com supervisor roles
type SupervisorRoleRepository interface {
	// Store — salva nova role
	Store(ctx context.Context, role *domain.SupervisorRole) error

	// FindByID — busca role por ID
	FindByID(ctx context.Context, id uuid.UUID) (*domain.SupervisorRole, error)

	// FindByTenant — lista roles de um tenant
	FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.SupervisorRole, error)

	// Update — atualiza role
	Update(ctx context.Context, role *domain.SupervisorRole) error

	// Delete — deleta role
	Delete(ctx context.Context, id uuid.UUID) error
}

// SupervisorAssignmentRepository — interface para operações com supervisor assignments
type SupervisorAssignmentRepository interface {
	// Store — salva nova atribuição
	Store(ctx context.Context, assignment *domain.SupervisorAssignment) error

	// FindByID — busca atribuição por ID
	FindByID(ctx context.Context, id uuid.UUID) (*domain.SupervisorAssignment, error)

	// FindByUser — lista atribuições de um usuário
	FindByUser(ctx context.Context, userID uuid.UUID) ([]*domain.SupervisorAssignment, error)

	// FindByTenant — lista atribuições de um tenant
	FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.SupervisorAssignment, error)

	// Update — atualiza atribuição
	Update(ctx context.Context, assignment *domain.SupervisorAssignment) error

	// Delete — deleta atribuição
	Delete(ctx context.Context, id uuid.UUID) error
}
