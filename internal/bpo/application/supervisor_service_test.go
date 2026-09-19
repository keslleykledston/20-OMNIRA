package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
)

// Tests

func TestCreateSupervisorRole(t *testing.T) {
	roleRepo := NewMockSupervisorRoleRepository()
	assignmentRepo := NewMockSupervisorAssignmentRepository()
	svc := NewSupervisorService(roleRepo, assignmentRepo)

	tenantID := uuid.New()
	createdBy := uuid.New()
	permissions := []string{domain.PermissionViewAllAccounts}

	role, err := svc.CreateSupervisorRole(context.Background(), tenantID, createdBy, "Supervisor", "", permissions)

	if err != nil {
		t.Errorf("failed to create role: %v", err)
	}

	if role.Status != "active" {
		t.Errorf("role should be active")
	}
}

func TestAssignSupervisor(t *testing.T) {
	roleRepo := NewMockSupervisorRoleRepository()
	assignmentRepo := NewMockSupervisorAssignmentRepository()
	svc := NewSupervisorService(roleRepo, assignmentRepo)

	ctx := context.Background()
	tenantID := uuid.New()
	createdBy := uuid.New()
	userID := uuid.New()
	accountID := uuid.New()

	// Create role first
	role, _ := svc.CreateSupervisorRole(ctx, tenantID, createdBy, "Supervisor", "", []string{domain.PermissionViewAllAccounts})

	// Assign supervisor
	assignment, err := svc.AssignSupervisor(ctx, tenantID, userID, role.ID, createdBy, []uuid.UUID{accountID})

	if err != nil {
		t.Errorf("failed to assign supervisor: %v", err)
	}

	if !assignment.CanSuperviseAccount(accountID) {
		t.Errorf("assignment should supervise account")
	}
}

func TestCanUserSuperviseAccount(t *testing.T) {
	roleRepo := NewMockSupervisorRoleRepository()
	assignmentRepo := NewMockSupervisorAssignmentRepository()
	svc := NewSupervisorService(roleRepo, assignmentRepo)

	ctx := context.Background()
	tenantID := uuid.New()
	createdBy := uuid.New()
	userID := uuid.New()
	accountID := uuid.New()

	// Create role and assignment
	role, _ := svc.CreateSupervisorRole(ctx, tenantID, createdBy, "Supervisor", "", []string{domain.PermissionViewAllAccounts})
	svc.AssignSupervisor(ctx, tenantID, userID, role.ID, createdBy, []uuid.UUID{accountID})

	// Check permission
	can, err := svc.CanUserSuperviseAccount(ctx, userID, accountID)

	if err != nil {
		t.Errorf("failed to check permission: %v", err)
	}

	if !can {
		t.Errorf("user should be able to supervise account")
	}
}

func TestRevokeAssignment(t *testing.T) {
	roleRepo := NewMockSupervisorRoleRepository()
	assignmentRepo := NewMockSupervisorAssignmentRepository()
	svc := NewSupervisorService(roleRepo, assignmentRepo)

	ctx := context.Background()
	tenantID := uuid.New()
	createdBy := uuid.New()
	userID := uuid.New()
	accountID := uuid.New()

	// Create and assign
	role, _ := svc.CreateSupervisorRole(ctx, tenantID, createdBy, "Supervisor", "", []string{})
	assignment, _ := svc.AssignSupervisor(ctx, tenantID, userID, role.ID, createdBy, []uuid.UUID{accountID})

	// Revoke
	err := svc.RevokeAssignment(ctx, assignment.ID)

	if err != nil {
		t.Errorf("failed to revoke assignment: %v", err)
	}

	// Check revoked
	retrieved, _ := svc.GetSupervisorAssignment(ctx, assignment.ID)
	if retrieved.Status != "inactive" {
		t.Errorf("assignment should be inactive after revoke")
	}
}
