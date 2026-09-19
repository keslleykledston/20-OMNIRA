package domain

import (
	"time"

	"github.com/google/uuid"
)

// SupervisorRole — role de supervisor
type SupervisorRole struct {
	ID               uuid.UUID              `json:"id"`
	TenantID         uuid.UUID              `json:"tenant_id"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	Permissions      []string               `json:"permissions"`
	Status           string                 `json:"status"` // active, inactive
	MaxAccountsView  int                    `json:"max_accounts_view"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
	CreatedBy        uuid.UUID              `json:"created_by"`
	Metadata         map[string]interface{} `json:"metadata"`
}

// SupervisorPermission — constantes de permissões
const (
	PermissionViewAllAccounts     = "view:all_accounts"
	PermissionViewAccountMetrics  = "view:account_metrics"
	PermissionViewAuditTrail      = "view:audit_trail"
	PermissionManageTicketRouting = "manage:ticket_routing"
	PermissionEscalateTicket      = "escalate:ticket"
	PermissionReassignTicket      = "reassign:ticket"
	PermissionViewSLAReports      = "view:sla_reports"
	PermissionManageSLAConfig     = "manage:sla_config"
	PermissionSuspendAccount      = "suspend:account"
	PermissionGenerateReports     = "generate:reports"
)

// NewSupervisorRole — cria nova supervisorRole
func NewSupervisorRole(tenantID, createdBy uuid.UUID, name, description string, permissions []string) *SupervisorRole {
	return &SupervisorRole{
		ID:              uuid.New(),
		TenantID:        tenantID,
		Name:            name,
		Description:     description,
		Permissions:     permissions,
		Status:          "active",
		MaxAccountsView: 999,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		CreatedBy:       createdBy,
		Metadata:        make(map[string]interface{}),
	}
}

// HasPermission — verifica se role tem permissão
func (r *SupervisorRole) HasPermission(permission string) bool {
	for _, p := range r.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

// AddPermission — adiciona permissão
func (r *SupervisorRole) AddPermission(permission string) {
	if !r.HasPermission(permission) {
		r.Permissions = append(r.Permissions, permission)
		r.UpdatedAt = time.Now().UTC()
	}
}

// RemovePermission — remove permissão
func (r *SupervisorRole) RemovePermission(permission string) {
	for i, p := range r.Permissions {
		if p == permission {
			r.Permissions = append(r.Permissions[:i], r.Permissions[i+1:]...)
			r.UpdatedAt = time.Now().UTC()
			return
		}
	}
}

// SupervisorAssignment — atribuição de supervisor a um usuário
type SupervisorAssignment struct {
	ID              uuid.UUID `json:"id"`
	TenantID        uuid.UUID `json:"tenant_id"`
	UserID          uuid.UUID `json:"user_id"`
	SupervisorRole  uuid.UUID `json:"supervisor_role_id"`
	Status          string    `json:"status"` // active, inactive
	AssignedAt      time.Time `json:"assigned_at"`
	AssignedBy      uuid.UUID `json:"assigned_by"`
	AssignedAccounts []uuid.UUID `json:"assigned_accounts"` // Accounts que pode supervisionar
	Metadata        map[string]interface{} `json:"metadata"`
}

// NewSupervisorAssignment — cria nova atribuição
func NewSupervisorAssignment(tenantID, userID, roleID, assignedBy uuid.UUID, accounts []uuid.UUID) *SupervisorAssignment {
	return &SupervisorAssignment{
		ID:               uuid.New(),
		TenantID:         tenantID,
		UserID:           userID,
		SupervisorRole:   roleID,
		Status:           "active",
		AssignedAt:       time.Now().UTC(),
		AssignedBy:       assignedBy,
		AssignedAccounts: accounts,
		Metadata:         make(map[string]interface{}),
	}
}

// IsActive — verifica se atribuição está ativa
func (a *SupervisorAssignment) IsActive() bool {
	return a.Status == "active"
}

// CanSuperviseAccount — verifica se pode supervisionar account
func (a *SupervisorAssignment) CanSuperviseAccount(accountID uuid.UUID) bool {
	if !a.IsActive() {
		return false
	}
	for _, id := range a.AssignedAccounts {
		if id == accountID {
			return true
		}
	}
	return false
}

// AddAccount — adiciona account à supervisão
func (a *SupervisorAssignment) AddAccount(accountID uuid.UUID) {
	for _, id := range a.AssignedAccounts {
		if id == accountID {
			return
		}
	}
	a.AssignedAccounts = append(a.AssignedAccounts, accountID)
}

// RemoveAccount — remove account da supervisão
func (a *SupervisorAssignment) RemoveAccount(accountID uuid.UUID) {
	for i, id := range a.AssignedAccounts {
		if id == accountID {
			a.AssignedAccounts = append(a.AssignedAccounts[:i], a.AssignedAccounts[i+1:]...)
			return
		}
	}
}
