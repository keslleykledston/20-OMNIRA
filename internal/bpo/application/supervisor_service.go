package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
	"github.com/omnira/omnira/internal/bpo/ports"
)

// SupervisorService — serviço para supervisão de operações BPO
type SupervisorService struct {
	roleRepo       ports.SupervisorRoleRepository
	assignmentRepo ports.SupervisorAssignmentRepository
}

// NewSupervisorService — cria novo SupervisorService
func NewSupervisorService(roleRepo ports.SupervisorRoleRepository, assignmentRepo ports.SupervisorAssignmentRepository) *SupervisorService {
	return &SupervisorService{
		roleRepo:       roleRepo,
		assignmentRepo: assignmentRepo,
	}
}

// CreateSupervisorRole — cria nova role de supervisor
func (s *SupervisorService) CreateSupervisorRole(
	ctx context.Context,
	tenantID, createdBy uuid.UUID,
	name, description string,
	permissions []string,
) (*domain.SupervisorRole, error) {
	role := domain.NewSupervisorRole(tenantID, createdBy, name, description, permissions)

	if err := s.roleRepo.Store(ctx, role); err != nil {
		return nil, fmt.Errorf("failed to create role: %w", err)
	}

	return role, nil
}

// GetSupervisorRole — obtém role por ID
func (s *SupervisorService) GetSupervisorRole(ctx context.Context, id uuid.UUID) (*domain.SupervisorRole, error) {
	role, err := s.roleRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get role: %w", err)
	}
	return role, nil
}

// UpdateSupervisorRole — atualiza role
func (s *SupervisorService) UpdateSupervisorRole(ctx context.Context, role *domain.SupervisorRole) error {
	if err := s.roleRepo.Update(ctx, role); err != nil {
		return fmt.Errorf("failed to update role: %w", err)
	}
	return nil
}

// AssignSupervisor — atribui supervisor a um usuário
func (s *SupervisorService) AssignSupervisor(
	ctx context.Context,
	tenantID, userID, roleID, assignedBy uuid.UUID,
	accountIDs []uuid.UUID,
) (*domain.SupervisorAssignment, error) {
	// Validar role existe
	role, err := s.roleRepo.FindByID(ctx, roleID)
	if err != nil {
		return nil, fmt.Errorf("role not found: %w", err)
	}

	if role == nil {
		return nil, fmt.Errorf("role not found")
	}

	assignment := domain.NewSupervisorAssignment(tenantID, userID, roleID, assignedBy, accountIDs)

	if err := s.assignmentRepo.Store(ctx, assignment); err != nil {
		return nil, fmt.Errorf("failed to assign supervisor: %w", err)
	}

	return assignment, nil
}

// GetSupervisorAssignment — obtém atribuição por ID
func (s *SupervisorService) GetSupervisorAssignment(ctx context.Context, id uuid.UUID) (*domain.SupervisorAssignment, error) {
	assignment, err := s.assignmentRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get assignment: %w", err)
	}
	return assignment, nil
}

// GetUserAssignments — obtém todas as atribuições de um usuário
func (s *SupervisorService) GetUserAssignments(ctx context.Context, userID uuid.UUID) ([]*domain.SupervisorAssignment, error) {
	assignments, err := s.assignmentRepo.FindByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get assignments: %w", err)
	}
	return assignments, nil
}

// CanUserSuperviseAccount — verifica se usuário pode supervisionar uma account
func (s *SupervisorService) CanUserSuperviseAccount(ctx context.Context, userID, accountID uuid.UUID) (bool, error) {
	assignments, err := s.assignmentRepo.FindByUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("failed to check permissions: %w", err)
	}

	for _, assignment := range assignments {
		if assignment.CanSuperviseAccount(accountID) {
			return true, nil
		}
	}

	return false, nil
}

// RevokeAssignment — revoga atribuição de supervisor
func (s *SupervisorService) RevokeAssignment(ctx context.Context, id uuid.UUID) error {
	assignment, err := s.assignmentRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("assignment not found: %w", err)
	}

	assignment.Status = "inactive"

	if err := s.assignmentRepo.Update(ctx, assignment); err != nil {
		return fmt.Errorf("failed to revoke assignment: %w", err)
	}

	return nil
}

// AddAccountToAssignment — adiciona account à atribuição
func (s *SupervisorService) AddAccountToAssignment(ctx context.Context, assignmentID, accountID uuid.UUID) error {
	assignment, err := s.assignmentRepo.FindByID(ctx, assignmentID)
	if err != nil {
		return fmt.Errorf("assignment not found: %w", err)
	}

	assignment.AddAccount(accountID)

	if err := s.assignmentRepo.Update(ctx, assignment); err != nil {
		return fmt.Errorf("failed to add account: %w", err)
	}

	return nil
}

// RemoveAccountFromAssignment — remove account da atribuição
func (s *SupervisorService) RemoveAccountFromAssignment(ctx context.Context, assignmentID, accountID uuid.UUID) error {
	assignment, err := s.assignmentRepo.FindByID(ctx, assignmentID)
	if err != nil {
		return fmt.Errorf("assignment not found: %w", err)
	}

	assignment.RemoveAccount(accountID)

	if err := s.assignmentRepo.Update(ctx, assignment); err != nil {
		return fmt.Errorf("failed to remove account: %w", err)
	}

	return nil
}
