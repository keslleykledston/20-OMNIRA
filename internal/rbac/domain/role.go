package domain

import (
	"github.com/google/uuid"
)

// PermissionAction — ação que uma permission autoriza
type PermissionAction string

const (
	ActionRead   PermissionAction = "read"
	ActionWrite  PermissionAction = "write"
	ActionAdmin  PermissionAction = "admin"
	ActionDelete PermissionAction = "delete"
)

// PermissionResource — recurso que uma permission protege
type PermissionResource string

const (
	ResourceTenant     PermissionResource = "tenant"
	ResourceMembership PermissionResource = "membership"
	ResourceAudit      PermissionResource = "audit"
	ResourceSettings   PermissionResource = "settings"
	ResourceAgent      PermissionResource = "agent"
)

// Permission — permissão com recurso + ação
type Permission struct {
	ID       uuid.UUID
	Resource PermissionResource
	Action   PermissionAction
	Name     string // human-readable: "read:tenant", "write:membership"
}

// Role — função com conjunto de permissões
type Role struct {
	ID          uuid.UUID
	TenantID    uuid.UUID    // nil for system roles
	Name        string       // "admin", "viewer", "editor"
	Description string
	IsSystem    bool
	Permissions []*Permission
	CreatedAt   string
}

// HasPermission — verifica se role tem permissão específica
func (r *Role) HasPermission(resource PermissionResource, action PermissionAction) bool {
	if r == nil {
		return false
	}

	for _, perm := range r.Permissions {
		if perm.Resource != resource {
			continue
		}
		// admin num recurso implica as demais ações somente nesse mesmo recurso.
		if perm.Action == action || perm.Action == ActionAdmin {
			return true
		}
	}

	return false
}

// CanRead — shorthand para read permission
func (r *Role) CanRead(resource PermissionResource) bool {
	return r.HasPermission(resource, ActionRead)
}

// CanWrite — shorthand para write permission
func (r *Role) CanWrite(resource PermissionResource) bool {
	return r.HasPermission(resource, ActionWrite)
}

// CanAdmin — shorthand para admin permission
func (r *Role) CanAdmin(resource PermissionResource) bool {
	return r.HasPermission(resource, ActionAdmin)
}

// SystemRoles — roles de sistema pré-definidos.
// NOTA: nomes devem corresponder à migração 000002_memberships_rbac (Git source of truth).
// Ver também: migrations/000002_memberships_rbac.up.sql
var SystemRoles = map[string]*Role{
	// system_admin: full platform access across all tenants
	"system_admin": {
		Name:        "system_admin",
		IsSystem:    true,
		Description: "System administrator - full platform access",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionAdmin},
			{Resource: ResourceMembership, Action: ActionAdmin},
			{Resource: ResourceAudit, Action: ActionAdmin},
			{Resource: ResourceSettings, Action: ActionAdmin},
		},
	},
	// tenant_admin: full access within a tenant
	"tenant_admin": {
		Name:        "tenant_admin",
		IsSystem:    true,
		Description: "Tenant administrator - full access within tenant",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionRead},
			{Resource: ResourceMembership, Action: ActionAdmin},
			{Resource: ResourceAudit, Action: ActionRead},
			{Resource: ResourceSettings, Action: ActionAdmin},
			{Resource: ResourceAgent, Action: ActionAdmin},
		},
	},
	// tenant_supervisor: supervisor/lead role
	"tenant_supervisor": {
		Name:        "tenant_supervisor",
		IsSystem:    true,
		Description: "Tenant supervisor - can read and review data",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionRead},
			{Resource: ResourceMembership, Action: ActionRead},
			{Resource: ResourceAudit, Action: ActionRead},
			{Resource: ResourceAgent, Action: ActionRead},
		},
	},
	// tenant_agent: agent/operator role
	"tenant_agent": {
		Name:        "tenant_agent",
		IsSystem:    true,
		Description: "Tenant agent - basic access to tenant",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionRead},
		},
	},
	// hub_admin: hub-level administrator
	"hub_admin": {
		Name:        "hub_admin",
		IsSystem:    true,
		Description: "Hub administrator - full hub access",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionAdmin},
			{Resource: ResourceMembership, Action: ActionAdmin},
			{Resource: ResourceAudit, Action: ActionAdmin},
		},
	},
}

// NewTenantRole — cria novo role customizado para tenant
func NewTenantRole(tenantID uuid.UUID, name string, description string, permissions []*Permission) *Role {
	return &Role{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Name:        name,
		Description: description,
		IsSystem:    false,
		Permissions: permissions,
		CreatedAt:   "", // set by repository
	}
}
