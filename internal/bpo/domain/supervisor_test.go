package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewSupervisorRole(t *testing.T) {
	tenantID := uuid.New()
	createdBy := uuid.New()
	permissions := []string{PermissionViewAllAccounts, PermissionViewAuditTrail}

	role := NewSupervisorRole(tenantID, createdBy, "Supervisor", "Supervisor role", permissions)

	if role.ID == uuid.Nil {
		t.Errorf("role id should not be nil")
	}

	if role.Status != "active" {
		t.Errorf("new role should be active")
	}

	if len(role.Permissions) != 2 {
		t.Errorf("expected 2 permissions, got %d", len(role.Permissions))
	}
}

func TestSupervisorRoleHasPermission(t *testing.T) {
	tenantID := uuid.New()
	createdBy := uuid.New()
	permissions := []string{PermissionViewAllAccounts}

	role := NewSupervisorRole(tenantID, createdBy, "Supervisor", "", permissions)

	if !role.HasPermission(PermissionViewAllAccounts) {
		t.Errorf("should have view all accounts permission")
	}

	if role.HasPermission(PermissionSuspendAccount) {
		t.Errorf("should not have suspend account permission")
	}
}

func TestSupervisorRoleAddPermission(t *testing.T) {
	tenantID := uuid.New()
	createdBy := uuid.New()

	role := NewSupervisorRole(tenantID, createdBy, "Supervisor", "", []string{})

	role.AddPermission(PermissionViewAllAccounts)

	if !role.HasPermission(PermissionViewAllAccounts) {
		t.Errorf("permission should be added")
	}

	// Adding same permission again should not duplicate
	role.AddPermission(PermissionViewAllAccounts)

	if len(role.Permissions) != 1 {
		t.Errorf("expected 1 permission, got %d", len(role.Permissions))
	}
}

func TestNewSupervisorAssignment(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()
	assignedBy := uuid.New()
	accounts := []uuid.UUID{uuid.New()}

	assignment := NewSupervisorAssignment(tenantID, userID, roleID, assignedBy, accounts)

	if assignment.ID == uuid.Nil {
		t.Errorf("assignment id should not be nil")
	}

	if assignment.Status != "active" {
		t.Errorf("new assignment should be active")
	}

	if !assignment.IsActive() {
		t.Errorf("assignment should be active")
	}
}

func TestSupervisorAssignmentCanSuperviseAccount(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()
	assignedBy := uuid.New()
	accountID := uuid.New()

	assignment := NewSupervisorAssignment(tenantID, userID, roleID, assignedBy, []uuid.UUID{accountID})

	if !assignment.CanSuperviseAccount(accountID) {
		t.Errorf("should be able to supervise assigned account")
	}

	otherAccountID := uuid.New()
	if assignment.CanSuperviseAccount(otherAccountID) {
		t.Errorf("should not be able to supervise unassigned account")
	}
}

func TestSupervisorAssignmentAddRemoveAccount(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()
	assignedBy := uuid.New()

	assignment := NewSupervisorAssignment(tenantID, userID, roleID, assignedBy, []uuid.UUID{})

	accountID := uuid.New()
	assignment.AddAccount(accountID)

	if !assignment.CanSuperviseAccount(accountID) {
		t.Errorf("should be able to supervise after adding")
	}

	assignment.RemoveAccount(accountID)

	if assignment.CanSuperviseAccount(accountID) {
		t.Errorf("should not be able to supervise after removing")
	}
}
