package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tool/domain"
	"github.com/omnira/omnira/internal/tool/ports"
)

// ToolService — aplicação de tools
type ToolService struct {
	toolRepo ports.ToolRepository
	execRepo ports.ExecutionRepository
}

// NewToolService — cria novo ToolService
func NewToolService(toolRepo ports.ToolRepository, execRepo ports.ExecutionRepository) *ToolService {
	return &ToolService{
		toolRepo: toolRepo,
		execRepo: execRepo,
	}
}

// CreateTool — cria nova ferramenta
func (s *ToolService) CreateTool(
	ctx context.Context,
	tenantID, createdBy uuid.UUID,
	name, description string,
	toolType domain.ToolType,
	spec domain.ToolSpec,
) (*domain.Tool, error) {
	// Verificar se tool com mesmo nome já existe
	existing, _ := s.toolRepo.FindByTenantAndName(ctx, tenantID, name)
	if existing != nil {
		return nil, fmt.Errorf("tool already exists: %s", name)
	}

	tool := domain.NewTool(tenantID, createdBy, name, description, toolType, spec)

	if err := s.toolRepo.Store(ctx, tool); err != nil {
		return nil, fmt.Errorf("failed to create tool: %w", err)
	}

	return tool, nil
}

// GetTool — busca tool por ID
func (s *ToolService) GetTool(ctx context.Context, id domain.ToolID) (*domain.Tool, error) {
	tool, err := s.toolRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get tool: %w", err)
	}

	return tool, nil
}

// ListTools — lista tools de um tenant
func (s *ToolService) ListTools(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.Tool, error) {
	tools, err := s.toolRepo.FindByTenant(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	return tools, nil
}

// UpdateTool — atualiza tool
func (s *ToolService) UpdateTool(
	ctx context.Context,
	id domain.ToolID,
	name, description string,
	spec domain.ToolSpec,
	updatedBy uuid.UUID,
) (*domain.Tool, error) {
	tool, err := s.toolRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("tool not found: %w", err)
	}

	tool.Update(name, description, spec, updatedBy)

	if err := s.toolRepo.Update(ctx, tool); err != nil {
		return nil, fmt.Errorf("failed to update tool: %w", err)
	}

	return tool, nil
}

// DeactivateTool — desativa tool
func (s *ToolService) DeactivateTool(ctx context.Context, id domain.ToolID, deactivatedBy uuid.UUID) error {
	tool, err := s.toolRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("tool not found: %w", err)
	}

	tool.Deactivate(deactivatedBy)

	if err := s.toolRepo.Update(ctx, tool); err != nil {
		return fmt.Errorf("failed to deactivate tool: %w", err)
	}

	return nil
}

// RecordExecution — registra execução de uma tool
func (s *ToolService) RecordExecution(
	ctx context.Context,
	tenantID, toolID, requestedBy, correlationID uuid.UUID,
	input map[string]interface{},
) (*domain.ToolExecution, error) {
	// Verificar se tool existe e está ativa
	tool, err := s.toolRepo.FindByID(ctx, toolID)
	if err != nil {
		return nil, fmt.Errorf("tool not found: %w", err)
	}

	if tool.TenantID != tenantID {
		return nil, fmt.Errorf("tool does not belong to tenant")
	}

	if !tool.IsActive() {
		return nil, fmt.Errorf("tool is not active")
	}

	exec := domain.NewExecution(tenantID, toolID, requestedBy, correlationID, input)

	if err := s.execRepo.Store(ctx, exec); err != nil {
		return nil, fmt.Errorf("failed to record execution: %w", err)
	}

	return exec, nil
}

// GetExecution — busca execução por ID
func (s *ToolService) GetExecution(ctx context.Context, id domain.ExecutionID) (*domain.ToolExecution, error) {
	exec, err := s.execRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get execution: %w", err)
	}

	return exec, nil
}

// ListExecutions — lista execuções de um tenant
func (s *ToolService) ListExecutions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.ToolExecution, error) {
	execs, err := s.execRepo.FindByTenant(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list executions: %w", err)
	}

	return execs, nil
}

// UpdateExecution — atualiza execução (resultado final)
func (s *ToolService) UpdateExecution(ctx context.Context, exec *domain.ToolExecution) error {
	if err := s.execRepo.Update(ctx, exec); err != nil {
		return fmt.Errorf("failed to update execution: %w", err)
	}

	return nil
}
