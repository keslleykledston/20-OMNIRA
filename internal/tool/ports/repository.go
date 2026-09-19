package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tool/domain"
)

// ToolRepository — interface para operações com tools
type ToolRepository interface {
	// Store — salva nova tool
	Store(ctx context.Context, tool *domain.Tool) error

	// FindByID — busca tool por ID
	FindByID(ctx context.Context, id domain.ToolID) (*domain.Tool, error)

	// FindByTenant — lista tools de um tenant
	FindByTenant(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]*domain.Tool, error)

	// FindByTenantAndName — busca tool por tenant e nome
	FindByTenantAndName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Tool, error)

	// Update — atualiza tool
	Update(ctx context.Context, tool *domain.Tool) error

	// Delete — deleta tool
	Delete(ctx context.Context, id domain.ToolID) error

	// CountByTenant — conta tools de um tenant
	CountByTenant(ctx context.Context, tenantID uuid.UUID) (int, error)
}

// ExecutionRepository — interface para operações com execuções
type ExecutionRepository interface {
	// Store — salva nova execução
	Store(ctx context.Context, exec *domain.ToolExecution) error

	// FindByID — busca execução por ID
	FindByID(ctx context.Context, id domain.ExecutionID) (*domain.ToolExecution, error)

	// FindByTool — lista execuções de uma tool
	FindByTool(ctx context.Context, toolID domain.ToolID, limit int, offset int) ([]*domain.ToolExecution, error)

	// FindByTenant — lista execuções de um tenant
	FindByTenant(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]*domain.ToolExecution, error)

	// Update — atualiza execução
	Update(ctx context.Context, exec *domain.ToolExecution) error

	// CountByStatus — conta execuções por status
	CountByStatus(ctx context.Context, tenantID uuid.UUID, status domain.ExecutionStatus) (int, error)
}
